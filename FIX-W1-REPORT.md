# FIX-W1 — VectorType de primera clase en el esquema canónico

Fecha: 2026-07-22
Perímetro tocado: `compat/schema.go`, `compat/ddl.go`, `compat/inspect.go`, `compat/store.go`, `compat/features.go`, `compat/journal.go`, `compat/vector_test.go` (nuevo), `docs/COMPATIBILITY.md`. Ningún archivo fuera del perímetro fue modificado (`capture.go`, `replicate.go`, `verify.go`, `runtime.go`, parsers, `cmd/`, `e2e/`, `experiments/` intactos).

## Decisiones de diseño

### Familia y validación (`schema.go`)
- Nueva familia `VectorType TypeFamily = "vector"` y `VectorValue ValueKind = "vector"` (en `journal.go`, donde viven todos los `ValueKind`).
- Validación en `Schema.Validate`: para `VectorType` exige **exactamente 1 argumento y > 0** (la dimensión). `DecimalType` no tenía validación de argumentos; la de vector se agregó en `Validate`, que es donde el esquema se valida. Rechaza: sin argumentos, 2 argumentos, dimensión 0, dimensión negativa.

### DDL (`ddl.go` `compileType`)
- SQLite → `TEXT`. Comentario documenta la decisión cerrada: el engine por defecto (modernc) no tiene funciones vectoriales; el carrier canónico validado contra libSQL/sqld y pgvector es texto `'[1,2,3]'` (ver `VECTOR-COMPAT-REPORT.md`).
- Postgres → `vector(N)` con `N = Arguments[0]`, reutilizando el closure `args()` existente (`"vector" + args()` → `"vector(3)"`). Comentario documenta que requiere pgvector en el destino; si falta, el `CREATE TABLE` falla con error claro del motor (aceptable).

### Canonicalización de valores (`store.go` `canonicalValue`)
- **Dónde se valida la dimensión**: la firma de `canonicalValue` es privada y se hizo **variádica** — `canonicalValue(kind TypeFamily, source any, dimension ...int)`. Esto es retrocompatible: los callers fuera del perímetro (`capture.go:225`, `replicate.go:176`) y los tests existentes llaman con 2 argumentos y siguen compilando sin tocarlos. La dimensión se pasa **solo desde `exportTable`** (`store.go`) para columnas vector, usando `column.Type.Arguments...`.
  - **Snapshot**: `exportTable` pasa la dimensión → `canonicalVectorValue` rechaza un valor cuya cuenta de componentes difiere de la declarada. Probado con `TestSQLiteVectorSnapshotRejectsDimensionMismatch` (vector(3) + valor `'[1, 2]'` → `ExportSnapshot` errorra).
  - **Replicación** (`capture.go`/`replicate.go`): llaman a `canonicalValue` sin la dimensión (no la pasan y están fuera del perímetro). El valor vector se **canonicaliza fielmente** a `'[c1,c2,...]'` pero **sin cross-check de dimensión** contra el tipo declarado. Cerrar esa brecha exigiría editar `capture.go:225` y `replicate.go:176` para pasar `column.Type.Arguments...` (cambio de 2 líneas), explícitamente fuera del perímetro → se documenta como trade-off, no se aborta, porque el objetivo central (VectorType de primera clase + interop por snapshot) se cumple y la replicación sigue transportando vectores fielmente en forma canónica.
- Forma canónica: `[c1,c2,...,cN]` sin espacios, cada componente vía `normalizeFloat` (reusada). Entrada: `'[1, 2.0, 3]'` (espacios opcionales). Texto que no es lista bracketed de números → error explícito (`'abc'`, `'[1,"x"]'`, `'[1, two, 3]'`). `[1, 2.0, 3]` → `"[1,2,3]"`; `[1.5, 2.5]` se preserva.
- `driverValue`: `VectorValue` cae en el caso default → devuelve el string canónico. En SQLite (TEXT) inserta texto; en PG `vector(N)` pgvector acepta texto `'[1,2,3]'` como input (cast implícito). Sin PG local, el path PG es de compilación/parseo de strings (validación e2e la hace el PM).

### Inspección (`inspect.go`)
- **SQLite**: `sqliteTypeFamily` reconoce `F32_BLOB` **antes** del substring `BLOB` → `VectorType`. La dimensión se extrae del tipo declarado vía nueva helper `parseTypeArguments(declared) []int` (parsea la lista `(N)` entre paréntesis; nil si no hay lista o componente no entero). En `inspectSQLiteTable` (línea 138), si la familia es vector se asigna `Type.Arguments = parseTypeArguments(declaredType)`. `BLOB` sin más sigue → `BinaryType`. Probado con `TestInspectSQLiteF32BlobColumn` (CREATE TABLE con `v F32_BLOB(3)` en modernc → inspecciona `vector` con `Arguments=[3]`, `Exact=true`).
- **Postgres**: la query de columnas de `inspectPostgres` se amplió para traer `udt_name` y `atttypmod` (además de `data_type`). Nueva helper `postgresType(dataType, udtName string, atttypmod int) Type`: si `udt_name == "vector"` → `VectorType` con `Arguments=[atttypmod]` (pgvector almacena la dimensión como typmod directo); `postgresTypeFamily` se reusa para el resto. Si `udt_name == "vector"` pero `atttypmod <= 0` (columna vector sin dimensión, que pgvector permite) → `VectorType` sin args y se emite un **objeto unresolved explícito** (`pgvector vector column has no declared dimension`), nunca familia binary silenciosa. La dimensión PG no es testeable sin Postgres real; los tests son de strings/compilación (`TestSQLiteAndPostgresTypeFamilyForVector` cubre `postgresType` con `atttypmod=3` y `atttypmod=-1`).

### Features (`features.go`)
- Nueva `CanonicalVectors Feature = "canonical_vectors"`. `assess` → `Exact` (agregada al primer case). `InferFeatures` la emite cuando el schema tiene al menos una columna `VectorType` (case en el switch existente por familia de columna). Probado con `TestInferFeaturesVector` (con vector → incluye; sin vector → no incluye; `assess` → `Exact`).

### Docs (`docs/COMPATIBILITY.md`)
- `canonical_vectors` agregada a la lista de capacidades canónicas exactas.
- Fila Vector en la tabla de familias y en la tabla de representación de valores: canónico `vector(N)` → SQLite `TEXT`, Postgres `vector(N)`, carrier `'[1,2,3]'`.

## Trade-offs / límites declarados
1. **Cross-check de dimensión en replicación**: no se aplica (capture/replicate fuera del perímetro y no pasan la dimensión). Snapshot sí lo aplica. Cerrar la brecha = 2 líneas en `capture.go:225` + `replicate.go:176` (fuera del perímetro).
2. **Dimensión PG vía `atttypmod`**: pgvector guarda la dimensión como typmod directo; se asume `atttypmod == N` para `vector(N)`. Validación e2e contra PG real la hace el PM (no hay PG en esta máquina). El caso `atttypmod <= 0` se resuelve como unresolved explícito.
3. **Carrier de texto**: válido para el engine SQLite por defecto (modernc, sin funciones vectoriales) y para pgvector (cast texto↔vector). No habilita índices ANN ni funciones de distancia nativas a través de `compat` (fuera de alcance, consistente con `VECTOR-COMPAT-REPORT.md`).

## Definición de hecho — salida real

### `go build ./... && go vet ./... && go test ./...`

```
$ go build ./... && go vet ./... && echo "=== BUILD+VET OK ===" && go test ./... -count=1
=== BUILD+VET OK ===
?	example.com/sqlite-postgres-compat/cmd/compat-audit	[no test files]
?	example.com/sqlite-postgres-compat/cmd/compat-copy	[no test files]
ok	example.com/sqlite-postgres-compat/compat	2.286s
```

### Tests nuevos (verboso)

```
$ go test ./compat/ -count=1 -v -run 'Vector|InspectSQLiteF32|InferFeaturesVector|SchemaValidatesVector|CompileDDLVector|CanonicalVectorValue|SQLiteAndPostgresTypeFamily'
=== RUN   TestSchemaValidatesVectorDimension
--- PASS: TestSchemaValidatesVectorDimension (0.00s)
=== RUN   TestCompileDDLVectorForBothEngines
--- PASS: TestCompileDDLVectorForBothEngines (0.00s)
=== RUN   TestCanonicalVectorValue
--- PASS: TestCanonicalVectorValue (0.00s)
=== RUN   TestSQLiteAndPostgresTypeFamilyForVector
--- PASS: TestSQLiteAndPostgresTypeFamilyForVector (0.00s)
=== RUN   TestSQLiteVectorSnapshotRoundTrip
--- PASS: TestSQLiteVectorSnapshotRoundTrip (0.00s)
=== RUN   TestSQLiteVectorSnapshotRejectsDimensionMismatch
--- PASS: TestSQLiteVectorSnapshotRejectsDimensionMismatch (0.00s)
=== RUN   TestInspectSQLiteF32BlobColumn
--- PASS: TestInspectSQLiteF32BlobColumn (0.00s)
=== RUN   TestInferFeaturesVector
--- PASS: TestInferFeaturesVector (0.00s)
PASS
ok	example.com/sqlite-postgres-compat/compat	2.341s
```

Cobertura de la definición de hecho:
- (a) `TestSchemaValidatesVectorDimension`: acepta `[3]`; rechaza sin args, 2 args, `[0]`, `[-1]`.
- (b) `TestCompileDDLVectorForBothEngines`: assert del string DDL completo — SQLite `CREATE TABLE "vecs" ("id" INTEGER NOT NULL, "v" TEXT, PRIMARY KEY ("id"))`; Postgres `CREATE TABLE "vecs" ("id" BIGINT NOT NULL, "v" vector(3), PRIMARY KEY ("id"))`.
- (c) `TestCanonicalVectorValue`: `'[1, 2.0, 3]'` → `"[1,2,3]"`; `'[1.5, 2.5]'` preservado; `'abc'`, `'[1,"x"]'`, `'[1, two, 3]'` → error; dimensión declarada 3 con 2 componentes → error (validado en `exportTable`, probado además con `TestSQLiteVectorSnapshotRejectsDimensionMismatch` a nivel snapshot).
- (d) `TestSQLiteAndPostgresTypeFamilyForVector`: `sqliteTypeFamily("F32_BLOB(3)")` → vector con dim 3 (`parseTypeArguments`); `sqliteTypeFamily("BLOB")` → binary; `postgresType` del tipo vector → vector con `[3]`.
- (e) `TestSQLiteVectorSnapshotRoundTrip`: round-trip SQLite→SQLite (modernc, sin red) `ExportSnapshot`/`ImportSnapshot`/`VerifySnapshots` equivalente.
- (f) `TestInferFeaturesVector`: con columna vector incluye `canonical_vectors`; sin columna vector no la incluye.

### `grep -n "canonical_vectors" docs/COMPATIBILITY.md`

```
$ grep -n "canonical_vectors" docs/COMPATIBILITY.md
38:- `canonical_vectors`.
```

Tests preexistentes: no modificados ni borrados. `go build`, `go vet`, `go test ./...` verdes.