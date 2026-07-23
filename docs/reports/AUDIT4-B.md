# AUDIT4-B — Ronda 4: CONFIRMACIÓN DE CIERRE. Scope RUNTIME / REPLICACIÓN / CAPTURA

**Repo:** `C:\Users\Administrador\Documents\hostinger\sqlite-postgres-compat` (HEAD `4dba30d`, rama `main`).
**Scope:** `compat/capture.go`, `store.go`, `replicate.go`, `journal.go`, `verify.go`, `inspect.go`, `runtime.go`.
**Rol:** auditor efímero sin memoria previa. RONDA 4 de confirmación de cierre + ataque al diseño del marcador (FIX-R1).
**Motor:** `go1.26.4 windows/amd64`, `modernc.org/sqlite v1.54.0`, `github.com/jackc/pgx/v5 v5.10.0`.
**Método:** cada conclusión lleva **evidencia ejecutada** sobre SQLite `:memory:` real (programa Go temporal `compat/zz_audit4_test.go` que llama funciones privadas del paquete `compat`, borrado al terminar — `git status` limpio). Lo que exige PostgreSQL vivo se marca **NO VERIFICADO**. Temporales borrados; árbol limpio; `go build ./...` y `go vet ./...` en verde antes y después.

---

## Resumen ejecutivo

**Todos los hallazgos previos del scope (ALTA, MEDIA y BAJA arreglados) están CERRADOS** sobre HEAD: la precisión float alta magnitud, el decimal REAL-stored sin conflicto espurio, el decimal TEXT verbatim end-to-end (`0.10000000000000001` intacto), el rechazo de Inf/NaN en `FloatType` y el `ConflictError` tipado en INSERT duplicado ya no se reproducen.

El **ataque al diseño del marcador (FIX-R1)** no encontró bugs de severidad ALTA/MEDIA. El marcador (`\x01real`) es **inasequible por construcción** en columnas `FloatType`/`TextType`, **no filtra** a snapshots ni digests, los UPDATE con cambio de `typeof` (text↔real, integer↔real) son **coherentes**, el UPDATE de PK sobre DECIMAL real-stored **funciona** (PK capturado de OLD), y el DELETE con before real-stored **aplica** limpio.

Se hallaron **2 hallazgos BAJA nuevos**, ambos **no silenciosos** y uno ya documentado:
1. **BAJA (nuevo):** un journal capturado por triggers **anteriores al fix** (sin marcador), aplicado con código nuevo a un destino canónico TEXT, **diverge** para decimales REAL-stored de alta magnitud — `ApplyChanges` no errora, pero `VerifySnapshots` lo detecta (`equivalent=false`). No está documentado. Requiere un escenario mixto de versión (journal in-flight pre-upgrade aplicado post-upgrade), fuera del contrato de migración documentado (capture→snapshot→catch-up drena el journal antes de cambiar el código).
2. **BAJA (ya documentado en FIX-R1):** un input fuera de contrato `"\x01real0.1"` en una columna `DECIMAL` TEXT se reescribe silenciosamente a `"0.1"` (corrupción de dato no válido, ya aceptada como trade-off del centinela reservado).

**Conteo final: previos cerrados 7/7 · nuevos ALTA 0 / MEDIA 0 / BAJA 2 (1 nuevo, 1 ya documentado).**

---

## PARTE 1 — Confirmación de cierre

Cada hallazgo previo del scope se re-ejecutó sobre HEAD con su repro original.

| Hallazgo (origen) | Severidad | Repro sobre HEAD | Estado |
|---|---|---|---|
| Float alta magnitud — pérdida silenciosa de precisión (AUDIT2-B H1) | ALTA | INSERT `123456789012345.6789` en FLOAT, captura, apply, comparar `printf('%!.17g')` origen vs destino | **CERRADO** |
| Decimal REAL-stored — `ConflictError` espurio (AUDIT2-B H2) | ALTA | Tabla NUMERIC-affinity, INSERT+UPDATE de alta magnitud, apply estricto | **CERRADO** |
| Decimal TEXT verbatim `0.10000000000000001` (AUDIT3-B ALTA-1) | ALTA | Cadena captura→journal→apply→verify, byte-a-byte | **CERRADO** |
| `FloatType` no rechazaba Inf/NaN (AUDIT3-B BAJA-2) | BAJA | `canonicalValue(FloatType, "inf"/"NaN"/±Inf/NaN)` | **CERRADO** |
| INSERT duplicado modo estricto → error opaco (AUDIT3-B BAJA-3) | BAJA | `ApplyChanges` estricto con PK ya existente | **CERRADO** |
| Comentarios `%.17g` vs `%!.17g` (AUDIT3-B BAJA-1) | BAJA | `grep "printf('%.17g'"` en fuente no-test | **CERRADO** |
| Tolerant: igualdad de estado final enmascara divergencia genuina (AUDIT2-B H3) | MEDIA | (trade-off documentado, no bug de código) | **SIN CAMBIO (by design)** |

### Evidencia ejecutada

**AUDIT2-B H1 — float alta magnitud (CERRADO).**
```
CAPTURE after flt="1.2345678901234567e+14" (printf 17g source="123456789012345.67")
source printf="123456789012345.67"  dest printf="123456789012345.67"
CERRADO: round-trip exacto
```
El destino reconstruye bit-idéntico el `float64` del origen: el `printf('%!.17g')` de la captura (rama `FloatType`, `capture.go:174`) ya no pierde precisión como el viejo `CAST(REAL AS TEXT)`, y `normalizeFloat` canoniza ambos al mismo `1.2345678901234567e+14`.

**AUDIT2-B H2 — decimal REAL-stored, sin conflicto espurio (CERRADO).**
```
changes=2  c0.after={decimal 1.2345678901234567e+14}  c1.before={decimal 1.2345678901234567e+14} c1.after={decimal 9.999999999999998e+13}
CERRADO: sin ConflictError espurio   (replay idempotente también OK)
```
Tabla `NUMERIC(38,18)` externa (afinidad NUMERIC → almacenamiento REAL). El trigger emite `MARKER || printf('%!.17g')` en la rama `typeof='real'`; `canonicalValue` strip+`normalizeFloat` converge con el `float64` leído del destino. Ya no hay `ConflictError` espurio para alta magnitud/precisión.

**AUDIT3-B ALTA-1 — decimal TEXT verbatim `0.10000000000000001` (CERRADO).**
```
SRC stored="0.10000000000000001"  CAPTURE after="0.10000000000000001"
DST stored="0.10000000000000001"  VERIFY equivalent=true
CERRADO: 0.10000000000000001 intacto end-to-end; verify equivalent=true
```
Columna `DECIMAL` canónica → `TEXT` (`ddl.go:125-128`), `typeof='text'` → `CAST` verbatim **sin marcador** → `canonicalValue` no es `float64`, no lleva marcador → **verbatim**. La captura NO altera el texto (ya probado en el punto de captura, antes del apply). `VerifySnapshots` ahora reporta `equivalent=true` **sin enmascarar divergencia**: ambos lados almacenan físicamente `0.10000000000000001` idéntico (a diferencia del bug ALTA-1 original, donde el origen `0.10000000000000001` y el destino `0.1` divergían pero verify canonizaba ambos a `0.1`).

**AUDIT3-B BAJA-2 — `FloatType` rechaza Inf/NaN (CERRADO).**
```
FLOAT in="inf"  err=invalid float "inf": Inf/NaN are not supported
FLOAT in="+inf"/"-inf"/"Inf"/"nan"/"NaN"  → todos rechazados
float64(+Inf) err=invalid float "+Inf": Inf/NaN are not supported
float64(NaN)  err=invalid float "NaN": Inf/NaN are not supported
FLOAT 1.5 -> "1.5" err=<nil>
CERRADO: Inf/NaN rechazados, finito OK
```
La rama `FloatType` (`store.go:325-332`) ahora parsea y rechaza no-finitos con error claro, en paridad con el gate que la rama `DecimalType` mantiene vía el marcador. Los finitos canonicalizan igual.

**AUDIT3-B BAJA-3 — INSERT duplicado → `ConflictError` tipado (CERRADO).**
```
err=replication conflict on a4_dup primary key map[id:{integer 1}]: expected {..."remote"...}, actual {..."local"...}  isConflict=true
CERRADO: ConflictError tipado tabla="a4_dup"; fila existente intacta="local"
```
`applyChange` rama `Insert` estricta (`replicate.go:210-216`) envuelve la colisión de PK en `*ConflictError` (tabla, PK, filas divergentes) sin enmascarar otros fallos; la fila existente queda intacta (rollback).

**AUDIT3-B BAJA-1 — comentarios `%.17g` vs `%!.17g` (CERRADO).**
`grep "printf('%.17g'"` sobre fuente no-test no encuentra ocurrencias sin `!`. Los comentarios citan fielmente `%!.17g`. Alineado.

**AUDIT2-B H3 — tolerant, igualdad de estado final (SIN CAMBIO, by design).**
Este no era un bug de código a cerrar sino un **trade-off documentado e inherente** (`store.go:38-56` / `replicate.go:38-56`): en modo `Tolerant`, una fila destino cuyo estado final coincide con `change.After` se trata como ya-aplicada, aun si la coincidencia es casual. El comportamiento sigue existiendo por diseño; no es una regresión ni objetivo de fix. Se confirma que **no fue alterado** por FIX-R1 (el marcador no toca la política tolerant). Queda como decisión de producto pendiente de sign-off del mantenedor, tal como la dejó AUDIT2-B.

---

## PARTE 2 — Ataque al diseño del marcador (FIX-R1)

Diseño bajo ataque (`capture.go:176-191`, `store.go:281-317`, `store.go:430-440`): el trigger de captura emite `realDecimalMarker || printf('%!.17g', col)` **solo** en la rama `typeof='real'` de `DecimalType` (SQLite); las demás ramas y todo Postgres emiten `CAST AS TEXT` verbatim. `canonicalValue(DecimalType)` normaliza **solo** si el valor llega como `float64` del driver o como texto con prefijo `realDecimalMarker`; el resto pasa verbatim.

### (a) Fila DECIMAL que cambia de `typeof` entre UPDATE — ¿before/after coherentes?

**Coherentes.** Probé dos escenarios en una columna `NUMERIC(38,18)` externa (la única donde `typeof` puede variar; el `DECIMAL` canónico es siempre `TEXT`):

**Cambio fractional↔fractional (text→real y real→text por afinidad NUMERIC):**
```
change 0 insert pk={id:1} after.amount="0.1"        (insert "0.10000000000000001" -> NUMERIC lo vuelve REAL)
change 1 update before.amount="0.1" after.amount="0.1"
change 2 insert after.amount="0.5"
change 3 update before.amount="0.5" after.amount="1.5"   (update a "1.50" -> REAL)
apply err=<nil>   dst id1="0.1" dst id2="1.5"
VERIFY equivalent=true src=4228e018... dst=4228e018...
```

**Cambio integer↔real (los casos realistas: "100"→INTEGER, 0.5→REAL):**
```
change 1 update before.amount="100" after.amount="0.5"   (integer->real: before verbatim, after marker+normalize)
change 3 update before.amount="0.5" after.amount="100"   (real->integer: before marker+normalize, after verbatim)
apply err=<nil>   VERIFY equivalent=true
```
La rama `integer` emite `CAST` verbatim (sin marcador, sin `normalizeFloat`); la rama `real` emite marcador+`printf` y se normaliza. Ante un cambio de `typeof`, el `Before` y el `After` se canonizan por la rama que corresponde a cada uno, y ambos son coherentes con el estado vivo del destino (`loadRow` lee el valor almacenado y lo canoniza por la misma regla). El UPDATE aplica y `VerifySnapshots` da `equivalent=true`. **Limpio.**

### (b) Marcador en columnas FloatType/TextType — ¿imposible o alcanzable?

**Imposible por construcción.** Inspección del SQL del trigger compilado:
```
FloatType trigger contiene marcador? false
TextType  trigger contiene marcador? false
FloatType (insert): ... 'f', CASE WHEN NEW."f" IS NULL THEN NULL ELSE printf('%!.17g', NEW."f") END ...
```
`FloatType` usa `printf('%!.17g')` **sin marcador** (`capture.go:161-175`); `TextType`/`IntegerType`/demás usan `CAST AS TEXT`. El marcador solo aparece en la rama `DecimalType`/`typeof='real'`. Es inasequible por la propia SQL del trigger.

Inyección del centinela en `TextType` (dato fuera de contrato): `canonicalValue(TextType, "\x01real0.1")` → verbatim `"\x01real0.1"` (la rama `TextType` no llama a `cutRealDecimalMarker`, solo lo hace `DecimalType`). No hay strippado indebido en `TextType`. **Limpio.** (El caso análogo en `DecimalType` se trata en el hallazgo BAJA de colisión, abajo.)

### (c) UPDATE de PK sobre tabla con DECIMAL real-stored

**Funciona; el decimal es coherente.**
```
change 0 insert pk={id:1} after={amount:{decimal 1.2345678901234567e+14} id:{integer 1}}
change 1 update pk={id:1} before={...id:1 amount:1.2345678901234567e+14} after={...id:2 amount:1.2345678901234567e+14}
apply err=<nil>   dst count=1 ids="2"
```
Observación clave: para `UPDATE`, el trigger captura la **PK de `OLD`** (`capture.go:101`, `primaryAlias="OLD"`), no de `NEW`. Así `change.PrimaryKey` identifica la fila en su estado previo (donde el destino aún la tiene), `loadRow` la encuentra, `rowsEqual(Before, actual)` casa, y `updateRow` hace `SET` de **todas** las columnas (incluida la PK nueva) con `WHERE` sobre la PK vieja → la fila migra `id 1→2`. El `DECIMAL` real-stored (`1.2345678901234567e+14`) se mantiene coherente en before/after. **Limpio.** (Nota: esto es correcto para CDC monótono; un cambio posterior que referencie la fila usa la nueva PK como su `OLD`.)

### (d) Journal VIEJO sin marcador aplicado con código nuevo

**Diverge para alta magnitud, pero verify lo detecta (no silencioso). No documentado.**

Simulé un trigger **anterior al fix** (rama `real` = `printf('%!.17g')` **sin** marcador), capturé, y apliqué con `canonicalValue` nuevo a un destino **canónico TEXT** (el caso real de replicación SQLite-NUMERIC → SQLite-canónico):

```
journal VIEJO after_row={"id":"1","amount":"123456789012345.67"}   (sin marcador)
decoded after.amount="123456789012345.67"   (código nuevo: verbatim, sin strip)
DST stored="123456789012345.67"
srcSnap=[{amount:{decimal 1.2345678901234567e+14} id:1}]           (origen REAL -> float64 -> normalize)
dstSnap=[{amount:{decimal 123456789012345.67} id:1}]               (destino TEXT verbatim)
VERIFY equivalent=false  src=536a9624... dst=b5237fb0...
Resultado: viejo journal alta-magnitud DIVERGE — verify lo detecta (no silencioso)
```

**Qué pasa:** el código nuevo trata el texto sin marcador como `TEXT` arbitrario (verbatim), así que el `printf` 17-dígitos del journal viejo se aplica tal cual al destino TEXT, mientras el origen (REAL) se canoniza a `1.2345678901234567e+14`. `ApplyChanges` **no errora** (la fila se inserta); la divergencia **solo** la atrapa un `VerifySnapshots` explícito (`equivalent=false`). Bajo el código viejo (`isCompactFloatText`), ese mismo journal se reconciliaba y verify daba `true`.

**No es silencioso** (verify lo atrapa) ni alcanza a valores de forma más corta (p. ej. `0.1` → `printf` da `"0.1"` == canon del `float64`, verificado: el journal viejo de `0.1` SÍ coincide). Solo diverge para `printf` de 17 dígitos.

**¿Documentado?** No. `FIX-R1-DECIMAL-REPORT.md` no menciona compatibilidad de journals in-flight capturados por triggers pre-fix ni migración del marcador. El contrato de migración documentado (capture-install → snapshot → catch-up) drena el journal antes de cualquier cambio de código, así que el escenario mixto de versión está fuera de contrato. → **BAJA nuevo** (ver Hallazgos nuevos).

### (e) VerifySnapshots / ExportSnapshot — ¿el marcador filtra a un snapshot o digest?

**No filtra. Limpio.**
```
journal raw seq1={"id":"1","amount":"real0.1"}   (marcador presente en el TEXT del journal: JSON-escape )
snapshot row id=1 amount="0.1" (marker? false)
snapshot row id=2 amount="0.1" (marker? false)        (fila TEXT "0.10000000000000001")
digest="b400b9fa..."
LIMPIO: marcador no filtra a snapshot/digest (strippado en decode / float64 en export)
```
El marcador vive **transitoriamente** en la columna TEXT del journal; `decodeCapturedRow` lo stripa (`cutRealDecimalMarker`) antes de construir el `Value`. `ExportSnapshot`/`loadRow` leen por driver: REAL → `float64` → `normalizeFloat` (sin marcador); TEXT → string verbatim (el trigger TEXT no emite marcador). Ningún `Value` de snapshot/digest lleva el prefijo `\x01real`. **Limpio.**

### (f) DELETE con before de decimal real-stored

**Limpio.**
```
change 0 insert before.amount=""
change 1 delete before.amount="9.999999999999998e+13"   (before real-stored -> marker+printf -> strip+normalize)
apply err=<nil>   dst count=0 (esperado 0)
CERRADO/LIMPIO: DELETE real-stored aplicado, fila eliminada   (replay idempotente OK)
```
El `Before` del DELETE se captura de `OLD` con la rama `typeof='real'` (marcador+`printf`), se stripa y normaliza a `9.999999999999998e+13`. En el destino, `loadRow` lee el `float64` REAL del insert previo y canoniza al mismo valor → `rowsEqual(Before, actual)` casa → `deleteRow` elimina la fila. **Limpio.**

---

## Hallazgos nuevos

### BAJA-N1 — Journal in-flight capturado por triggers pre-fix (sin marcador) diverge bajo código nuevo (alta magnitud); detectado por verify, no documentado

**Severidad:** BAJA. **Archivo:línea:** `compat/store.go:281-317` (rama `DecimalType`: normaliza solo con marcador o `float64`), `compat/capture.go:176-191` (trigger emite marcador solo en rama `real`).

**Qué pasa:** un `Change` cuya columna `DECIMAL` proviene de un journal capturado por triggers **anteriores a FIX-R1** (rama `real` = `printf('%!.17g')` sin marcador), aplicado con `canonicalValue` nuevo, se trata como `TEXT` verbatim. Para valores de alta magnitud (17 dígitos), el destino canónico TEXT almacena el `printf` textual (`"123456789012345.67"`) mientras el origen REAL canoniza a `"1.2345678901234567e+14"`. `ApplyChanges` no errora; la divergencia solo la atrapa `VerifySnapshots` (`equivalent=false`). Evidencia ejecutada en **(d)** arriba.

**Por qué BAJA:** (1) **no silencioso** — `VerifySnapshots` lo detecta; (2) requiere un escenario **mixto de versión** (journal in-flight pre-upgrade aplicado post-upgrade), fuera del contrato de migración documentado (el catch-up drena el journal antes de cambiar el código); (3) no afecta a valores de forma corta (`0.1` coincide). Es una **regresión de comportamiento** acotada y ruidosa, no corrupción silenciosa.

**Recomendación (no aplicada — read-only):** documentar que un upgrade de código con journals in-flight pre-fix requiere drenar el journal antes del upgrade, o que `canonicalValue` reconciliation del `DecimalType` no es retrocompatible con journals sin marcador. Bastaría una nota operativa en `FIX-R1-DECIMAL-REPORT.md` / `OPERATIONS.md`.

### BAJA-N2 — Colisión del marcador con input fuera de contrato `"\x01real…"` (ya documentado en FIX-R1)

**Severidad:** BAJA (trade-off aceptado). **Archivo:línea:** `compat/store.go:310` (`cutRealDecimalMarker`).

**Qué pasa:** un valor `DECIMAL` TEXT cuyo texto **arranca literalmente** con `realDecimalMarker` (`"\x01real0.1"`) se interpreta como REAL-stored y se reescribe silenciosamente:
```
SRC stored="\x01real0.1"  CAPTURE after strippado a "0.1"
DST stored="0.1"  VERIFY equivalent=true   (ambos lados canonizan a "0.1" -> divergencia enmascarada)
```
Es **silencioso** (verify `equivalent=true`) pero sobre **dato fuera de contrato**: `"\x01real0.1"` no es un decimal válido (arranca con SOH, un byte de control). El marcador es un centinela interno reservado de la capa de captura. **Ya está documentado y aceptado** en `FIX-R1-DECIMAL-REPORT.md:67` ("Colisión teórica… dato fuera de contrato… el marcador es un centinela reservado"). Se reconfirma aquí con evidencia ejecutada; no es nuevo.

---

## Áreas limpias (verificadas en esta ronda)

- **Marcador inasequible en FloatType/TextType** por construcción del trigger (b). Trigger SQL inspeccionado: marcador solo en `DecimalType`/`typeof='real'`.
- **Sin fuga a snapshots/digests** (e): el marcador se stripa en `decodeCapturedRow` y nunca llega a un `Value`; `ExportSnapshot` lee `float64`/string sin marcador.
- **Cambio de `typeof` en UPDATE coherente** (a): text↔real e integer↔real aplican y verify `equivalent=true`.
- **UPDATE de PK sobre DECIMAL real-stored** (c): PK capturada de `OLD`, `updateRow` migra la fila, decimal coherente.
- **DELETE con before real-stored** (f): before se normaliza vía marcador y casa con el `float64` del destino; aplica e idempotente.
- **Reconciliación REAL-stored** (propósito del fix `6e9c2f6`/FIX-R1): alta magnitud y `99999999999999.99` convergen sin `ConflictError` espurio, replay idempotente.
- **`FloatType` alta magnitud**: round-trip bit-exacto; `Inf`/`NaN` rechazados; finito canonicaliza.
- **`ConflictError` tipado** en INSERT duplicado estricto, fila intacta.
- **`decodeCapturedRow`/`requireAllColumns`**: split PK vs fila completa correcto (sin cambios desde AUDIT3-B).
- **`ApplyChanges`/`ApplyChangesTolerant`** idempotencia y anti-echo (supresión SQLite single-conn) sin cambios; el marcador no altera la política tolerant (H3 sigue by design).

## NO VERIFICADO (sin PostgreSQL vivo)

- **Origen Postgres `NUMERIC`/`float8`**: la captura de PG usa `CAST AS TEXT` sin gate `typeof` y **sin marcador** (`capture.go` rama Postgres). El diseño asume que el texto de PG ya round-tripea (float8 shortest; NUMERIC arbitrario). Comportamiento del driver pgx v5.10.0 para `NUMERIC` (string vs `float64`) y `float8` con `Infinity` (Postgres admite `Infinity` en float8 → la rama `FloatType` lo rechazaría) **NO VERIFICADO** sin PG vivo. La lógica de función pura (strip del marcador, `normalizeFloat`, rechazo Inf/NaN) sí está verificada.
- **`inspectPostgres` refactor** y la captura/apply contra PG vivo: revisión de lógica limpia en AUDIT3-B; ejecución **NO VERIFICADA**.
- **Changelogs de deps** (`pgx`, `modernc/sqlite`): sin red; no revisados. El comportamiento de `printf('%!.17g')` y afinidad de almacenamiento **sí** están verificados ejecutando sobre modernc v1.54.0.

---

## Definición de hecho

- [x] Tabla de confirmación (7 hallazgos previos del scope).
- [x] Hallazgos nuevos con evidencia ejecutada (BAJA-N1 nuevo, BAJA-N2 ya documentado).
- [x] Áreas limpias explícitas.
- [x] Ataque al marcador (a)–(f) con evidencia cada uno.
- [x] Conteo final.

**Conteo final: previos cerrados 7/7 · nuevos ALTA 0 / MEDIA 0 / BAJA 2 (1 nuevo + 1 ya documentado).**