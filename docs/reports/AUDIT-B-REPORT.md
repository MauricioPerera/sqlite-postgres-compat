# AUDIT-B-REPORT — Auditoría READ-ONLY del runtime y la replicación

Scope: `compat/capture.go`, `journal.go`, `replicate.go`, `runtime.go`, `store.go`, `verify.go`, `inspect.go`, `spec.go`, `schema.go`, `features.go` y sus `*_test.go`.

Baseline (verificada, ver §Limitaciones para el entorno de pruebas):
- `go build ./...` → limpio.
- `go vet ./compat/` → limpio.
- `go test ./compat/` → `ok ... (cached)`.

Convenciones:
- **VERIFICADA**: afirmación demostrada con comando y salida real pegada.
- **NO VERIFICADA (por lectura)**: afirmación por lectura del código, sin demostración ejecutable (típicamente caminos de Postgres, que no tenían motor disponible — ver §Limitaciones).

---

## Hallazgos

### H1 — ALTA — Replicación de columnas `float` (REAL) produce conflicto espurio y divergencia silenciosa de datos

**Archivos:** `compat/capture.go:130`, `compat/capture.go:124-144` (escritura del journal), `compat/store.go:314-323` (`stringify`), `compat/store.go:258-267` (`canonicalValue` para `FloatType`), `compat/replicate.go:248-255` (`rowsEqual`), `compat/replicate.go:131-142` (`applyChange` update/delete).

**Descripción:** El `before`/`after` de un cambio se captura como texto vía SQL `CAST(col AS TEXT)` (`capture.go:129` `encoded := "CAST(" + value + " AS TEXT)"`). En `loadRow` (destino) el valor se reconstruye con `canonicalValue` → `stringify(driverValue)`, que para un `float64` usa `fmt.Sprint`. Estas dos productoras de texto **no coinciden para SQLite REAL**: `CAST(1.0 AS TEXT) = "1.0"` pero `fmt.Sprint(float64(1.0)) = "1"`. `canonicalValue` para `FloatType` **no normaliza** a una forma canónica (solo copia el texto, `store.go:266-267`). En `applyChange`, update/delete comparan `rowsEqual(change.Before, actual)` (`replicate.go:136`) por marshaling JSON byte-a-byte, así que `"1.0"` ≠ `"1"` → `ConflictError` espurio que aborta la operación.

**Evidencia (VERIFICADA), SQLite→SQLite, esquema con columna `FloatType`:**

Cast SQL vs stringify Go (demo `%TEMP%/audb-run.go`, corriendo desde el módulo del repo):
```
SQLite CAST(1.0 AS TEXT)="1.0" CAST(1 AS TEXT)="1" CAST('hi') AS TEXT)="hi"
```
```
Sprint(float64(1.0)) = "1"      # %TEMP%/audb-fmt.go (puro stdlib)
Sprint(float64(1.5)) = "1.5"
```

Update espurio abortado (insert + update del mismo stream):
```
ApplyChanges(insert+update) ERROR: replication conflict on flt primary key map[id:{integer 1}]
```

Delete no aplicado → divergencia fuente/destino (fila eliminada en origen permanece en destino — **pérdida de consistencia**):
```
after insert, dest amount="1"
delete ERROR (row NOT removed, source-dest DIVERGE): replication conflict on flt primary key map[id:{integer 1}]
rows with id=1 remaining in dest: 1 (source deleted it -> divergence)
```

**Impacto:** Toda columna `FloatType` (SQLite `REAL`/`DOUBLE`) hace que updates/deletes replicados fallen con conflicto falso; los deletes dejan la fila en el destino → divergencia silenciosa. Asumiendo el flujo primario SQLite→PostgreSQL, esto afecta cualquier esquema con columnas reales.

---

### H2 — ALTA — Cero cobertura de tests para cualquier camino de Postgres en runtime/replicación

**Archivos:** `compat/replicate_test.go`, `compat/capture_test.go`, `compat/store_test.go`, `compat/inspect_test.go` (todos los `*_test.go` en scope).

**Descripción:** Ningún test del scope abre un `Store` Postgres. Las únicas referencias a `Postgres` en tests de runtime son como **etiqueta del `Target` fuente** (`replicate_test.go:20,57` usan `Target{Engine: Postgres}` pero el `Store` es `OpenSQLite`), o en `ddl_test.go` (fuera de scope de runtime, sólo compila texto DDL). Consecuentemente no hay prueba de: triggers de captura Postgres (`compileCaptureTriggers` rama Postgres, `capture.go:114-119`), `driverValue` Postgres (`store.go:305-312`), `placeholder` `$N`, `ApplyChanges` contra destino Postgres, ni `inspectPostgres`.

**Evidencia (VERIFICADA):**
```
$ grep -rn "OpenPostgres\|OpenStore" compat/*_test.go
(none — sólo ddl_test.go cita Postgres para compilar DDL, no para ejecutar)
$ grep -rln "InstallChangeCapture\|ReadCapturedChanges\|ApplyChanges" compat/*_test.go
compat/capture_test.go
compat/replicate_test.go      # ambos usan OpenSQLite exclusivamente
```

---

### H3 — ALTA — El mecanismo anti-eco (supresión de captura) no tiene test end-to-end

**Archivos:** `compat/replicate.go:50-52,72-74,97-104` (`setCaptureSuppressed`), `compat/capture.go:110,117` (WHEN/IF sobre `suppress`).

**Descripción:** `ApplyChanges` setea `suppress=1` en `__compat_capture_state` para evitar que los triggers del destino journalicen las escrituras replicadas (bucle de eco). El test de idempotencia (`replicate_test.go:30`) reaplica el mismo stream **sin** `InstallChangeCapture` en el destino, por lo que sólo ejercita la dedup por `__compat_applied_changes`, **no** la supresión. No hay test que instale captura en el destino y verifique que `ApplyChanges` no genere journal de eco.

**Evidencia (VERIFICADA):**
```
$ grep -n "InstallChangeCapture" compat/replicate_test.go
(no matches — el destino del test de replicación nunca instala captura)
```
**NO VERIFICADA (por lectura):** comportamiento real del anti-eco con triggers presentes.

---

### H4 — MEDIA — `VerifySnapshots` es frágil para JSON: no canonicaliza el contenido del valor JSON

**Archivos:** `compat/store.go:276-290` (`canonicalValue` rama `JSONType`), `compat/verify.go:14-31` (`SnapshotDigest`), `compat/verify.go:11-13` (comentario).

**Descripción:** `canonicalValue(JSONType)` valida el JSON pero **almacena el texto original verbatim** (`store.go:290` `return Value{Kind: JSONValue, Value: text}, nil`) sin re-serializar a forma canónica. El digest se calcula por marshaling byte-a-byte de la `Row` (`verify.go:25`). El comentario de `verify.go:13` afirma “value names are encoded by encoding/json with stable map ordering”, pero eso sólo ordena las **claves del `Row`/`Value` Go**, no el contenido del string JSON. Dos motores que almacenen el mismo JSON lógico con distinto orden de claves o espaciado producen digests distintos → falso negativo de equivalencia.

**Evidencia (VERIFICADA)** — dos snapshots idénticos salvo orden de claves del JSON:
```
JSON key-order differs only: Equivalent=false  src=ddc10f8a...c6df9 dst=cdae2e18...850221
```

---

### H5 — MEDIA — Race condition en la supresión de captura si `ApplyChanges` corre concurrentemente sobre destino Postgres

**Archivos:** `compat/replicate.go:97-104` (`setCaptureSuppressed`), `compat/store.go:60-66` (`OpenPostgres` sin `SetMaxOpenConns`).

**Descripción:** `suppress` es una **bandera global** (fila única `id=1` en `__compat_capture_state`), no un mecanismo por transacción. Bajo MVCC, una segunda tx de `ApplyChanges` concurrente lee `suppress=0` del estado cometido (no ve el `=1` no cometido de la primera tx), así que los triggers de la segunda tx **sí journalizan** sus escrituras replicadas → eco / ruptura de idempotacia. SQLite está protegido por `SetMaxOpenConns(1)` (`store.go:48`); Postgres no tiene límite de conexiones (`OpenPostgres` no configura pool). **NO VERIFICADA** (no hay Postgres disponible).

---

### H6 — MEDIA — Captura desde un origen Postgres falla al parsear columnas `timestamp` (y posiblemente `float`)

**Archivos:** `compat/capture.go:124-144` (`captureJSONExpression`, `CAST(col AS TEXT)` para toda columna), `compat/store.go:271-275` (`canonicalValue` rama `TimestampType` usa `time.Parse(time.RFC3339Nano, text)`), `compat/capture.go:178`.

**Descripción:** Para Postgres, el valor de cada columna se journaliza como `CAST(col AS TEXT)`. Para `timestamptz` eso produce texto estilo `"2026-07-22 16:06:34+00"` (separador espacio, offset corto), que `time.Parse(time.RFC3339Nano, ...)` **rechaza** (requiere `T` y `Z`/`±HH:MM`). Entonces `ReadCapturedChanges` → `decodeCapturedRow` → `canonicalValue(TimestampType)` retorna error y aborta la lectura de cambios. Nótese que `committed_at` sí se formatea explícitamente con `T`/`Z` (`capture.go:104`), por eso no se rompe; el problema es **sólo las columnas de datos**. **NO VERIFICADA** (sin Postgres). El flujo primario SQLite→PostgreSQL no se ve afectado (origen SQLite), pero la dirección inversa (soportada por `Contract`) sí.

---

### H7 — MEDIA — `SearchText` carga la tabla completa en memoria sin `WHERE`/`LIMIT`

**Archivos:** `compat/runtime.go:105` (`store.DB.QueryContext(ctx, "SELECT ... FROM "+table)` sin predicado ni límite), `compat/runtime.go:112-140`.

**Descripción:** La búsqueda full-text materializa **todas** las filas y columnas solicitadas en el proceso Go y tokeniza cada una. Para tablas grandes es OOM y latencia no acotada. No hay paginación ni límite. **NO VERIFICADA** (por lectura del código).

---

### H8 — MEDIA — `SearchText` tokeniza `NULL` como la cadena literal `"<nil>"`

**Archivos:** `compat/runtime.go:122-125` (`stringify(value)`), `compat/store.go:314-323` (`stringify(nil)` → `fmt.Sprint(nil)` → `"<nil>"`).

**Descripción:** Si una columna de texto de la fila es `NULL`, `values[i]` es `nil` y `stringify(nil)` = `"<nil>"`, que se tokeniza como el token `nil`. Esto puede producir matches falsos si el query contiene “nil” y nunca debería ocurrir. **NO VERIFICADA** (por lectura).

---

### H9 — MEDIA — `InferFeatures` no emite `CanonicalFullText`; `FullText` queda `Unknown` pese a que `SearchText` lo implementa

**Archivos:** `compat/features.go:58-69` (`assess`: `FullText`→`Unknown`), `compat/features.go:83-122` (`InferFeatures` no menciona `CanonicalFullText`), `compat/runtime.go:97-146` (`SearchText`).

**Descripción:** `assess(CanonicalFullText)` devuelve `Exact` (`features.go:60`) e `InferFeatures` nunca lo emite (no hay rama para él). La feature `FullText` (no-canónica) se reporta `Unknown` “requires parser and semantic compiler” aunque `SearchText` ya implementa tokenización Unicode determinística en Go. Hay una desconexión feature-gate ↔ implementación: la feature canónica existe y es `Exact` pero no se infiere; la no-canónica se declara desconocida pese a estar implementada. **VERIFICADA** (por lectura; el grep de tests confirma que `SearchText`/`FullText` no se testean — ver §Cobertura).

---

### H10 — BAJA — `Change.TransactionID` es inconsistente entre engines y no se usa

**Archivos:** `compat/capture.go:102` (SQLite escribe `''`), `compat/capture.go:104` (Postgres escribe `txid_current()::text`), `compat/journal.go:54` (campo), `compat/replicate.go:106-125` (dedup por `source_engine`+`source_version`+`sequence`, ignora `TransactionID`).

**Descripción:** El journal SQLite siempre deja `transaction_id` vacío; el de Postgres lo popula. El campo se parsea en `ReadCapturedChanges` (`capture.go:177`) y se guarda en `Change.TransactionID`, pero `ApplyChanges`/`markChangeApplied` **nunca lo usan** (la dedup es por secuencia). Campo a medio cablear / inconsistencia de formato journal entre engines. **VERIFICADA** por lectura del código (el uso de `TransactionID` post-parseo no aparece en `replicate.go`):
```
$ grep -n "TransactionID" compat/replicate.go compat/capture.go
compat/capture.go:102: ... strftime(...), '', ...          # SQLite: vacío
compat/capture.go:104: ... txid_current()::text, ...       # Postgres: poblado
(grep de uso en replicate.go: ninguno)
```

---

### H11 — BAJA — `CallRoutine` sólo soporta acciones `insert`

**Archivos:** `compat/runtime.go:36-39` (`if action.Kind != "insert" { return error }`).

**Descripción:** Los recientes commits “support trigger update and delete actions” y “translate native insert procedures” extendieron triggers, pero `CallRoutine` sigue rechazando cualquier acción que no sea `insert`. Rutinas con update/delete quedan sin cablear. **VERIFICADA** por lectura.

---

### H12 — BAJA — Doble `Close()` de `objects` (rows de vistas) en `inspectPostgres`

**Archivos:** `compat/inspect.go:457` (`defer objects.Close()`) y `compat/inspect.go:471` (`if err := objects.Close(); ...`).

**Descripción:** `objects.Close()` se llama explícitamente y luego el `defer` lo cierra otra vez al salir de la función. `sql.Rows.Close()` es idempotente, así que no es un bug funcional, pero es redundante y siembra confusión sobre el ciclo de vida del recurso. **VERIFICADA** por lectura.

---

### H13 — BAJA — `Version.Valid()` acepta versión cero `Version{0,0,0}`

**Archivos:** `compat/spec.go:23-25` (`v.Major >= 0 && v.Minor >= 0 && v.Patch >= 0`).

**Descripción:** El valor cero `Version{}` es “válido”, así que `Target{Engine: SQLite, Version: Version{}}` pasa `Validate()`. La dedup en `markChangeApplied`/`changeWasApplied` usa `change.Source.Version.String()` como parte de la clave (`replicate.go:112,123`), entonces dos orígenes reales distintos con versión cero colisionarían. Valor hardcodeado/debería ser config o validarse como positivo. **VERIFICADA** por lectura; `spec_test.go` no cubre el caso cero.

---

### H14 — BAJA — `ConflictError.Error()` no incluye `Expected`/`Actual`

**Archivos:** `compat/replicate.go:15-24`.

**Descripción:** El struct expone `Expected`/`Actual` pero `Error()` sólo imprime tabla y PK, descartando la información de diagnóstico que el struct porta. Dificulta depurar conflictos (incluyendo los espurios de H1). **VERIFICADA** por lectura (la salida del demo H1 muestra el mensaje sin before/actual).

---

## Categorías sin hallazgos adicionales

- **Recursos sin cerrar (rows/tx/conexiones) — en su mayor parte correctos.** `ReadCapturedChanges` (`capture.go:160` `defer rows.Close()`), `ExportSnapshot`/`exportTable` (`store.go:123`), `inspectSQLite`/`inspectSQLiteTable`/`inspectSQLiteIndexes` y `inspectPostgres` cierran sus `rows` con chequeo de error. Las `tx` usan `defer tx.Rollback()` consistentemente. Sólo se halló el doble-close cosmético de H12.
- **Errores ignorados con `_` que afecten runtime/replicación:** no se hallaron en el scope. Los `_ =` presentes son en `SnapshotDigest` (`verify.go:19-20`) sobre `json.Marshal` de `Row` (que no puede fallar para tipos estáticos) — acceptable.
- **Transacciones mal delimitadas:** `ApplyChanges` envuelve el lote completo en una tx atómica con `defer Rollback()` (`replicate.go:42-46,75`); correcto. La supresión `suppress` se setea y se revierte dentro de la misma tx (commitea `false` antes del commit), así que un rollback revierte el `suppress=true`. Sin desbalanceo de transacciones hallado (salvo la race de H5).

## Cobertura de tests

Tests en scope: `journal_test.go`, `verify_test.go`, `store_test.go`, `runtime_test.go`, `replicate_test.go`, `spec_test.go`, `capture_test.go`, `inspect_test.go`.

Cubierto:
- Captura SQLite insert/update/delete y orden del stream (`capture_test.go`).
- `ApplyChanges` idempotencia + detección de conflicto con rollback, SQLite únicamente (`replicate_test.go`).
- Round-trip snapshot SQLite (UUID/Boolean/JSON) + normalización de timestamp/JSON a nivel `canonicalValue` (`store_test.go`).
- `OrderedChanges` rechaza secuencias duplicadas (`journal_test.go`).
- `SnapshotDigest` ignora orden de filas (`verify_test.go`).
- `InspectSchema` recupera metadata canónica e inspección de catálogo SQLite externo + tablas reservadas (`inspect_test.go`).
- `tokenize` Unicode/case (`runtime_test.go`).
- `Contract`/`Audit`/`RequireExact`/`assess`/`InferFeatures` (`spec_test.go`).

Huecos (VERIFICADA vía grep):
- **Ningún test ejecuta Postgres** (H2): triggers de captura PG, `driverValue` PG, `placeholder $N`, `ApplyChanges` contra destino PG, `inspectPostgres` — 0 cobertura.
- **`FloatType`/`DecimalType`/`BinaryType`/`DateType` sin round-trip en captura/replicación** (donde vive el bug H1):
  ```
  $ grep -rn "FloatType\|FloatValue\|DecimalType\|BinaryType\|DateType" compat/*_test.go
  (sólo ddl_test.go compila texto DecimalType; store_test.go usa TimestampType vía canonicalValue unitario; nada en capture/replicate)
  ```
- **`SearchText`/`CallRoutine` — 0 tests**:
  ```
  $ grep -rn "SearchText\|CallRoutine" compat/*_test.go
  (NONE)
  ```
- **Anti-eco de supresión sin test e2e** (H3).
- **`VerifySnapshots` no prueba el caso JSON no-canónico** (H4) — el único test (`verify_test.go`) usa integers.
- **`Version` cero no se testea** (H13); `spec_test.go` sólo prueba engine desconocido.

## Trade-offs / limitaciones de esta auditoría

1. **Postgres no ejecutable.** No había motor Postgres disponible (`psql` ausente, sin directorio de instalación). Todo hallazgo sobre el camino Postgres (H5, H6, y la rama Postgres de H2/H10) está marcado **NO VERIFICADA (por lectura)**; se basan en lectura del código y comportamiento conocido de SQL.
2. **Bloqueo ambiental para módulos Go en `%TEMP%`.** Go 1.26.4 (binario del entorno) emite `ignoring go.mod in system temp root` para cualquier `go.mod` fuera del repo, incluso bajo `C:\`, `%HOME%` o `Documents`. Por ello los programas de demo se compilaron/ ejecutaron **desde el módulo del repo** (`go run "$TEMP/xxx.go"` con `cwd` = repo) y el **archivo fuente** vivió en `%TEMP%`. No se creó ni modificó ningún archivo del repo. Archivos temp usados (ya eliminados): `%TEMP%/audb-fmt.go`, `audb-run.go`, `audb-del.go`, `audb-json.go`, `probe.go`.
3. **Demos H1/H4 son SQLite→SQLite.** Replican la lógica exacta de captura (`CAST AS TEXT`) y de `loadRow`/`digest`, que es la misma independiente del engine destino; son representativos del flujo SQLite→PostgreSQL para la parte divergente. La divergencia específica demostrada (float) depende del origen SQLite; el comportamiento de `CAST(float AS TEXT)` en Postgres no se verificó.
4. **Scope acotado.** No se leyó `sqlparse.go`, `sqlselect.go`, `sqlroutine.go`, `sqltrigger.go`, `ddl.go`, `cmd/`, `e2e/`, `docs/`. Hallazgos que crucen con parseo DDL/SQL o el orquestador de `cmd`/`e2e` quedan fuera. Los helpers de parseo citados desde `inspect.go` (`parseCatalogSelect`, `parseCatalogExpression`, `extractCheckExpressions`, `matchingParenthesis`) se asumieron correctos por estar fuera de scope.
5. **No se modificó nada.** `go build`/`go vet`/`go test` son read-only; los programas temp se ejecutaron contra `:memory:` y se borraron. Sin secretos logueados.