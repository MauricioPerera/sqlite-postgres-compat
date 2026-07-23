# AUDIT7-B — Interacciones entre familias y deuda de concurrencia

Scope: read-only. Repo Go `sqlite-postgres-compat` (compatibilidad SQLite→Postgres).
Rama `main`, árbol limpio. Esta es la séptima ronda (AUDIT7-B); las 6 rondas
previas cerraron todos sus hallazgos. El mandato de esta ronda: **INTERACCIONES**
entre TODAS las familias a la vez y la **DEUDA DE CONCURRENCIA** más antigua del
proyecto (AUDIT2-B, supresión de ecos GUC `compat.suppress` bajo conexiones
concurrentes), ambas contra **PostgreSQL real**.

Artefactos ajenos presentes en el árbol (`compat/zz_audit7a_test.go` de
AUDIT7-A y `.audit7c_*` de AUDIT7-C) no fueron tocados. La única creación
permanente de esta ronda es este archivo.

---

## Viabilidad verificada ANTES de construir (regla global 1)

- **PostgreSQL real**: `postgres://postgres:***@31.220.22.176:5434/postgres?sslmode=disable` → **PostgreSQL 17.10** (Alpine). Ping OK, version() OK. Contraseña SIEMPRE enmascarada como `***` en este reporte.
- **pgvector**: `pg_available_extensions` devuelve **0**; `CREATE EXTENSION vector` falla incluso como superuser (`SQLSTATE 0A000` — los archivos del extension no están instalados en el servidor). Consecuencia tajante: la familia **`vector` NO se puede probar contra este PG real**, porque el DDL del proyecto mapea `VectorType`→`vector(N)` nativo en Postgres (`compat/ddl.go`) y `ApplySchema` haría `CREATE TABLE ... vector(N)`, que requiere la extensión. El portador TEXT de SQLite sí cruza, pero el path nativo PG es inalcanzable aquí. Reportado como limitación (no defecto de código). Las demás 10 familias (integer/text/float/decimal/date/timestamp/json/uuid/boolean/binary) sí se probaron.
- **Tipos de escaneo pgx → `interface{}`** (probe directo, base efímera `audit7b_numtest`, dropeada):
  - `NUMERIC` → **`string`** preservando precisión arbitraria (`"0.123456789012345678"`). Esto es decisivo para el borde de 18 dígitos: el decimal de 18 dígitos round-tripea byte-for-byte.
  - `DOUBLE PRECISION` → `float64` · `BIGINT` → `int64` · `BOOLEAN` → `bool` · `BYTEA` → `[]byte` · `TEXT` → `string`.
- Toda la evidencia se ejecutó con un test temporal `e2e/zz_audit7b_test.go` (build tag `e2e`, paquete `e2e_test`, reusando el harness del repo: `openPostgres`, `postgresTestDSN`, el `TestMain` que crea/dropea `compat_e2e_<ns>`). **El archivo temporal se borró** tras la corrida; `git status` final solo muestra el reporte y los artefactos ajenos. `go build ./...` y `go vet ./compat/... ./cmd/...` verdes antes y después.

---

## PARTE 1 — INTERACCIONES (matriz probada)

Una sola tabla `audit7b_allfam` con **las 10 familias verificables a la vez** (vector
excluido por pgvector ausente), más dos tablas auxiliares (PK compuesta y
DECIMAL de storage REAL). Pipeline real: **snapshot → captura (trigger nativo) →
`ReadCapturedChanges` → `ApplyChanges` (anti-eco) → `VerifySnapshots`/`RequireEquivalent** contra PG real.

### Matriz de interacciones

| # | Escenario (todas las familias a la vez) | Resultado | Evidencia |
|---|---|---|---|
| 1 | INSERT inicial de 3 filas (completa, NULL en cada familia, bordes) + 1 fila PK compuesta + 1 fila DECIMAL REAL-storage, capturadas y aplicadas en **una sola tanda** | ✅ equivalentes, sin eco | 5 cambios capturados, `ApplyChanges` OK, journal dest=0, `RequireEquivalent` OK |
| 2 | **NULL replicado en cada familia** (fila 2: todas las columnas no-PK NULL) | ✅ | round-trip NULL→`NullValue`→`nil` en PG; equivalentes |
| 3 | **Fila con TODOS los bordes** (float 17 dígitos `1.2345678901234567` + decimal 18 dígitos `123456789012345678` + date extrema `0001-01-01` + ts extrema `9999-12-31T23:59:59Z`) en una sola fila, capturada y aplicada en la tanda | ✅ | spot-check PG: `c_dec=123456789012345678`, `c_float=1.2345678901234567`; equivalentes |
| 4 | **UPDATE cross-family**: cambia text/float/decimal/date/ts/json/uuid/bool/binary en una sola fila simultáneamente | ✅ | before/after con todas las familias; `loadRow` dest coincide con `Before`; equivalentes |
| 5 | **UPDATE DECIMAL text-storage → real-storage y viceversa** en la MISMA fila que un `date` (tabla `audit7b_decstrg`, afinidad NUMERIC manual) | ✅ | `typeof(d)`: `real`→`integer`→`real`; marker `\x01real` reconciliado por `normalizeFloat`; date coexistiendo; equivalentes |
| 6 | **DELETE con before completo multi-familia** (fila 1 tenía cada familia poblada; el `before_row` del cambio carga las 10 familias y debe coincidir con la fila viva en dest) | ✅ | `ApplyChanges(Delete)` OK; fila eliminada ambos lados; equivalentes |
| 7 | **PK compuesta con columna de familia nueva** (`audit7b_compk`: PK = `c_id` integer + `c_uuid` uuid) — INSERT y UPDATE por la PK compuesta | ✅ | UPDATE localizado por ambas columnas PK; equivalentes |
| 8 | **Anti-eco cross-familia vía API pública `ApplyChanges`** (dest con capture instalado; el GUC `compat.suppress` arma y desarma por tx) | ✅ | journal dest=0 tras cada `ApplyChanges`; control positivo (escritura manual posterior) → journal dest=1 |

### Cruces de canonicalización entre familias — veredicto

**No se halló ningún cruce de canonicalización que rompa la equivalencia.** Cada
familia se canonicaliza de forma aislada en `canonicalValue`
(`compat/store.go:289-381`) y en el trigger de captura
(`compat/capture.go:148-204`), y los pares before/after de un mismo cambio —que
contienen familias heterogéneas— se comparan fila-completa vía `rowsEqual` sin que
una familia interfiera con otra. Casos confirmados:

- **JSON** (reordenamiento de claves): fuente `{"b":2,"a":1}` y `{"z":1,"a":[1,2,3]}` → `canonicalValue` re-serializa con claves ordenadas; captura (CAST) y snapshot convergen en el mismo byte form tanto en SQLite como en PG TEXT. Sin divergencia.
- **DECIMAL REAL-storage vs date en la misma fila**: el `CASE typeof(d) WHEN 'real' THEN '<marker>'||printf('%!.17g',d) ELSE CAST(d AS TEXT) END` discrimina por **storage class**, no por forma del texto, así que un `date` TEXT adyacente nunca colisiona con el marker `\x01real` (que empieza con SOH, byte que ningún decimal ni fecha legítimos pueden tener).
- **FLOAT 17 dígitos**: `printf('%!.17g', x)` en SQLite y `CAST(float8 AS TEXT)` en PG ambos emiten la forma shortest que round-tripea; `normalizeFloat` los reconcilia. Confirmado en PG real (`1.2345678901234567`).
- **DECIMAL 18 dígitos (TEXT storage)**: preservado verbatim en SQLite TEXT y en PG NUMERIC (pgx devuelve `string` con precisión completa). Confirmado en PG real (`123456789012345678`).
- **BOOLEAN/INTEGER/BINARY/UUID/TIMESTAMP**: cada uno round-tripea (bool→`true`/`false` canonical; integer `int64`; binary hex↔base64↔BYTEA; uuid verbatim TEXT; timestamp RFC3339Nano).

### Evidencia ejecutada (Parte 1)

```
$ go test ./e2e/ -tags e2e -run 'TestAudit7B' -v
=== RUN   TestAudit7BAllFamiliesInteractions
    zz_audit7b_test.go:156: decstrg id=1 typeof(d)=real (as expected)
    zz_audit7b_test.go:163: initial captured changes: 5
    zz_audit7b_test.go:173: dest journal after initial replication: 0 (anti-echo OK across all families)
    zz_audit7b_test.go:175: PASS: initial batch — all families equivalent, no echo, NULL replicated per family, border row round-trips
    zz_audit7b_test.go:196: decstrg id=1 typeof(d)=integer (as expected)
    zz_audit7b_test.go:206: incremental captured changes: 3 (cross-family update + dec real->int + composite-PK update)
    zz_audit7b_test.go:217: PASS: incremental cross-family UPDATE + DECIMAL real->integer storage transition + composite-PK update all converge, no echo
    zz_audit7b_test.go:229: decstrg id=1 typeof(d)=real (as expected)
    zz_audit7b_test.go:239: second incremental: 2 (dec int->real transition + full multi-family DELETE)
    zz_audit7b_test.go:250: PASS: DECIMAL integer->real transition + full multi-family DELETE (before carries all families) converge, no echo
    zz_audit7b_test.go:260: PG border row c_dec=123456789012345678 (18 digits preserved)
    zz_audit7b_test.go:268: PG border row c_float=1.2345678901234567 (17 sig digits preserved)
    zz_audit7b_test.go:278: PASS: positive control — manual dest write journaled (anti-echo was suppression, not dead triggers)
--- PASS: TestAudit7BAllFamiliesInteractions (9.21s)
PASS
ok      example.com/sqlite-postgres-compat/e2e  12.973s
```

### Nota sobre el path DECIMAL REAL-storage (escenario 5)

El DDL del proyecto mapea `DecimalType`→`TEXT` en SQLite y →`NUMERIC` en
Postgres (`compat/ddl.go`). En SQLite, **TEXT storage** (siempre
`typeof='text'`): la rama del marker `\x01real` del trigger **no se alcanza nunca**
por una tabla creada con `ApplySchema`. El marker existe precisamente para tablas **legacy/externas con
afinidad NUMERIC/REAL** que guardan valores fraccionales como `REAL`. Para
ejercer la transición text↔real-storage pedida por el mandato se creó a mano una
tabla SQLite `audit7b_decstrg` con columna `d NUMERIC` (afinidad NUMERIC) y se
declaró en el schema como `DecimalType`; así `1.50`→`typeof='real'` (marker) y
`2`→`typeof='integer'` (CAST verbatim). El comportamiento es correcto, pero es
importante documentar que **el proyecto nunca produce storage REAL para DECIMAL
por su propio DDL**: este camino es exclusivamente de interoperabilidad con
tablas preexistentes. (Ver hallazgo BAJA-2.)

---

## PARTE 2 — DEUDA DE CONCURRENCIA (AUDIT2-B, el NO VERIFICADO más viejo)

La deuda: AUDIT2-B la dejó como **BAJA — NO VERIFICADA** ("transaction-local GUC
suppression `compat.suppress` correctness under concurrent Postgres connections
… needs a live multi-connection Postgres test; the repo's own comments point to
`e2e/suppress_test.go`").

### ¿Cubre `e2e/suppress_test.go` exactamente lo pedido? — SÍ, con evidencia

El test **`TestSuppressIsolationDoesNotLeakAcrossConnections`**
(`e2e/suppress_test.go:55-130`) implementa textualmente el escenario del
mandato:

- **2+ conexiones pgx simultáneas**: `store.DB` (conexión A, pool del Store) y un pool separado `other` (conexión B, `sql.Open("pgx", postgresTestDSN)`) — garantiza dos backends PG distintos.
- **Una aplicando cambios con supresión activa**: A abre una tx y arma la supresión con el SQL exacto del path productivo (`SELECT set_config('compat.suppress','1',true)` — el mismo que `setCaptureSuppressed` corre dentro de `ApplyChanges`; el comentario del test explica que `ApplyChanges` commitea internamente, así que el único modo de mantener la tx armada es replicar ese mecanismo directo). A escribe una fila estando armada.
- **Otra escribiendo/leyendo la misma tabla**: B inserta en la misma tabla y commitea **mientras A sigue abierta**.
- **Verifica transaction-local de verdad**: la escritura de B **SÍ se journaliza** (1 entrada; la supresión de A NO se filtró), y la escritura de A **NO** se journaliza (la supresión está activa en su propia conexión). Post-commit de A, el journal sigue en 1 (la escritura de A sigue sin eco). Lectura final confirma ambas filas persisten.

Complementariamente, **`TestSuppressAntiEchoOnReplicatedWrites`** cubre la ruta
**pública `ApplyChanges`** (no el mecanismo a mano): replica vía `ApplyChanges`
contra un dest con capture y afirma 0 ecos, con control positivo (escritura
manual posterior → journal=1). **`TestSuppressReapplicationIsIdempotent`**
verifica idempotencia. La combinación cubre: aislamiento bajo concurrencia real
(el núcleo de la deuda) + path público anti-eco + idempotencia.

Mi test de la Parte 1 **refuerza** esto vía la API pública: tras cada
`ApplyChanges` con capture instalado en dest, el journal de dest permanece en 0
(supresión anti-eco) y el control positivo (escritura manual) produce journal=1
—atravesando las 10 familias. No hay hueco que llenar: la deuda está cubierta.

### Evidencia ejecutada (Parte 2, aislado, contra PG real)

```
$ go test ./e2e/ -tags e2e -run 'TestSuppress' -v
=== RUN   TestSuppressIsolationDoesNotLeakAcrossConnections
--- PASS: TestSuppressIsolationDoesNotLeakAcrossConnections (3.43s)
=== RUN   TestSuppressAntiEchoOnReplicatedWrites
--- PASS: TestSuppressAntiEchoOnReplicatedWrites (3.37s)
=== RUN   TestSuppressReapplicationIsIdempotent
--- PASS: TestSuppressReapplicationIsIdempotent (3.27s)
PASS
ok      example.com/sqlite-postgres-compat/e2e    13.408s
```

### Veredicto sobre la deuda

**CERRADA.** La supresión `compat.suppress` es **transaction-local de verdad**
bajo MVCC en PG real: `set_config('compat.suppress','1',true)` (tercer arg
`true` = local a la tx) es invisible a otras transacciones y se resetea sola en
COMMIT/ROLLBACK. Una conexión ajena concurrente **NO queda suprimida** y sus
cambios **SÍ se capturan**. Verificado con un multi-conexión real (2 backends
pgx), no por lectura. AUDIT2-B BAJA → resuelta.

---

## Hallazgos por severidad

### ALTA — 0
Ninguno.

### MEDIA — 0
Ninguno.

### BAJA — 2

- **BAJA-1 — Familia `vector` no verificable contra este PG real (limitación de entorno, no defecto de código).** El servidor no tiene instalados los archivos del extension `pgvector` (`pg_available_extensions=0`; `CREATE EXTENSION vector` → `SQLSTATE 0A000` incluso como superuser). El DDL del proyecto mapea `VectorType`→`vector(N)` nativo en Postgres (`compat/ddl.go`), por lo que `ApplySchema` de una tabla con columna `vector` fallaría aquí. El portador TEXT canónico de SQLite (validado SQLite-side y vs libSQL/sqld en `VECTOR-COMPAT-REPORT.md`) no se ejerció contra PG nativo en esta ronda. **Acción sugerida**: instalar `postgresql*-pgvector` en el host de pruebas y re-ejecutar; no es un cambio de código.

- **BAJA-2 — El path DECIMAL REAL-storage (marker `\x01real`) es inalcanzable por el DDL propio del proyecto (by design; documentar).** `DecimalType`→`TEXT` en SQLite (`compat/ddl.go`), storage siempre `text`, así que la rama `typeof='real'` del trigger de captura solo se dispara en tablas legacy/externas con afinidad NUMERIC/REAL. Se verificó que esa rama funciona correctamente (transición real→integer→real, misma fila que un date, contra PG real) creando manualmente una tabla `d NUMERIC`. No hay nada que arreglar; se documenta porque el marker existe exclusivamente para interoperabilidad con tablas preexistentes y conviene que quede explícito para futuros auditores.

## Áreas limpias (explícitas)

- **Cruces de canonicalización entre familias** (Parte 1, matriz de 8 escenarios): limpios. Ninguna familia interfiere con otra en before/after fila-completa; JSON, DECIMAL REAL-storage + date, FLOAT 17 dígitos, DECIMAL 18 dígitos, boolean/integer/binary/uuid/timestamp todos round-tripean contra PG real.
- **Supresión de ecos `compat.suppress` bajo concurrencia** (Parte 2): limpia y **verificada** contra PG real multi-conexión. Deuda AUDIT2-B cerrada.
- **Anti-eco vía API pública `ApplyChanges`** atravesando las 10 familias: limpio (journal dest=0 tras réplica; control positivo journal=1).
- **`go build ./...`** y **`go vet ./compat/... ./cmd/...`**: verdes antes y después. Sin archivos de producción modificados.

## Limpieza

- Test temporal `e2e/zz_audit7b_test.go`: **borrado**. Módulo temporal `.tmp-audit7b` / `.tmp-audit7b-chk`: **borrados**.
- Bases temporales: el harness `TestMain` crea y dropea `compat_e2e_<ns>` automáticamente; las bases efímeras de los probes (`audit7b_numtest`, `audit7b_vectest`) se dropearon inline. Verificación final: **`NO audit7b_*/compat_e2e_* databases remaining`**.

---

**Conteo por severidad:** ALTA: 0 · MEDIA: 0 · BAJA: 2