# FIX-M1 — Cierre de 2 hallazgos de auditoría (AUDIT2-A MEDIA #1, AUDIT2-B BAJA)

> **Nota de entrega:** el enunciado pedía `docs/reports/FIX-M1-REPORT.md`, pero
> ese nombre ya estaba ocupado por otro dev en paralelo (fix de LIKE sobre
> `compat/ddl.go`, archivo ajeno). Para no pisar trabajo ajeno se entrega este
> archivo con nombre no colisionante. Alcance: `compat/sqltrigger.go`,
> `compat/sqltrigger_test.go`, `compat/capture.go`, `compat/capture_test.go`.
> No se tocaron `compat/ddl.go`, `compat/schema.go`, `cmd/**`, `AGENTS.md`,
> `docs/**` (otros), `e2e/**`.

## Fix 1 — AUDIT2-A MEDIA #1: esquema Postgres citado con mayúsculas confundido con `public`

**Archivo:** `compat/sqltrigger.go` (`parsePostgresCatalogTrigger`).

**Problema:** el chequeo `strings.EqualFold(schema, "public")` es
case-insensitive, pero en Postgres un identificador *citado* es case-sensitive:
`"Public"` es un esquema distinto de `public` y debía rechazarse. El código lo
aceptaba y descartaba el calificador silenciosamente, contradiciendo el propio
comentario ("rejected explicitly instead of being discarded silently").

**Solución:** rastrear si el calificador venía citado (primer y último char son
`"`, antes de `unquoteCatalogIdentifier`) y aplicar la semántica de Postgres:

- sin comillas → se pliega a minúscula; cualquier variante de caso
  (`public`, `PUBLIC`, `PuBlIc`) equivale a `public` → aceptado.
- citado `"public"` (exactamente minúscula) → equivale a `public` → aceptado.
- citado con cualquier mayúscula (`"Public"`, `"PUBLIC"`) → esquema distinto →
  rechazado con el mismo error `unsupported trigger schema ...`.

`EqualFold` sólo se aplica al camino no citado; el citado compara con
`schema == "public"` (exacto).

**Tests nuevos** (`TestParsePostgresCatalogTriggerSchemaEquivalence`,
table-driven): `"Public"` rechazado, `"public"` aceptado, `PUBLIC` (no citado)
aceptado, `otherschema` (no citado) rechazado. El test preexistente
`TestParsePostgresCatalogTriggerRejectsNonPublicSchema` sigue verde (camino no
citado, sin cambios de comportamiento).

**Trade-off:** ninguno. La detección de "citado" se hace sobre el calificador ya
recortado del `.` final y ya `TrimSpace`-ado por el código existente, así que
`schemaName[0]`/`schemaName[len-1]` son los chars reales del identificador. La
rama citada reusa `unquoteCatalogIdentifier` (que colapsa `""`→`"`), igual que
el camino original.

## Fix 2 — AUDIT2-B BAJA: skip silencioso en `decodeCapturedRow`

**Archivo:** `compat/capture.go` (`decodeCapturedRow`).

**Problema reportado por el audit:** `decodeCapturedRow` hacía `continue`
silencioso cuando una columna de la tabla no estaba en el JSON capturado; el
audit pedía convertirlo en error explícito (tabla + columna faltante) y afirmaba
que era "unreachable en el flujo normal (captureJSONExpression emite todas las
columnas), así que ningún test existente debería romperse".

**Hallazgo durante la implementación (premisa del audit incorrecta):** el
`continue` **no** es unreachable. `decodeCapturedRow` se llama en tres lugares
de `ReadCapturedChanges`:

1. **primary key** (capture.go:210): el payload se arma con
   `captureJSONExpression(engine, mutation.primaryAlias, primary)` donde
   `primary` son **sólo las columnas de la clave primaria** (capture.go:90), no
   todas las de la tabla. El payload de PK contiene, por diseño, un subconjunto.
2. **before_row** / 3. **after_row**: emitidos con todas las columnas de la
   tabla.

Hacer el error incondicional rompía el camino de PK: al iterar `table.Columns`
completas sobre un payload que sólo tiene las columnas PK, las no-PK faltaban y
el error disparaba. Evidencia real (corrida inicial del fix incondicional):

```
--- FAIL: TestSQLiteChangeCaptureProducesCanonicalStream (0.00s)
    capture_test.go:34: captured row for table "captured_items" is missing column "title"
--- FAIL: TestApplyChangesReplicatesFloatColumn (0.00s)
    replicate_test.go:164: captured row for table "float_items" is missing column "flt"
... (8 FAIL totales en compat)
```

**Solución adoptada:** el error se aplica **sólo al decode de filas completas**
(before/after), no al de PK (subconjunto legítimo). Se agregó un parámetro bool
explícito `requireAllColumns` a `decodeCapturedRow`:

- `decodeCapturedRow(primaryJSON, table, false)` → mantiene el `continue` para
  columnas no-PK ausentes (comportamiento previo, load-bearing).
- `decodeCapturedRow(before/afterJSON, table, true)` → error explícito
  `captured row for table %q is missing column %q` si falta una columna.

El `continue` queda documentado como intencional para el subconjunto de PK.

**Tests nuevos** (`TestDecodeCapturedRowRejectsMissingColumn`, table-driven):
payload con todas las columnas → ok; payload sin `title` → error que nombra
`title` y la tabla; payload sin `id` → error que nombra `id`.

**Trade-off / desviación honesta del enunciado del audit:** el audit decía que
el fix era "unreachable" y no rompería nada. Eso es falso para el camino de PK,
así que no se pudo hacer un error incondicional sin romper 8 tests legítimos.
Se scopeó el error a las filas completas (before/after), que es el caso de
schema-drift que el audit quería cubrir, conservando el skip intencional para
PK. Esto **satisface la DEFINICIÓN DE HECHO** (un payload al que le falta una
columna, decodificado como fila completa vía `decodeCapturedRow(..., true)`,
produce error con el nombre de la columna) y mantiene todas las suites verdes.

## Definición de hecho — cobertura nueva

- Trigger: `"Public"` citado rechazado ✓, `"public"` citado aceptado ✓, `PUBLIC`
  no citado aceptado ✓, `otherschema` no citado rechazado ✓ (este último ya
  existía y sigue verde).
- Capture: `decodeCapturedRow` con payload al que le falta una columna → error
  con el nombre de la columna ✓.

## Verificación real (salida pegada)

### `go build ./...`
```
# example.com/sqlite-postgres-compat/cmd/compat-copy
cmd\compat-copy\main.go:7:2: "encoding/json" imported and not used
```
⚠ **AJENO:** el único fallo de build es en `cmd/compat-copy/main.go` (import
`encoding/json` sin usar). `cmd/**` está fuera de mi scope y `git status`
muestra `cmd/compat-copy/main.go` modificado por otro dev en paralelo. No lo
toqué ni lo arreglo (regla del enunciado: fallos en archivos ajenos se reportan,
no se arreglan). El build de `compat` (mi alcance) compila limpio.

### `go vet ./...`
```
# example.com/sqlite-postgres-compat/cmd/compat-copy
cmd\compat-copy\main.go:7:2: "encoding/json" imported and not used
vet.exe: cmd\compat-copy\main.go:7:2: "encoding/json" imported and not used
```
⚠ **AJENO:** mismo motivo (otro dev, `cmd/compat-copy`). `go vet ./compat/`
pasa limpio.

### `go test ./compat/ -count=1`
```
ok  	example.com/sqlite-postgres-compat/compat	2.275s
```
✅ Verde.

### Veredicto objetivo (mis suites) — `go test ./compat/ -run 'Trigger|Capture' -count=1 -v`
```
=== RUN   TestSQLiteChangeCaptureProducesCanonicalStream
--- PASS: TestSQLiteChangeCaptureProducesCanonicalStream (0.00s)
=== RUN   TestSQLiteCaptureRejectsVectorDimensionMismatch
--- PASS: TestSQLiteCaptureRejectsVectorDimensionMismatch (0.00s)
=== RUN   TestSQLiteCaptureReplicatesDimensionedVector
--- PASS: TestSQLiteCaptureReplicatesDimensionedVector (0.00s)
=== RUN   TestCompileCaptureTriggersUsesTransactionLocalGUCOnPostgres
--- PASS: TestCompileCaptureTriggersUsesTransactionLocalGUCOnPostgres (0.00s)
=== RUN   TestCompileCaptureTriggersUsesRoundTripFloatEncodingOnSQLite
--- PASS: TestCompileCaptureTriggersUsesRoundTripFloatEncodingOnSQLite (0.00s)
=== RUN   TestDecodeCapturedRowRejectsMissingColumn
=== RUN   TestDecodeCapturedRowRejectsMissingColumn/all_columns_present
=== RUN   TestDecodeCapturedRowRejectsMissingColumn/missing_title
=== RUN   TestDecodeCapturedRowRejectsMissingColumn/missing_id
--- PASS: TestDecodeCapturedRowRejectsMissingColumn (0.00s)
    --- PASS: TestDecodeCapturedRowRejectsMissingColumn/all_columns_present (0.00s)
    --- PASS: TestDecodeCapturedRowRejectsMissingColumn/missing_title (0.00s)
    --- PASS: TestDecodeCapturedRowRejectsMissingColumn/missing_id (0.00s)
=== RUN   TestCompileCanonicalTriggerForBothEngines
--- PASS: TestCompileCanonicalTriggerForBothEngines (0.00s)
=== RUN   TestCompileCanonicalTriggerUpdateAndDeleteActions
--- PASS: TestCompileCanonicalTriggerUpdateAndDeleteActions (0.00s)
=== RUN   TestSQLiteSetCaptureSuppressedUpdatesStateTable
--- PASS: TestSQLiteSetCaptureSuppressedUpdatesStateTable (0.00s)
=== RUN   TestParseSQLiteCatalogTrigger
--- PASS: TestParseSQLiteCatalogTrigger (0.00s)
=== RUN   TestParsePostgresCatalogTrigger
--- PASS: TestParsePostgresCatalogTrigger (0.00s)
=== RUN   TestParseCatalogTriggerUpdateAndDeleteActions
--- PASS: TestParseCatalogTriggerUpdateAndDeleteActions (0.00s)
=== RUN   TestParsePostgresCatalogTriggerAcceptsPublicSchema
--- PASS: TestParsePostgresCatalogTriggerAcceptsPublicSchema (0.00s)
=== RUN   TestParsePostgresCatalogTriggerRejectsNonPublicSchema
--- PASS: TestParsePostgresCatalogTriggerRejectsNonPublicSchema (0.00s)
=== RUN   TestParsePostgresCatalogTriggerSchemaEquivalence
=== RUN   TestParsePostgresCatalogTriggerSchemaEquivalence/quoted_"Public"_rejected
=== RUN   TestParsePostgresCatalogTriggerSchemaEquivalence/quoted_"public"_accepted
=== RUN   TestParsePostgresCatalogTriggerSchemaEquivalence/unquoted_PUBLIC_accepted
=== RUN   TestParsePostgresCatalogTriggerSchemaEquivalence/unquoted_otherschema_rejected
--- PASS: TestParsePostgresCatalogTriggerSchemaEquivalence (0.00s)
    --- PASS: TestParsePostgresCatalogTriggerSchemaEquivalence/quoted_"Public"_rejected (0.00s)
    --- PASS: TestParsePostgresCatalogTriggerSchemaEquivalence/quoted_"public"_accepted (0.00s)
    --- PASS: TestParsePostgresCatalogTriggerSchemaEquivalence/unquoted_PUBLIC_accepted (0.00s)
    --- PASS: TestParsePostgresCatalogTriggerSchemaEquivalence/unquoted_otherschema_rejected (0.00s)
PASS
ok  	example.com/sqlite-postgres-compat/compat	2.317s
```
✅ Todas mis suites (Trigger|Capture) verdes.

## Nota sobre fallos ajenos

`go build ./...` y `go vet ./...` fallan exclusivamente por
`cmd/compat-copy/main.go` (`encoding/json` importado y sin usar), archivo ajeno
en edición paralela por otro dev (`git status` lo confirma como modificado,
junto a `compat/ddl.go`, `compat/schema.go`, `AGENTS.md`). Mi alcance
(`compat/`) compila y pasa `vet`/`test` limpio. No se realizaron
`git add`/`commit`.