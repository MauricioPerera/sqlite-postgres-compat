# AUDIT7-C — Zonas nunca auditadas en 6 rondas

Fecha: 2026-07-22
Auditor: efímero, sin memoria previa.
Repo: `sqlite-postgres-compat` (Go), branch `main`, árbol limpio salvo ajenos.
PG real: `postgres://postgres:***@31.220.22.176:5434/postgres?sslmode=disable` (password enmascarado).
Alcance: `docs/ARCHITECTURE.md`, `docs/COMPATIBILITY.md`, `docs/OPERATIONS.md`; `examples/*.json`; `contracts/migration.contract.example.md`; `scripts/check.ps1`, `scripts/test-system.ps1`; `experiments/vector/`; calidad de `e2e/*.go`.

Modalidad: READ-ONLY. Única creación permanente: este reporte. Temporales/binarios propios borrados. Bases temporales en el VPS dropeadas (no quedaron `compat_e2e_*` ni `audit7c_*`; tablas manuales `entries`/`__compat_*` dropeadas del DB `postgres`).

> Nota de entorno: durante esta auditoría, auditores paralelos (AUDIT7-A/B) mutaron el árbol en vivo. Apareció y desapareció `e2e/zz_audit7b_test.go` (no rastreado, ajeno) y apareció `compat/zz_audit7a_test.go` (no rastreado, ajeno). La corrida e2e de la sección 5 incluyó `zz_audit7b_test.go` mientras existió; no se re-corrrió. Ningún archivo ajeno fue tocado.

---

## 1. Coherencia claim por claim: docs vs código actual

Verificación contra el código tras los cambios de la semana (marcador de journal, `date→TEXT`, CLI unificado, refactors).

### `docs/ARCHITECTURE.md`

| # | Claim | Verificación | ✅/❌ |
|---|---|---|---|
| A1 | `compat.Contract` identifica motor, versión y capacidades requeridas | `compat/spec.go:57` `Contract{Source,Destination,RequiredFeatures}`; `Target.Validate` valida engine+version | ✅ |
| A2 | `Audit` devuelve un `Finding` por capacidad; `RequireExact` rechaza no-exact | `compat/features.go:48` Audit; `:72` RequireExact | ✅ |
| A3 | `canonical_*` refieren al AST común; familias genéricas permanecen `unknown` | `features.go:61` assess: canonical_*→Exact; `:65` ForeignKeys/CheckRules/Indexes/Triggers/Views/StoredRoutines/FullText→Unknown | ✅ |
| A4 | `compat.Schema` modela tablas/columnas/tipos (familias escalares, `decimal(p,s)` con args, `vector(N)` con dim canónica), PK/UNIQUE/FK/CHECK, índices únicos/parciales/DESC, vistas/joins/filtros/agrupación/orden, triggers INSERT/UPDATE/DELETE, rutinas transaccionales parametrizadas | `compat/schema.go` (Type con `Arguments`; Index con `Unique`/`Where`/`Descending`; View/SelectQuery con Joins/Where/GroupBy/Having/OrderBy/Limit/Offset; Trigger/TriggerAction; Routine/RoutineAction) | ✅ |
| A5 | `CompileDDL` genera DDL físico; decimal SQLite=texto; JSON/UUID=texto; timestamp=texto RFC3339Nano | `compat/ddl.go:128` DecimalType→TEXT; `:131/170/174` JSON/UUID→TEXT; `:166` Timestamp→TEXT (comentario RFC3339) | ✅ |
| A6 | `ApplySchema` guarda el esquema en `__compat_schema`; `InspectSchema` reconstruye el AST | `compat/store.go:26` `schemaMetadataTable="__compat_schema"`; `writeSchemaMetadata`; `compat/inspect.go` | ✅ |
| A7 | Para bases externas sin metadatos, los inspectores consultan catálogos; objetos fuera de la gramática → `Inspection.Unresolved`, `Inspection.Exact=false` | `compat/inspect.go` (Unresolved/Exact) | ✅ |
| A8 | `ExportSnapshot`/`ImportSnapshot`/`VerifySnapshots`; importación aditiva (no borra/reemplaza) | `store.go:162` ImportSnapshot usa `CREATE TABLE IF NOT EXISTS`, sin DROP/DELETE; `compat/verify.go` | ✅ |
| A9 | `InstallChangeCapture` crea triggers y `__compat_change_journal`; `ReadCapturedChanges` ordena por secuencia | `compat/capture.go:13` `changeJournalTable`; `:105` trigger `__compat_capture_<t>_<m>`; ReadCapturedChanges ordenado | ✅ |
| A10 | `ApplyChanges`: stream un origen en tx; registra secuencias en `__compat_applied_changes`; compara `before`; `ConflictError` con `Expected`/`Actual` | `compat/replicate.go:12` `appliedChangesTable`; `:15` `ConflictError{Table,PrimaryKey,Expected,Actual}` | ✅ |
| A11 | Inhibe captura anti-eco transaccional; en Postgres GUC local `compat.suppress` (no fila compartida), no filtra a tx ajenas bajo MVCC | `capture.go:141` `current_setting('compat.suppress', true)`; `replicate.go:119-125` | ✅ |
| A12 | Todas las tablas capturadas necesitan PK canónica | `capture.go:93` "automatic capture requires a primary key on table %s" | ✅ |
| A13 | Runtime común y búsqueda Unicode se ejecutan desde Go | `compat/runtime.go` (SearchText, ILIKE branch `:159`) | ✅ |
| A14 | Tablas internas reservadas: `__compat_schema`, `__compat_applied_changes`, `__compat_capture_state`, `__compat_change_journal`, triggers/funciones `__compat_capture_*` | constantes en `store.go:26`, `replicate.go:12-13`, `capture.go:13`; prefijo `__compat_capture_` en `:105` | ✅ |

ARCHITECTURE.md: **área limpia** (14/14 ✅).

### `docs/COMPATIBILITY.md`

| # | Claim | Verificación | ✅/❌ |
|---|---|---|---|
| C1 | Matriz de compatibilidad (familias × AST canónico / catálogo externo / fuera de cobertura) | Coincide con `inspect.go`/`sqlparse.go`/`ddl.go` (traducción de afinidades y tipos, gramática acotada, rechazo explícito) | ✅ |
| C2 | Parser canónico `compat/sqlparse.go`; gramática acotada; todo lo demás se rechaza con error | `sqlparse.go:141` "unsupported catalog expression" | ✅ |
| C3 | Precedencia: OR · AND · NOT · IS NULL/IS NOT NULL · comparaciones (`<=,>=,<>,!=,=,<,>,LIKE`) · `||` · `+`/`-` · `*`/`/` | `sqlparse.go:22-29` niveles idénticos | ✅ |
| C4 | `NOT` se resuelve entre `AND` y las comparaciones; `NOT a = b` → `not(eq(a,b))` | `sqlparse.go:36` (index==2) + comentario `:32-35` | ✅ |
| C5 | `a NOT LIKE b` → `not(like(a,b))` | `sqlparse.go:48-53,66-68` (`stripTrailingNot` + `negateLike`) | ✅ |
| C6 | `LIKE`→`ILIKE` en Postgres (compromiso conocido Unicode) | `ddl.go:352` | ✅ |
| C7 | Allowlist funciones: `count,sum,avg,min,max` (`*` o expr); `lower,upper` (1 expr); `length,abs,trim` (1 expr); `coalesce` (≥1); `replace` (==3); otra → `unsupported catalog function` | `sqlparse.go:100-134`; mensaje exacto `:134` | ✅* |
| C8 | Literales: strings `''`, `TRUE`/`FALSE`, `NULL`, `CURRENT_TIMESTAMP`, enteros/decimales (`123`,`1.5`,`1e3`), hex `0x10`/`0XABCDEF`→decimal; identificadores con `.` y `"..."` | `sqlparse.go:75-97` (`catalogHexLiteral`), `parseCatalogIdentifier` | ✅ |
| C9 | Capacidades canónicas exactas actuales: 8 `canonical_*` | `features.go:61` lista las 8 CanonicalForeignKeys/Checks/Indexes/Views/Triggers/Routines/FullText/Vectors → Exact | ✅ |
| C10 | Familias genéricas `unknown`: `foreign_keys, check_constraints, indexes, views, triggers, stored_routines, full_text` (7) | `features.go:65` exactamente esas 7 → Unknown | ✅ |
| C11 | Tabla de representación de valores: Boolean INTEGER/BOOLEAN; Integer INTEGER/BIGINT; Decimal TEXT/NUMERIC(p,s) o NUMERIC; Float REAL/DOUBLE; Text TEXT/TEXT; Binary BLOB/BYTEA; **Date TEXT/DATE**; Timestamp TEXT/TEXT; JSON TEXT/TEXT; UUID TEXT/TEXT; Vector TEXT/vector(N) | `ddl.go:122-184` coincide **salvo Date**: code Date Postgres = `TEXT` (`ddl.go:159`), doc dice `DATE` | ❌ |
| C12 | Límite del 100 %: sigue fallando mientras exista familia genérica requerida no-exact; referencia a `VALIDATION_REPORT.md` | `features.go` RequireExact; `docs/reports/VALIDATION_REPORT.md` existe | ✅ |

\* C7: ver hallazgo H5 — el parser agrupa `lower`/`upper` con las agregadas, permitiendo `lower(*)`/`upper(*)`, que el doc describe como "una expresión".

COMPATIBILITY.md: **1 claim ❌** (C11, Date), resto ✅.

### `docs/OPERATIONS.md`

| # | Claim | Verificación | ✅/❌ |
|---|---|---|---|
| O1 | Adaptador SQLite: una sola conexión, `PRAGMA foreign_keys=ON` aplicado a todas las ops | `store.go:49` `SetMaxOpenConns(1)`; `:53-58` `sqliteDSN` añade `_pragma=foreign_keys(1)` | ✅ |
| O2 | SQLite: múltiples lectores, un escritor; condiciones de suficiencia | Documentación operativa; consistente con el modelo de conexión única | ✅ |
| O3 | DSN SQLite/PostgreSQL; E2E es excepción (crea/elimina DB temporal) | `e2e/system_test.go:24` `TestMain` crea `compat_e2e_<nano>` y la dropea | ✅ |
| O4 | Despliegue inicial 8 pasos; "no ejecutes `compat copy` repetidamente: la importación no reemplaza ni elimina" | `store.go:162` ImportSnapshot aditiva (sin DROP) | ✅ |
| O5 | Cursores: actualizar sólo tras `ApplyChanges` OK; destino registra secuencias aplicadas → reintentos seguros | `replicate.go:12` `appliedChangesTable` (dedup por secuencia) | ✅ |
| O6 | Conflictos: `ApplyChanges` aborta tx, `ConflictError{Table,PrimaryKey,Expected,Actual}`; supresión anti-eco GUC `compat.suppress`; `Version{0,0,0}` rechazado | `replicate.go:15` ConflictError; `spec.go:29` `Version{0,0,0}` inválido; `capture.go:141` GUC | ✅ |
| O7 | Journal y retención: `__compat_change_journal` crece; sin API de poda | Sin función de poda en `capture.go`/`replicate.go` | ✅ |
| O8 | Cambios de esquema: captura registra filas no DDL; 6 pasos coordinados | `capture.go` captura filas; consistente | ✅ |
| O9 | Limitaciones operacionales (sin scheduler/daemon, sin poda, sin resolución auto de conflictos, sin DDL replication, SQLite sin TransactionID) | Sin daemon; sin poda; ConflictError no auto-resuelve; consistente | ✅ |

OPERATIONS.md: **área limpia** (9/9 ✅).

---

## 2. Ejemplos y contrato: ¿ejecutables tal cual hoy?

Cada ejemplo se corrió con el binario real (`go run ./cmd/compat` o binario construido). DSN destino ajustado al VPS; si requirió más que el DSN, se registra como hallazgo.

### `examples/contract.example.json` → `compat audit`
```
$ go run ./cmd/compat audit ./examples/contract.example.json
[{"feature":"tables","status":"exact"},{"feature":"canonical_foreign_keys","status":"exact"},{"feature":"transactions","status":"exact"}]
exit=0
```
Salida y exit code coinciden con lo esperado por `contracts/migration.contract.example.md` (Verdict 1). **Ejecutable tal cual, sin ajustes. ✅**

### `examples/schema.example.json`
Es un `compat.Schema` suelto (no es entrada directa de un subcomando). Se validó como `schema_ref` en una config `compat copy` (resolución relativa al config, path absoluto al ejemplo del repo):
```
$ compat.exe copy refcfg.json   # source_dsn=source.db relativo; schema_ref=<repo>/examples/schema.example.json
{"source_digest":"2f016b...a0f","destination_digest":"2f016b...a0f0f","equivalent":true}
exit=0
```
**Válido y utilizable vía `schema_ref`. ✅**

### `examples/migration.example.json` → `compat copy`
Ajustes: DSN destino → VPS; **requirió además crear `source.db`** con el esquema y filas (no se incluye en el repo). Corrido desde un dir con `source.db` relativo (como en el ejemplo):
```
$ compat.exe copy migration.json
{"source_digest":"2f016b...a0f","destination_digest":"2f016b...a0f","equivalent":true}
exit=0
```
**Funciona end-to-end con PG real. ✅** (Ajuste > DSN: necesita un `source.db` preexistente — ver H3.)

### `examples/cutover.example.json` → `compat cutover`
Mismos ajustes (DSN + `source.db`). Dry-run + real, contra VPS:
```
$ compat.exe cutover --dry-run cutover.json
compat cutover: audit: exact coverage for 3 required features
{"status":"plan","audit":[...],"source_tables":[{"name":"entries","rows":2}],"destination_has_tables":false,"phases":["install_capture","snapshot","catch_up","verify"]}
exit=0

$ compat.exe cutover cutover.json
compat cutover: audit: exact coverage for 3 required features
compat cutover: capture: change capture installed on source
compat cutover: snapshot: imported into destination
compat cutover: catch-up: drained after 0 changes
{"status":"ready","source_digest":"2f016b...a0f","destination_digest":"2f016b...a0f","changes_applied":0}
exit=0
```
`status=ready`, digests idénticos. Las 4 líneas de progreso en stderr coinciden exactamente con lo especificado en `contracts/migration.contract.example.md` (Verdict 2). **Funciona end-to-end con PG real. ✅** (Ajuste > DSN: `source.db` — H3.)

### `contracts/migration.contract.example.md`
Documento migratorio humano+machine. Sus Verdicts:
- **#1 `compat audit ./examples/contract.example.json`** → ejecutado tal cual, exit 0, output esperado. **✅**
- **#2 `compat cutover ./cutover.json`** → no hay `cutover.json` de ejemplo; el doc dice "Prepare a `cutover.json`". Es template por diseño. Al prepararlo (con `schema_ref` al ejemplo o schema inline), corre y da `status=ready` (ver arriba). **✅ por diseño** (requiere preparar config — H4 cosmético).
- **#3 Verificación** → es una llamada a la API Go (`compat.VerifySnapshots`), no un comando CLI; el cutover ya la codifica internamente. No ejecutable como CLI aislado (no es claim de CLI).
- **#4** → corte manual del DSN, no es CLI. Por diseño.

---

## 3. Scripts

### `scripts/check.ps1`
```
$ pwsh -NoProfile -File scripts/check.ps1
gofmt: OK
go vet: OK
?  	example.com/sqlite-postgres-compat/cmd/compat	[no test files]
ok  	example.com/sqlite-postgres-compat/cmd/internal/cliout	2.363s
ok  	example.com/sqlite-postgres-compat/compat	2.233s
go test: OK
exit=0
```
**Funciona, verde. ✅** Gate de formato/vet/unit válido.

### `scripts/test-system.ps1`
Lee el script: param `$PostgresDsn` con default `postgres://$env:USERNAME@127.0.0.1:5432/postgres?sslmode=disable` (localhost, no roto; acepta override). Setea `COMPAT_POSTGRES_DSN`, corre `go test ./... -timeout 60s`, `go vet ./...`, `go test -tags=e2e ./e2e -v -count=1 -timeout 60s`. Corrido con el DSN del VPS:
- Unit (`go test ./...`): **PASS**.
- `go vet ./...`: **PASS**.
- E2E: **corre contra PG real** (crea/dropea DB temporal `compat_e2e_*`), pero **falla** en `TestSystemClaimsExactCoverageForRequiredFeatureFamilies` y el script **termina exit 1**.

```
--- FAIL: TestSystemClaimsExactCoverageForRequiredFeatureFamilies (0.00s)
    --- FAIL: .../foreign_keys
    --- FAIL: .../check_constraints
    --- FAIL: .../indexes
    --- FAIL: .../triggers
    --- FAIL: .../views
    --- FAIL: .../stored_routines
    --- FAIL: .../full_text
FAIL  example.com/sqlite-postgres-compat/e2e  52.697s
exit=1
```
**Hallazgo H2**: `test-system.ps1` no es un gate verde utilizable: siempre termina exit 1 por un test e2e que afirma exactitud para familias genéricas que son `unknown` por diseño (`features.go:65`). Es el "baseline N+1 intencional" (memoria del proyecto), pero como script de calidad es no-funcional: cualquier CI que lo ejecute siempre queda rojo. El script no hardcodea un DSN roto (acepta `-PostgresDsn`), así que no es hallazgo el default localhost.

---

## 4. `experiments/vector/`

Módulo Go independiente (`module example.com/vector-exp`, `replace example.com/sqlite-postgres-compat => ../..`).

| Verificación | Resultado |
|---|---|
| `go build ./...` (desde `experiments/vector/`) | **exit 0** ✅ |
| `go vet ./...` | **exit 0** ✅ (incluye archivos `_test.go`) |
| `go.mod` alineado con raíz | `github.com/jackc/pgx/v5 v5.10.0` coincide con raíz ✅; agrega `tursodatabase/libsql-client-go` (necesario para `probe.go`, que abre `libsql`); `modernc.org/sqlite v1.54.0` y `go 1.26` coinciden ✅ |
| Propósito cierto | `README.md:69` "validación del tipo vector contra libSQL/sqld y pgvector reales". `probe.go` hace exactamente eso (`vector_extract` sobre libsql). **✅** |
| Tests (`postvectortype_test.go`, `vector_test.go`) | Necesitan libSQL/sqld externo (`VECTOR_LIBSQL_URL`/`VECTOR_PG_DSN`). **NO VERIFICADOS** (permitido por el enunciado). Compilan (vet OK). |

Nota (H6, BAJA): `docs/reports/VECTOR-COMPAT-REPORT.md` abre afirmando "The `compat` layer has **no `vector` type family**". Es **stale**: `compat` ya tiene `VectorType` (`schema.go:48`), `vector(N)` con dimensión canónica (`ddl.go:178-184`) y `canonical_vectors` (`features.go:27`). El propósito del experimento sigue siendo cierto; el reporte adjunto no.

**`experiments/vector/`: área limpia** salvo la nota stale del reporte.

---

## 5. Calidad de la suite e2e

Corrida **única completa** contra el VPS (`go test -tags=e2e ./e2e -v -count=1 -timeout 120s`):

**Tally: 38 PASS / 1 FAIL / 0 SKIP** (top-level). Subtests: 14 PASS / 7 FAIL. `exit=1`. Duración ~53.8s.

La única FAIL top-level es `TestSystemClaimsExactCoverageForRequiredFeatureFamilies` (`e2e/system_test.go:1012`): sus 7 subtests fallan porque las familias genéricas son `unknown` por diseño — el test afirma `status==exact` para todas. Es el baseline intencional (H2).

Revisión de calidad sobre los 3 archivos estables (`cutover_test.go`, `suppress_test.go`, `system_test.go`; `zz_audit7b_test.go` es ajeno y efímero):

- **Tests sin aserciones reales**: ninguno. Los 36 `Test*` (estables) contienen aserciones reales (`t.Fatal`/`t.Errorf` o helpers de aserción).
- **Helpers muertos**: ninguno. Todos los helpers (`cutoverSchema`, `runCLI`, `firstErrorJSONLine`, `builtCLI`, `runBuiltCLI`, `writeCutoverConfig`, `cutoverContract`, `suppressSchema`, `journalCountForTable`, `suppressInsertChange`, `databaseDSN`, `hasConstraint`, `hasColumnDefault`, `hasForeignActions`, `openSQLite`, `openPostgres`, `assertEquivalent`, `assertStoreSnapshotsEquivalent`, `queryNameTotals`) se usan ≥2 veces (definición + llamada).
- **Asserts debilitados por los renames/cambios de la semana (unificación CLI)**: limpio. No quedan referencias a los prefijos viejos `compat-audit:`/`compat-copy:`/`compat-cutover:` en `e2e/`. Los asserts de CLI usan los prefijos nuevos (`compat audit:`/`compat copy:`/`compat cutover:`) y códigos de error tipados (`ERR_*`). `TestCLIUsageCountMessageConsistent`, `TestCLIRejectsUnknownFlagAsUsage`, `TestCLIDispatchUsage`, `TestCopyCLINotExactStderrOnce`, `TestCopyCLIDivergedStderrOnce`, `TestCutoverCLINotExactStderrOnce`, `TestCutoverRejectsUnknownConfigKey`, etc. cubren los cambios de la semana.
- **Duplicación grosera**: no detectada; helpers compartidos centralizan aperturas, aserciones de equivalencia y corridas CLI.
- **TestMain** (`system_test.go:24`): harness sólido — requiere `COMPAT_POSTGRES_DSN` (exit 2 si falta), crea DB `compat_e2e_<nanos>`, corre, termina backends y dropea la DB (setea code=1 si el drop falla). Limpió tras sí (no quedaron `compat_e2e_*`).

**e2e: área limpia** en calidad de tests/helpers/asserts. El único FAIL es el baseline intencional (H2), no un defecto de la suite.

---

## Hallazgos por severidad

### MEDIA

- **H1 — `docs/COMPATIBILITY.md` tabla de valores: Date → Postgres `DATE` es falso.** El doc (tabla de representación de valores) afirma `Date | TEXT | DATE | Conversión mediante el adaptador`. El código mapea Date → `TEXT` en Postgres (`compat/ddl.go:159`, commit `150acac` "map date family to lossless TEXT on Postgres"), con comentario explícito: pgx devolvería `time.Time` y se plegaría a `TimestampValue`, divergiendo del TEXT de SQLite; por eso se eligió TEXT. La decisión de código es correcta; el doc no se actualizó. Un usuario que lea el doc esperaría columnas `DATE` nativas en Postgres y semántica de fecha, cuando en realidad recibe `TEXT`. **Acción**: cambiar la celda Postgres de `DATE` a `TEXT` y el motivo a "preservar el valor canónico exacto (pgx pliega DATE a time.Time)".

- **H2 — `scripts/test-system.ps1` siempre termina exit 1 (gate no-funcional).** `TestSystemClaimsExactCoverageForRequiredFeatureFamilies` (`e2e/system_test.go:1012`) falla permanentemente porque exige `exact` para las 7 familias genéricas que son `unknown` por diseño (`features.go:65`). Es el "baseline N+1 intencional", pero como script de calidad es rojo fijo: cualquier CI que lo invoque siempre falla. **Acción**: o bien marcar el test como `t.Skip` con un TODO que referencie el baseline, o hacer que el script espere ese único FAIL conocido (p.ej. filtrar y comparar el conteo), de modo que un verde/negro sea significativo.

### BAJA

- **H3 — `examples/migration.example.json` y `examples/cutover.example.json` no son self-contained.** Requieren un `source.db` preexistente (no se incluye en el repo) con el esquema y filas, además del DSN. Son templates por diseño ("replace... for your case"), pero ningún ejemplo de cutover/copy trae un source listo; ejecutarlos verbatim necesita crear primero la DB origen. Hallazgo por completitud, no por defecto.

- **H4 — `contracts/migration.contract.example.md` muestra stdout pretty-print.** Verdict 2 muestra el `cutoverReport` multi-line/indentado como "Expected stdout", pero el CLI emite JSON compacto de una línea vía `cliout.EmitJSON`. Cosmético; el contenido (campos y valores) coincide. Un agente que parseara literalmente esperaría newlines.

- **H5 — `docs/COMPATIBILITY.md` allowlist: `lower`/`upper` admiten `*`.** El doc describe `lower`,`upper` como "una expresión". El parser los agrupa con `count,sum,avg,min,max` (`sqlparse.go:100`), que aceptan `*`, así que `lower(*)`/`upper(*)` se aceptan y compilan a `LOWER(*)`/`UPPER(*)` (SQL inválido en la práctica). Over-acceptance menor; no se alcanza por entradas reales del catálogo.

- **H6 — `docs/reports/VECTOR-COMPAT-REPORT.md` stale.** Afirma "The `compat` layer has **no `vector` type family**"; `compat` ya tiene `VectorType`/`vector(N)`/`canonical_vectors`. El experimento y su propósito siguen siendo ciertos; el reporte adjunto no se actualizó. (Sección 4.)

- **H7 — `docs/COMPATIBILITY.md` "Significado de los estados" lista 3 estados; el código define 5.** `MappingStatus` (`features.go:30-37`) define `Exact, Transformed, Emulated, Unsupported, Unknown`; `assess` sólo retorna `Exact`/`Unknown`, así que `Transformed`/`Emulated`/`Unsupported` son constantes muertas. El doc simplifica a 3 estados (exacto canónico, traducible externo, no resuelto), que no son nombres literales del código. Drift menor doc/enum.

### ALTA
Ninguna.

---

## Conteo final

- **ALTA: 0**
- **MEDIA: 2** (H1 doc Date, H2 test-system.ps1)
- **BAJA: 5** (H3, H4, H5, H6, H7)

Áreas limpias explícitas: `docs/ARCHITECTURE.md` (14/14), `docs/OPERATIONS.md` (9/9), `examples/contract.example.json` + `schema.example.json`, `scripts/check.ps1`, `experiments/vector/` (build/vet/go.mod/propósito), y la calidad estructural de la suite e2e (sin tests vacíos, sin helpers muertos, sin asserts debilitados por la unificación CLI). El único fallo de ejecución e2e es el baseline intencional (H2).