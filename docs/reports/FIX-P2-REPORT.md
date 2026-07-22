# FIX-P2 — Cutover sin ventana + catch-up tolerante

## Resumen

Implementé el catch-up idempotente OPT-IN (`ApplyChangesTolerant`) y la CLI
`compat-cutover` que orquesta un cutover SQLite → PostgreSQL sin ventana de
corte. La semántica de `ApplyChanges` existente queda **intacta**: el modo
tolerante es una rama opt-in que comparte el mismo internals vía un flag
`tolerant bool` en `applyChanges`/`applyChange`.

## Archivos tocados

- `compat/replicate.go` — `ApplyChanges` (wrapper), `ApplyChangesTolerant` (nuevo), `applyChanges` (interno con flag), `applyChange` (rama tolerante opt-in).
- `compat/replicate_test.go` — 4 unit tests del modo tolerante.
- `cmd/compat-cutover/main.go` — CLI nuevo.
- `e2e/cutover_test.go` — 5 tests e2e (build tag `e2e`).
- `docs/USAGE.md`, `README.md` — sección del nuevo CLI + `ApplyChangesTolerant`.
- `cutover.example.json` — ejemplo coherente con los `.example` existentes (DSN de ejemplo, no el real).

## Diseño del modo tolerante

`ApplyChangesTolerant` difiere de `ApplyChanges` **sólo** en la resolución de
conflictos. Un cambio se considera YA APLICADO (se marca en
`__compat_applied_changes` y se continúa) cuando el estado ACTUAL del destino
coincide con el estado FINAL del cambio:

- **insert**: la fila existe y `rowsEqual(after, actual)`.
- **update**: la fila existe y `rowsEqual(after, actual)` aunque `Before` no matchee.
- **delete**: la fila ya no existe.

Cualquier otra divergencia sigue siendo `ConflictError` estricto. El path
`tolerant=false` es byte-idéntico al comportamiento anterior (rama tolerante
skip-eada), por lo que `ApplyChanges` no cambia su semántica — condición de no-aborto cumplida.

## Desviación documentada del diseño cerrado

El paso (b) del PM (“abrir stores, `ApplySchema` en destino”) **choca** con el
paso (d) (“`ImportSnapshot` destino”) en un destino vacío. Verificación empírica
previa a la implementación:

```
$ go run /tmp/probe_import.go
ImportSnapshot err: apply base schema: SQL logic error: table "entries" already exists (1)
```

`ImportSnapshot` crea el schema canónico completo (tablas, constraints,
triggers, metadata) en una transacción; `CompileDDL` emite `CREATE TABLE` sin
`IF NOT EXISTS` (`compat/ddl.go:103`), de modo que `ApplySchema` + `ImportSnapshot`
duplican las tablas y el segundo falla. Como `store.go` está fuera de scope
(no se puede modificar `ImportSnapshot`), la herramienta funcional usa
`ImportSnapshot` como bring-up único de schema+datos, **omitiendo** el
`ApplySchema` redundante. Esto está comentado en `cmd/compat-cutover/main.go`.
No es un aborto: el modo tolerante no requiere cambiar `ApplyChanges`, y la
herramienta queda verde.

## Definición de hecho — salida real

### 1. `go build ./... && go vet ./... && go test ./...`

```
=== build ===
build OK
=== vet ===
vet OK
=== unit test ===
?   example.com/sqlite-postgres-compat/cmd/compat-audit   [no test files]
?   example.com/sqlite-postgres-compat/cmd/compat-copy    [no test files]
?   example.com/sqlite-postgres-compat/cmd/compat-cutover [no test files]
ok  example.com/sqlite-postgres-compat/compat             (cached)
```

### 2. `go test -tags=e2e ./e2e -run TestCutover -v -count=1 -timeout 600s` (COMPAT_POSTGRES_DSN vivo, password enmascarado)

DSN usado: `postgres://postgres:***@31.220.22.176:5434/postgres?sslmode=disable`

```
=== RUN   TestCutoverCLIEndToEnd
--- PASS: TestCutoverCLIEndToEnd (1.83s)
=== RUN   TestCutoverOverlapTolerantResolvesInsertInSnapshot
--- PASS: TestCutoverOverlapTolerantResolvesInsertInSnapshot (0.03s)
=== RUN   TestCutoverOverlapStrictFailsOnInsertInSnapshot
--- PASS: TestCutoverOverlapStrictFailsOnInsertInSnapshot (0.02s)
=== RUN   TestCutoverOverlapUpdateStrictConflicts
--- PASS: TestCutoverOverlapUpdateStrictConflicts (0.03s)
=== RUN   TestCutoverTolerantGenuineDivergenceStillConflicts
--- PASS: TestCutoverTolerantGenuineDivergenceStillConflicts (0.01s)
PASS
ok  example.com/sqlite-postgres-compat/e2e  5.734s
```

Salida stderr del CLI en `TestCutoverCLIEndToEnd` (señal de vida por fase):

```
compat-cutover: audit: exact coverage for 3 required features
compat-cutover: capture: change capture installed on source
compat-cutover: snapshot: imported into destination
compat-cutover: catch-up: drained after 0 changes
{"status":"ready","source_digest":"f671403654b8b7851760fc724460ed61a33f379baac790b364a4322f89faf83a","destination_digest":"f671403654b8b7851760fc724460ed61a33f379baac790b364a4322f89faf83a","changes_applied":0}
```

### 3. `go test -tags=e2e ./e2e -count=1 -timeout 600s` — resumen

23 PASS (18 preexistentes + 5 nuevos) + SOLO el FAIL intencional
`TestSystemClaimsExactCoverageForRequiredFeatureFamilies`:

```
--- PASS: TestSystemPortableCoreRoundTripSQLitePostgresSQLite (0.95s)
--- PASS: TestSystemCompatCopyCLIEndToEnd (2.74s)
--- PASS: TestSystemPreservesArbitraryPrecisionDecimals (0.90s)
--- PASS: TestSystemCanonicalViewProducesEquivalentResults (1.27s)
--- PASS: TestSystemCanonicalTriggerProducesEquivalentEffects (0.99s)
--- PASS: TestSystemCanonicalRoutineExecutesEqually (1.01s)
--- PASS: TestSystemCanonicalFullTextReturnsEquivalentResults (0.99s)
--- PASS: TestSystemReconstructsExactCanonicalSchemaFromBothEngines (0.78s)
--- PASS: TestSystemReplicatesIncrementalChangesBothDirections (2.82s)
--- PASS: TestSystemPreservesJSONUUIDAndTimestampSemantics (0.89s)
--- PASS: TestSystemEnforcesForeignKeysEqually (1.22s)
--- PASS: TestSystemEnforcesCanonicalChecksAndIndexesEqually (1.26s)
--- PASS: TestSystemInspectsNativeSchemaObjectsWithoutMetadata (4.77s)
--- PASS: TestSystemAutomaticallyCapturesAndReplicatesBothDirections (2.98s)
--- PASS: TestDatabaseDSNPreservesConnectionParameters (0.00s)
--- PASS: TestSuppressAntiEchoOnReplicatedWrites (2.51s)
--- PASS: TestSuppressIsolationDoesNotLeakAcrossConnections (2.91s)
--- PASS: TestSuppressReapplicationIsIdempotent (2.81s)
--- PASS: TestCutoverCLIEndToEnd (1.83s)
--- PASS: TestCutoverOverlapTolerantResolvesInsertInSnapshot (0.03s)
--- PASS: TestCutoverOverlapStrictFailsOnInsertInSnapshot (0.02s)
--- PASS: TestCutoverOverlapUpdateStrictConflicts (0.03s)
--- PASS: TestCutoverTolerantGenuineDivergenceStillConflicts (0.01s)
--- FAIL: TestSystemClaimsExactCoverageForRequiredFeatureFamilies (0.00s)
FAIL  example.com/sqlite-postgres-compat/e2e  42.206s
```

El FAIL es el intencional de la baseline (`foreign_keys`, `check_constraints`,
`indexes`, `triggers`, `views`, `stored_routines`, `full_text` → `status=unknown
reason=requires parser and semantic compiler`). No se modificaron tests
preexistentes.

### 4. `cutover.example.json`

Creado en la raíz con DSN de ejemplo (`postgres://user:password@localhost:5432/...`),
coherente con `migration.example.json`/`contract.example.json`. El password real
del DSN **no** aparece en el ejemplo ni en docs.

### 5. Tests preexistentes

No se modificaron ni borraron tests preexistentes. `e2e/system_test.go`,
`e2e/suppress_test.go`, `capture.go`, `store.go` sin tocar.

## Notas

- El cutover del DSN de la aplicación es **manual** (documentado en `--help`/error de uso, `docs/USAGE.md` y `README.md`).
- Exit codes: `0` ready, `1` diverged/error/no-exact, `2` uso incorrecto.
- DSN real verificado persistente antes de construir (3 reintentos implícitos vía `TestMain` ping); el e2e crea y destruye su DB temporal por run.