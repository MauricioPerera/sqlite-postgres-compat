# AUDIT3-B — Ronda 3, scope RUNTIME / REPLICACIÓN / CAPTURA

**Repo:** `C:\Users\Administrador\Documents\hostinger\sqlite-postgres-compat` (HEAD `c09ee8f`, rama `main`, árbol limpio).
**Scope:** `compat/capture.go`, `store.go`, `replicate.go`, `journal.go`, `verify.go`, `inspect.go`, `runtime.go`.
**Foco:** código recién cambiado — fix de precisión float/decimal (`6e9c2f6`), refactor de `inspectPostgres` en fases (`b62128d`), aplanado de funciones (`983c109`), actualización de deps (`d5fb0aa`).
**Método:** cada hallazgo ALTA/MEDIA lleva evidencia ejecutada (programa Go temporal sobre SQLite real vía modernc; PostgreSQL **no** está montado → lo que exige PG vivo se marca **NO VERIFICADO**). Los temporales se borraron al terminar.
**Motor de prueba:** `go1.26.4 windows/amd64`, `modernc.org/sqlite v1.54.0`, `github.com/jackc/pgx/v5 v5.10.0`. Suite `go test ./compat/` en verde.

---

## Resumen ejecutivo

Se encontró **1 hallazgo ALTA**: divergencia silenciosa de datos en `DECIMAL` de precisión arbitraria, introducida por el fix de reconciliación float/decimal (`6e9c2f6`). El umbral “≤17 dígitos significativos” de `isCompactFloatText` no distingue un valor de precisión arbitraria almacenado como `TEXT` (el diseño canónico de `DECIMAL` en SQLite, `ddl.go:125-128`) de la salida `printf('%!.17g')` de un `REAL`. El resultado: un decimal legítimo como `0.10000000000000001` se reescribe a `0.1` **en la captura**, se aplica así al destino, y `VerifySnapshots` reporta `equivalent=true` porque ambos lados canonizan por la misma ruta lossy. Corrupción silenciosa e indetectable por verify, **alcanzable en SQLite** (motor principal), demostrada end-to-end.

El resto del scope está limpio o verificado para su propósito declarado: la reconciliación `REAL`-stored (el objetivo original del fix) funciona sin `ConflictError` espurio; `decodeCapturedRow`/`requireAllColumns` separa PK vs fila completa correctamente; `ApplyChanges`/`ApplyChangesTolerant` son idempotentes; el refactor de `inspectPostgres` preserva la lógica de ordenación y `Unresolved`. El camino Postgres vivo queda **NO VERIFICADO** (sin PG montado).

**Conteo final: ALTA 1 / MEDIA 0 / BAJA 3.**

---

## ALTA

### ALTA-1 — `DECIMAL` de precisión arbitraria silenciosamente corrompido/reformateado por `isCompactFloatText`

**Archivo:línea:** `compat/store.go:306-312` (rama `DecimalType` → `isCompactFloatText` → `normalizeFloat`), en cadena con `compat/capture.go:170-171` (emit `CAST AS TEXT` verbatim cuando `typeof != 'real'`) y `compat/store.go:425-451` (`isCompactFloatText`/`significantDigits`).

**Causa raíz.** El fix `6e9c2f6` introdujo `isCompactFloatText` para reconciliar `DECIMAL` almacenados como `REAL` (columnas `DECIMAL`/`NUMERIC` de bases SQLite *externas* con afinidad NUMERIC, cuyo valor fraccional SQLite guarda como `float64`). El heuristic decide “tratar como `float64`” cuando el texto **parsea como finito y tiene ≤17 dígitos significativos** (el límite de round-trip de un double). Pero **no verifica que el texto sea efectivamente la representación más corta que round-tripea** — solo cuenta dígitos.

Eso colisiona con el diseño canónico: `ddl.go:125-128` compila `DecimalType` en SQLite a **`TEXT`** (afinidad TEXT, precisamente para “preservar precisión arbitraria”). Un `DECIMAL` canónico en SQLite **nunca** se guarda como `REAL`; se guarda verbatim como `TEXT`. El trigger de captura (`capture.go:170-171`) emite `CAST(col AS TEXT)` (verbatim) cuando `typeof(col) != 'real'`. Para un `DECIMAL` canónico, `typeof` es siempre `text`, así que el journal lleva el texto exacto. Al decodificar, `decodeCapturedRow` pasa ese string a `canonicalValue(DecimalType, ...)`, que entra por `isCompactFloatText` y lo reescribe con `normalizeFloat` → `ParseFloat` + `FormatFloat('g', -1)`. **Cualquier decimal de precisión arbitraria con ≤17 dígitos significativos que no sea exactamente un `float64` se redondea al `float64` más cercano.**

**Dos manifestaciones, ambas alcanzables y silenciosas:**

1. **Cambio de valor** (corrupción real): `0.10000000000000001` → `0.1`. Son números racionales distintos.
2. **Pérdida de escala**: `1.50` → `1.5` (cae el cero significativo de escala; numéricamente igual, pero el texto almacenado diverge). Para un `DECIMAL(p,s)` declarado, la escala es semántica.

**Evidencia ejecutada (función pura, `canonicalValue`):**

```
DECIMAL in=<<0.10000000000000001>>  out=<<0.1>>  err=<nil>  changed=true
DECIMAL in=<<1.2345678901234567>>   out=<<1.2345678901234567>>  changed=false   (este SÍ round-tripea)
DECIMAL in=<<123456789012345.67>>   out=<<1.2345678901234567e+14>>  changed=true  (numéricamente igual, repr cambiada)
DECIMAL in=<<1.50>>                 out=<<1.5>>  changed=true   (pérdida de escala)
DECIMAL in=<<99999999999999999>>    out=<<99999999999999999>>  changed=false  (18 dígitos → verbatim, OK)
```

`0.30000000000000004` y `1.0000000000000002` **no** se alteran porque son artefactos `float64` que sí round-tripean. `0.10000000000000001` **sí** se altera porque no es la repr más corta de ningún `float64` (su `float64` más cercano es el de `0.1`).

**Evidencia ejecutada end-to-end (pipeline real del `Store`):** `ApplySchema` → `InstallChangeCapture` → `INSERT` `"0.10000000000000001"` → `ReadCapturedChanges` → `ApplyChanges` (destino) → `ExportSnapshot` + `VerifySnapshots`.

Programa temporal (`package compat`, SQLite `:memory:`):

```go
src := openAudit3Store(t, schema); dst := openAudit3Store(t, schema)
src.InstallChangeCapture(ctx, schema)
src.DB.ExecContext(ctx, "INSERT INTO t (id, amt) VALUES (?, ?)", 1, "0.10000000000000001")
src.DB.QueryRowContext(ctx, "SELECT CAST(amt AS TEXT) FROM t WHERE id=1").Scan(&srcStored)
changes, _ := src.ReadCapturedChanges(ctx, schema, 0, 100)
dst.ApplyChanges(ctx, schema, changes)
dst.DB.QueryRowContext(ctx, "SELECT CAST(amt AS TEXT) FROM t WHERE id=1").Scan(&dstStored)
srcSnap,_ := src.ExportSnapshot(ctx, schema); dstSnap,_ := dst.ExportSnapshot(ctx, schema)
rep,_ := VerifySnapshots(srcSnap, dstSnap)
```

Salida real:

```
SRC stored=<<0.10000000000000001>>
CAPTURE after=map[amt:{decimal 0.1} id:{integer 1}]      <- YA corrompido al decodificar la captura
DST stored=<<0.1>>
DIVERGENCE stored-differs=true
VERIFY equivalent=true  (source=30a9ed2e...5621 dest=30a9ed2e...5621)   <- digests idénticos
CONCLUSION silent=true
```

`typeof` de la columna `amt` (declarada por `ApplySchema`): **`text`** — confirma que es almacenamiento `TEXT` (afinidad TEXT), no `REAL`. La captura emitió `CAST(amt AS TEXT)` verbatim y `isCompactFloatText` lo reescribió.

**Por qué verify no lo detecta.** `VerifySnapshots` canoniza ambos lados por el mismo `canonicalValue` lossy. Origen físico `0.10000000000000001` → canon `0.1`; destino físico `0.1` → canon `0.1`. Digests idénticos → `Equivalent=true` aunque los datos almacenados difieren. **Divergencia silenciosa e indetectable** — exactamente la clase que define ALTA.

**Alcance/alcance de motor.** Alcanzable en **SQLite** (motor principal, vía el `DECIMAL` canónico = `TEXT`). También alcanzaría a un origen **Postgres `NUMERIC`** (la captura de PG usa `CAST AS TEXT` sin gate `typeof`, `capture.go:170` solo aplica el `CASE typeof` a `engine==SQLite`), pero el comportamiento exacto del driver pgx para `NUMERIC` (string vs `float64`) es **NO VERIFICADO** sin PG vivo; la lógica de la función pura (corrupción de string) ya está probada y no depende del driver.

**Comentario sobre la premisa del fix.** La premisa de `6e9c2f6` (“una columna `DECIMAL` de afinidad REAL cuyo valor fraccional SQLite guarda como `float64`”) **no aplica al `DECIMAL` canónico** (que es `TEXT`); aplica solo a `DECIMAL`/`NUMERIC` *externos* inspeccionados por `inspectSQLite`. El `isCompactFloatText` no puede distinguir “`TEXT` de precisión arbitraria” (canónico) de “`printf 17g` de un `float64`” (externo `REAL`), porque ambos llegan como string. El umbral de 17 dígitos pretende separarlos, pero los decimales de precisión arbitraria con ≤17 dígitos lo cruzan.

**Dirección de fix (sugerencia, no aplicada — auditoría read-only).** Antes de normalizar, verificar **round-trip**: tratar como `float64` solo si `FormatFloat(ParseFloat(text),'g',-1,64)` (o la forma canónica) **iguala al texto de entrada normalizado**. Eso admite la salida de `printf('%!.17g')` (que siempre es la repr más corta de su `float64`) y rechaza `0.10000000000000001` (no round-tripea → verbatim). Alternativamente, gatear la normalización al `DecimalType` solo cuando el origen provenga de almacenamiento `REAL` (p.ej. marcar el `Change`/`Value` con el `typeof` capturado) en vez de inferirlo heurísticamente del texto.

---

## BAJA

### BAJA-1 — `printf('%!.17g')` (forma alternate) vs. `%.17g` documentado

**Archivo:línea:** `compat/capture.go:158, 171` (código usa `printf('%!.17g', ...)`); comentarios en `capture.go:150,163,286` y `store.go:286` dicen `printf('%.17g')`.

El flag `%!` es la “forma alternate” del `printf` de SQLite: fuerza punto decimal y ceros finales no removidos. Verificado sobre modernc v1.54.0:

```
printf('%!.17g', 1.0)   = "1.0"      printf('%.17g', 1.0)   = "1"
printf('%!.17g', 100.0) = "100.0"    printf('%.17g', 100.0) = "100"
printf('%!.17g', 1e20)  = "1.0e+20"   printf('%.17g', 1e20)  = "1e+20"
printf('%!.17g', 1.2345678901234567e+14) = "123456789012345.67"  (idéntico a plain)
```

**Sin impacto funcional:** la forma alternate sigue siendo ≤17 dígitos significativos y `normalizeFloat` la canoniza al mismo resultado que la plain (ambos convergen con el destino). Es solo una **discrepancia documentación/código** (los comentarios citan `%.17g`, el código emite `%!.17g`). Nota, no bug.

### BAJA-2 — Rama `FloatType` no rechaza `Inf`/`NaN` (asimetría con `DecimalType`)

**Archivo:línea:** `compat/store.go:314-319` (rama `FloatType` → `normalizeFloat` incondicional) vs `store.go:433-435` (rama `DecimalType` vía `isCompactFloatText` sí excluye `Inf`/`NaN`).

`normalizeFloat` no filtra no-finitos. Verificado:

```
FLOAT in=<<inf>> out=<<+Inf>>   FLOAT in=<<NaN>> out=<<NaN>>
```

En `DecimalType` el gate `isCompactFloatText` explícitamente devuelve `false` para `Inf`/`NaN` y los preserva verbatim; `FloatType` no tiene ese gate. **Baja alcanzabilidad** en SQLite: un `REAL` de SQLite no produce `Inf`/`NaN` por aritmética normal (overflow de literal `1e999` queda fuera de rango y no se almacena como `REAL` finite). El caso vivo sería un origen `float8` de Postgres con `Infinity` (Postgres lo admite) → **NO VERIFICADO** sin PG. Nota de asimetría; si se quiere consistencia, aplicar el mismo guard `IsInf/IsNaN` a `FloatType` o documentar la diferencia.

### BAJA-3 — `ApplyChanges` estricto: `INSERT` de fila ya presente da error SQL opaco, no `ConflictError`

**Archivo:línea:** `compat/replicate.go:204` (`return insertRow(...)` en modo no-tolerant).

En modo estricto, un `Insert` cuya PK ya existe (p.ej., solapamiento snapshot→catch-up sin `Tolerant`) produce una violación de unicidad genérica del motor, no un `ConflictError` tipado. El modo `Tolerant` (`replicate.go:192-203`) sí lo maneja y emite `ConflictError` solo ante divergencia real. El modo estricto por diseño no contempla solapamiento, así que el error opaco es esperado; es solo una nota de UX de diagnóstico (el caller no recibe `*ConflictError` para distinguir). No es divergencia de datos.

---

## Áreas limpias (verificadas)

**`decodeCapturedRow` / `requireAllColumns` — split PK vs fila completa.** Correcto en todos los call sites: `capture.go:210` (`primary_key`, `requireAllColumns=false`), `capture.go:220` (`before_row`, `true`), `capture.go:226` (`after_row`, `true`). `NULL` vs ausente: un valor JSON `null` → `*string == nil` → `Value{Kind: NullValue}` (`capture.go:261-263`); una columna ausente del payload → si `requireAllColumns` → error explícito (`capture.go:256-258`), si no → `continue` (`capture.go:259`). El PK es intencionalmente un subconjunto (solo columnas clave) → `false` es correcto. **Limpio.**

**Reconciliación `REAL`-stored (propósito original del fix `6e9c2f6`).** Verificado que un `DECIMAL`/`REAL` almacenado como `float64` y un `FLOAT` round-tripean sin `ConflictError` espurio:

```
CAPTURE kind=insert after=map[amt:{decimal 1.2345678901234567e+14} id:{integer 1}]   (REAL-stored 123456789012345.67)
CAPTURE kind=insert after=map[amt:{decimal 0.1} ...]  {decimal 1.5}  {decimal 100}
CAPTURE-FLOAT after=map[f:{float 1.2345678901234567}]  {float 1}  {float 100}  {float 0.1}
```

El destino (`float64` leído) y la captura convergen en la misma forma canónica. **Limpio para su propósito declarado** (el bug ALTA-1 es un *sobre-alcance* del mismo mecanismo, no una falla del caso `REAL`).

**`ApplyChanges` / `ApplyChangesTolerant` — idempotencia y anti-echo.** Dedup por `(source_engine, source_version, sequence)` (`replicate.go:165-184`) → reaplicar el mismo stream es seguro. Supresión anti-echo: SQLite usa `__compat_capture_state.suppress` (válido por `SetMaxOpenConns(1)`, conexión única); el `UPDATE` a `0` antes del commit restaura el estado y el `Rollback` del `defer` no deja `suppress=1` fugado. `Tolerant` trata “ya aplicado” (insert/update matching `after`, delete gone) y registra la secuencia; cualquier otra divergencia sigue siendo `ConflictError` estricto. Lógica revisada y consistente. **Limpio** (camino Postgres GUC: **NO VERIFICADO** vivo, pero el diseño MVCC-local documentado en `replicate.go:135-163` es correcto).

**`rowsEqual` / `rowPredicate` / `loadRow`.** `rowsEqual` compara JSON canonizado de ambos `Row` (`replicate.go:344-351`) — simétrico y determinístico. `rowPredicate` itera `table.Columns`, salta las no presentes en el `Row` PK, usa `IS NULL` para PKs nulas (`replicate.go:330-331`) — correcto. **Limpio.**

**`runtime.go` (`CallRoutine`, `SearchText`, `tokenize`).** `SearchText` salta `nil` para no emitir token espurio “nil” (`runtime.go:255-257`); `tokenize` es Unicode letter/number. `CallRoutine` valida tabla, parámetros y requiring `WHERE` para UPDATE/DELETE. **Limpio.**

**`journal.go` / `verify.go`.** `OrderedChanges` valida y rechaza secuencias duplicadas por fuente; `Validate` cubre los invariantes de `Insert`/`Update`/`Delete`. `VerifySnapshots`/`SnapshotDigest` ordenan filas y hashean. **Limpio.** (Nótese: la *capacidad* de verify para detectar la divergencia de ALTA-1 es nula por diseño — ambos lados canonizan igual; eso es parte del hallazgo ALTA-1, no un bug de `verify.go` en sí.)

### NO VERIFICADO (sin PostgreSQL vivo)

- **`inspectPostgres` refactor (`b62128d`)**: la orquestación (`inspect.go:369-401`) preserva lógicamente el orden (`order`), la re-copia de tablas con constraints (`inspect.go:381-383`), y la adición de `Unresolved` por fase (columnas, constraints, índices, vistas, triggers, rutinas). El `map tables` compartido entre `inspectPostgresColumns` y `inspectPostgresConstraints` se re-copia correctamente al slice final. **Revisión de lógica limpia; ejecución contra PG vivo NO VERIFICADA.**
- **Driver pgx v5.10.0**: tipo retornado para `NUMERIC` (string vs `float64`) — determina si la rama `float64Value` de `canonicalValue` se dispara en lectura PG. **NO VERIFICADO** sin PG.
- **Changelogs de deps** (`pgx` v5.10.0, `modernc/sqlite` v1.54.0): sin red en este entorno; no se revisaron cambios de comportamiento del driver. El comportamiento relevante (`printf('%!.17g')`, afinidad de almacenamiento) **sí está verificado ejecutando** sobre modernc v1.54.0.

---

## Conclusión

El fix de precisión float/decimal (`6e9c2f6`) logra su objetivo (reconciliar `DECIMAL` almacenados como `REAL` sin `ConflictError` espurio) pero **sobre-alcanza**: el heuristic `isCompactFloatText` reescribe `DECIMAL` de precisión arbitraria almacenados como `TEXT` (el diseño canónico en SQLite), corrompiendo silenciosamente valores como `0.10000000000000001` → `0.1` y degradando escala `1.50` → `1.5`, de modo que `VerifySnapshots` no puede detectar la divergencia. **ALTA-1.** El resto del scope de runtime/replicación/captura está limpio o verificado para su propósito; el camino Postgres vivo queda pendiente de verificación.

**Conteo final: ALTA 1 / MEDIA 0 / BAJA 3.**