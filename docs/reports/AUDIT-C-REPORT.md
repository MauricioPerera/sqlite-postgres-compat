# AUDIT-C — Auditoría READ-ONLY de consistencia docs ↔ código ↔ CLI ↔ e2e

Fecha: 2026-07-22
Scope auditado: `README.md`, `VALIDATION_REPORT.md`, `docs/*.md`, `cmd/compat-audit/main.go`, `cmd/compat-copy/main.go`, `e2e/system_test.go`, `scripts/test-system.ps1`, `contract.example.json`, `migration.example.json`. Referencia de structs: `compat/spec.go`, `compat/features.go`, `compat/schema.go` (lectura puntual de símbolos vía grep en el resto de `compat/`).
Regla: no se modificó ningún archivo del repo. No se levantó Postgres ni se corrió el e2e.

---

## (a) Tabla de inconsistencias doc ↔ código

| # | Doc (archivo:línea) — qué dice | Código (archivo:línea) — qué hay | Veredicto |
|---|---|---|---|
| 1 | `docs/TESTING.md:58` — "Qué valida extremo a extremo: … idempotencia, **conflictos** y supresión de ecos" (lista de lo que valida el e2e) | `e2e/system_test.go` no contiene **ningún** test de conflicto (grep `conflict` → 0 matches). La detección de conflictos sólo está probada a nivel unitario en `compat/replicate_test.go:66` (`var conflict *ConflictError`). El e2e cubre idempotencia (`system_test.go:537`, reaplicación de stream) y supresión de ecos (`system_test.go:954` y `:979`), pero **no** conflictos. | **INCONSISTENCIA**: la lista "extremo a extremo" incluye `conflictos`, que no tiene cobertura e2e (sólo unitaria). |
| 2 | `VALIDATION_REPORT.md:43` — "Detección de conflictos antes de sobrescribir cambios divergentes" aparece bajo "Comprobaciones superadas" (encabezado que sigue a "La batería se ejecutó contra SQLite real y PostgreSQL 17.5 real") | El e2e no prueba conflictos (ver #1). La comprobación existe sólo en unit test `compat/replicate_test.go:56-68`. | **AMBIGÜEDAD**: si la lista se entiende como parte de la batería integral/e2e (como sugiere el encabezado de `VALIDATION_REPORT.md:9`), `conflictos` no está verificado e2e. Es exacto sólo si se cuenta la prueba unitaria. Relacionado con #1. |

**Categoría revisada y limpia (sin inconsistencias):**

- **Features documentadas vs código** (`README.md:7`, `VALIDATION_REPORT.md:11-44`, `docs/COMPATIBILITY.md:28-38`): cada capacidad listada tiene símbolo o test que la respalda. `compat/features.go:60-64` declara `Exact` exactamente las `canonical_*` que enumera `docs/COMPATIBILITY.md:30-36` (`canonical_foreign_keys`, `canonical_check_constraints`, `canonical_indexes`, `canonical_views`, `canonical_triggers`, `canonical_routines`, `canonical_full_text`) más `tables`/`primary_keys`/`transactions`. Las familias genéricas `foreign_keys`, `check_constraints`, `indexes`, `views`, `triggers`, `stored_routines`, `full_text` son `Unknown` (`features.go:64`), coincidiendo con `docs/COMPATIBILITY.md:38`. Limpio.
- **Tablas internas reservadas** (`docs/ARCHITECTURE.md:85-89`, `docs/OPERATIONS.md:72,75`): los 4 nombres coinciden con las constantes en `compat/store.go:25` (`__compat_schema`), `compat/replicate.go:12-13` (`__compat_applied_changes`, `__compat_capture_state`), `compat/capture.go:13` (`__compat_change_journal`) y el prefijo de triggers `__compat_capture_` (`compat/capture.go:89`). Limpio.
- **API Go de `docs/USAGE.md:58-162`**: todos los símbolos referenciados existen — `OpenSQLite`/`OpenPostgres`/`OpenStore` (`compat/store.go:41,60,27`), `ApplySchema` (`store.go:72`), `ExportSnapshot`/`ImportSnapshot` (`store.go:98,151`), `VerifySnapshots`/`RequireEquivalent`/`VerificationReport` (`compat/verify.go:39,55,33`), `InspectSchema` (`compat/inspect.go:28`), `InstallChangeCapture` (`compat/capture.go:17`), `ReadCapturedChanges(ctx,schema,after,limit)` (`capture.go:151`, firma coincide con `USAGE.md:124`), `ApplyChanges` (`replicate.go:29`), `CallRoutine` (`runtime.go:14`), `SearchText` (`runtime.go:97`), `ConflictError` (`replicate_test.go:66`), `Value`/`IntegerValue`/`TextValue` (`compat/journal.go:12,22,25`). Limpio.
- **Versión de Go** (`README.md:13` "Go 1.26 o la indicada por `go.mod`"): `go.mod:3` dice `go 1.26`. Limpio.
- **Env var y build tag e2e** (`docs/TESTING.md:17-18`): `COMPAT_POSTGRES_DSN` es leída por `e2e/system_test.go:25`; el tag `e2e` coincide con `//go:build e2e` (`system_test.go:1`) y con `-tags=e2e` del script (`scripts/test-system.ps1:14`). Limpio.
- **Conteo de la batería** (`VALIDATION_REPORT.md:58` "15 pruebas superiores superadas y 1 fallida"): el e2e define 16 funciones `Test*`; 15 pasan y `TestSystemClaimsExactCoverageForRequiredFeatureFamilies` (`system_test.go:1012`) falla intencionalmente. 15+1=16. Aritméticamente correcto (ver sección (d)/limitaciones para un matiz de etiquetado).

---

## (b) Flags / subcomandos CLI reales vs documentados (completa)

Ambos CLIs **no definen flags ni subcomandos**. Ambos exigen exactamente un argumento posicional vía `len(os.Args) != 2` (uso incorrecto → `os.Exit(2)`):

| CLI | Código (archivo:línea) | Args/flags reales | Uso real (string de error) | Documentado en |
|---|---|---|---|---|
| `compat-audit` | `cmd/compat-audit/main.go:12-15` | 1 posicional `<contract.json>`, sin flags | `uso: compat-audit <contract.json>` | `README.md:22,34`, `docs/USAGE.md:6` |
| `compat-copy` | `cmd/compat-copy/main.go:22-25` | 1 posicional `<migration.json>`, sin flags | `uso: compat-copy <migration.json>` | `README.md:28,40`, `docs/USAGE.md:49` |

Comparación flag-por-flag: **no hay flags** ni en código ni en docs. Las invocaciones documentadas (`go run ./cmd/compat-audit .\contract.example.json`, `go run ./cmd/compat-copy .\migration.example.json`, y las variantes `.\contract.json`/`.\migration.json` en `README.md:34,40`) pasan exactamente un argumento posicional, coincidente con el código. **Categoría limpia.**

Observaciones (omisiones, no claims falsos): la docs no mencionan que el arg-count incorrecto sale con código `2` (`compat-audit/main.go:14`, `compat-copy/main.go:24`), ni que los errores de lectura/parse/auditoría salen con `1` vía `fatal`. `docs/USAGE.md:9` sólo documenta "termina con código `1` si cualquier capacidad requerida no es exacta" (correcto, `compat-audit/main.go:32-34`). No se documenta mal; sólo no se documenta el `2`.

---

## (c) Veredicto sobre los dos `.json` de ejemplo

### `contract.example.json`
- Deserialize en `compat.Contract` (`compat/spec.go:49-53`): `source`/`destination` → `Target{Engine,Version}` (`spec.go:31-34`), `Version` → `{major,minor,patch}` (`spec.go:17-21`), `required_features` → `[]Feature` (`spec.go:52`).
- Valores: `"engine":"sqlite"/"postgres"` válidos (`spec.go:13-14`); `"version":{major,minor,patch}` válidos; `"required_features":["tables","canonical_foreign_keys","transactions"]` — los tres son constantes `Feature` válidas (`features.go:8,11,14`) y los tres se resuelven a `Exact` (`features.go:60`).
- **Verificación ejecutada** (no requiere Postgres):
  ```
  $ go run ./cmd/compat-audit ./contract.example.json
  [{"feature":"tables","status":"exact"},{"feature":"canonical_foreign_keys","status":"exact"},{"feature":"transactions","status":"exact"}]
  ===AUDIT EXIT 0===
  ```
- **Veredicto: COINCIDE.** La estructura parsea correctamente y el ejemplo funciona como está documentado (sale JSON + código 0).

### `migration.example.json`
- Deserialize en `migrationConfig` (`cmd/compat-copy/main.go:14-19`): `source_dsn`→`SourceDSN`, `destination_dsn`→`DestinationDSN`, `contract`→`compat.Contract`, `schema`→`compat.Schema`.
- `schema` vs `compat.Schema` (`compat/schema.go:8-14`): `tables` → `[]Table`. `Table` (`schema.go:16-20`): `name`, `columns`, `constraints`. `Column` (`schema.go:22-27`): `name`, `type{family}`, `nullable` — todos los tags coinciden (`json:"name"`, `json:"type"`, `json:"nullable"`). `Type.Family` → `json:"family"` (`schema.go:30`); valores `"integer"`/`"text"` = constantes `IntegerType`/`TextType` (`schema.go:41,43`). `Constraint.Kind` → `json:"kind"` (`schema.go:52`); `"primary_key"` = `PrimaryKey` (`schema.go:61`); `Columns` → `json:"columns"` (`schema.go:53`).
- `contract` (sin `required_features`): válido — `compat-copy` añade `InferFeatures(schema)` (`main.go:37`), que para este schema añade `tables` + `primary_keys` (ambos `Exact`). `Contract.Validate()` exige `source.engine != destination.engine` (`spec.go:62-64`): sqlite≠postgres ✓.
- **Veredicto: COINCIDE estructuralmente.** La estructura es exactamente la que el código deserializa y `Schema.Validate()` (`schema.go:183`) la aceptaría (tabla `entries` no reservada, columnas con nombre y tipo, PK válida). No se ejecutó porque requiere DSN reales (ver (d)); la no-ejecución es por entorno, no por desajuste de estructura.

---

## (d) Comandos de docs verificados con salida real pegada

Verificados (no requieren Postgres; corridos en este entorno, `go version go1.26.4 windows/amd64`):

```
$ go build ./...
===BUILD EXIT 0===

$ go vet ./...
===VET EXIT 0===

$ go test ./... -timeout 60s
?   	example.com/sqlite-postgres-compat/cmd/compat-audit	[no test files]
?   	example.com/sqlite-postgres-compat/cmd/compat-copy	[no test files]
ok  	example.com/sqlite-postgres-compat/compat	(cached)
===TEST EXIT 0===

$ go run ./cmd/compat-audit ./contract.example.json
[{"feature":"tables","status":"exact"},{"feature":"canonical_foreign_keys","status":"exact"},{"feature":"transactions","status":"exact"}]
===AUDIT EXIT 0===
```

Documentados en: `docs/TESTING.md:6-7` (`go test ./...`, `go vet ./...`), `README.md:20-22` (inicio rápido: `go test ./...`, `go vet ./...`, `go run ./cmd/compat-audit .\contract.example.json`). Todos corren limpios como dice la docs.

### NO VERIFICADA (requieren PostgreSQL; no se levantó Postgres por regla)

| Comando | Origen en docs | Motivo de no verificación |
|---|---|---|
| `go run ./cmd/compat-copy .\migration.example.json` | `README.md:28`, `docs/USAGE.md:49` | Abre stores reales: `OpenStore(source)` → `source.db` inexistente; `OpenStore(postgres)` → DSN `postgres://user:password@localhost:5432/database` inalcanzable sin PG levantado. Requiere Postgres + DSN reales. |
| `.\scripts\test-system.ps1` | `README.md:50`, `docs/TESTING.md:24,30` | `scripts/test-system.ps1:6` setea `COMPAT_POSTGRES_DSN` y `:14` corre `go test -tags=e2e ./e2e`, que requiere PG admin (crea/drop DB temporal, `e2e/system_test.go:44,58`). |
| `go test -tags=e2e ./e2e -v -count=1 -timeout 60s` | `docs/TESTING.md:18` | `e2e/system_test.go:25-29` aborta con exit 2 si `COMPAT_POSTGRES_DSN` no se conecta (`admin.PingContext`). Requiere PG. |
| `$env:COMPAT_POSTGRES_DSN = "..."` (seteo) | `docs/TESTING.md:17` | Variable de entorno; sólo tiene sentido con PG levantado. |

Cobertura e2e vs `docs/TESTING.md:48-59` ("Qué valida extremo a extremo") — cruzado contra las 16 funciones `Test*` de `e2e/system_test.go`:
- ida y vuelta SQLite→PG→SQLite: `TestSystemPortableCoreRoundTripSQLitePostgresSQLite` (:75) ✓
- CLI real `compat-copy`: `TestSystemCompatCopyCLIEndToEnd` (:122) ✓
- precisión decimal, JSON, UUID, timestamp, binarios: `TestSystemPreservesArbitraryPrecisionDecimals` (:190), `TestSystemPreservesJSONUUIDAndTimestampSemantics` (:556), binarios en :83/:919 ✓
- claves y acciones referenciales: `TestSystemEnforcesForeignKeysEqually` (:591) ✓
- `CHECK` e índices: `TestSystemEnforcesCanonicalChecksAndIndexesEqually` (:654) ✓
- vistas, triggers, rutinas canónicas y externas traducibles: `TestSystemCanonicalViewProducesEquivalentResults` (:229), `TestSystemCanonicalTriggerProducesEquivalentEffects` (:306), `TestSystemCanonicalRoutineExecutesEqually` (:370), `TestSystemInspectsNativeSchemaObjectsWithoutMetadata` (:721) ✓
- inspección sin metadatos propios: `TestSystemReconstructsExactCanonicalSchemaFromBothEngines` (:465) + :721 ✓
- captura automática y replicación ambas direcciones: `TestSystemAutomaticallyCapturesAndReplicatesBothDirections` (:912), `TestSystemReplicatesIncrementalChangesBothDirections` (:508) ✓
- idempotencia, **conflictos**, supresión de ecos: idempotencia (:537) ✓, supresión de ecos (:954,:979) ✓, **conflictos ✗** (ver inconsistencia #1) 
- limpieza de la base PG temporal: `TestMain` (:56-63) ✓

`scripts/test-system.ps1` — paths/flags referenciados: `go test ./... -timeout 60s` (:8), `go vet ./...` (:11), `go test -tags=e2e ./e2e -v -count=1 -timeout 60s` (:14). Todos existen/son válidos; el param `-PostgresDsn` (:1) coincide con `docs/TESTING.md:30`. Limpio en cuanto a referencias (no se ejecutó por requerir PG).

---

## (e) Trade-offs / limitaciones de esta auditoría

1. **No se ejecutó nada que requiera Postgres** (regla del encargo). Por tanto, las claims físicas de `docs/COMPATIBILITY.md:42-53` (mapeo de tipos: Decimal→`TEXT`/`NUMERIC(p,s)`, JSON/UUID/Timestamp→`TEXT`, etc.), los flujos de `compat-copy` y todo el e2e no fueron verificados en runtime. Se verificó que los **símbolos** existen y que la **estructura** de los `.json` coincide con los structs; no el comportamiento físico real.
2. **Resto de `compat/` fuera de scope.** No se auditó la lógica interna del compilador DDL, parsers de catálogo, runtime ni replicación (otros devs trabajan ahí). Sólo se hicieron greps puntuales para confirmar existencia de símbolos/constantes referenciados por docs. Una claim de docs respaldada por un símbolo existente no implica que el símbolo haga exactamente lo que la docs dice — sólo que existe.
3. **`VALIDATION_REPORT.md:30`** afirma traducción de "procedimientos PostgreSQL **SQL**/PLpgSQL parametrizados con inserciones canónicas". El e2e (`system_test.go:872`) sólo prueba un procedimiento `LANGUAGE plpgsql` de inserción; un procedimiento `LANGUAGE SQL` de inserción parametrizada **no** se prueba en el e2e (el único `LANGUAGE SQL` del e2e, `native_standalone` en `:892`, es una función con retorno y queda `Unresolved`). No se pudo determinar si el parser soporta procedimientos SQL de inserción porque esa lógica está en `compat/` fuera de scope. Posible sobre-claim de docs vs cobertura e2e, **no confirmado** como inconsistencia.
4. **Etiquetado "pruebas superiores"** (`VALIDATION_REPORT.md:58`): el conteo 15+1=16 es aritméticamente correcto, pero `TestDatabaseDSNPreservesConnectionParameters` (`system_test.go:1111`) —una de las 15 que pasan— es un test utilitario puro de strings, no una prueba "superior"/conductual. No afecta la exactitud del conteo, sólo la denominación.
5. Las omisiones de docs (código de salida `2` no documentado, ausencia de mención de "exige exactamente un argumento") se reportan como observaciones, no como inconsistencias: la docs no afirma nada falso al respecto.
6. No se verificó que los ejemplos Go de `docs/USAGE.md` compilen como programa standalone (son fragmentos); se verificó sólo que cada símbolo referenciado existe con la firma usada (cotejada contra usos reales en `e2e/system_test.go`).