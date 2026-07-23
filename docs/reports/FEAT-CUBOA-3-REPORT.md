# FEAT-CUBOA-3 — Columnas generadas STORED con expresión canónica

**Repo:** `sqlite-postgres-compat`, rama `main`.
**Objetivo:** soportar `col TIPO GENERATED ALWAYS AS (<expr>) STORED` (idéntico en SQLite ≥ 3.31 y PostgreSQL ≥ 12), de forma **aditiva** y sin alterar el comportamiento de la cadena de fidelidad (marcador `\x01real`, reconciliación decimal/date, supresión de ecos).
**Motor:** `go1.26.4 windows/amd64`, `modernc.org/sqlite`, PostgreSQL **17.10** real (DSN con password SIEMPRE `***`).
**Archivos tocados:** `compat/schema.go`, `compat/schema_test.go`, `compat/ddl.go`, `compat/ddl_test.go`, `compat/inspect.go`, `compat/inspect_test.go`, `compat/store.go`, `compat/replicate.go`, `compat/replicate_test.go`, `e2e/system_test.go`, `README.md`, `docs/TESTING.md`, `docs/COMPATIBILITY.md`, `AGENTS.md`, este reporte. (No se tocó `store_test.go`/`capture_test.go`: no hicieron falta cambios ahí.)

---

## 1. Diseño

### Modelo (aditivo)

`Column` gana un campo `omitempty`:

```go
Generated *GeneratedColumn `json:"generated,omitempty"`

type GeneratedColumn struct {
    Expression Expression `json:"expression"`
    Stored     bool       `json:"stored"`
}
```

Una columna sin `Generated` deja **todo byte-idéntico** (JSON con `omitempty`, y ningún cambio en el DDL emitido ni en la cadena de datos). Se usó un sub-struct con `Stored` explícito en vez de un `*Expression` pelado precisamente para poder **rechazar VIRTUAL** en validación: `Stored=false` es la única forma de modelar VIRTUAL en el AST y se rechaza; VIRTUAL nunca se emite.

### Compilación (`compat/ddl.go`, `compileTable`)

Rama aditiva: si `column.Generated != nil` se emite `GENERATED ALWAYS AS (<expr>) STORED` (sintaxis idéntica en ambos motores) en lugar de `DEFAULT`. La expresión pasa por el mismo `compileExpression` que cualquier otra, así que una expresión fuera de la gramática falla en compilación con error explícito. SQL exacto congelado (test `TestCompileGeneratedStoredColumnForBothEngines`):

```
SQLite : "total" INTEGER NOT NULL GENERATED ALWAYS AS (("price" * "quantity")) STORED
Postgres: "total" BIGINT  NOT NULL GENERATED ALWAYS AS (("price" * "quantity")) STORED
```

### Validación (`Schema.Validate`)

Tres rechazos canónicos (ambos motores los imponen):

- **VIRTUAL** → `Generated.Stored=false` rechazado (`… must be STORED (VIRTUAL is not supported)`).
- **Generada + Default** a la vez → rechazado.
- **Generada dentro de una PRIMARY KEY** → rechazado.

### Inspección nativa (`compat/inspect.go`)

Antes, toda columna generada iba a `Unresolved` (bloqueaba `Exact`). Ahora:

- **SQLite**: `pragma_table_xinfo.hidden = 3` (STORED) → se reconstruye la columna generada; la expresión no la expone el pragma, así que se recupera del texto del `CREATE TABLE` con un extractor nuevo (`extractGeneratedExpression` + helpers `leadingIdentifier`/`indexWord`, reutilizando `splitTopLevelCommas`/`matchingParenthesis`/`wordBoundary` ya existentes) y se parsea con `parseCatalogExpression`. `hidden = 2` (VIRTUAL) y `hidden = 1` (hidden) siguen en `Unresolved`.
- **PostgreSQL**: se añadió `c.generation_expression` al SELECT de `information_schema.columns`. `is_generated='ALWAYS'` → se reconstruye desde `generation_expression` vía `parsePostgresCatalogDefault` (quita casts/paréntesis del deparser). `is_identity='YES'` sigue en `Unresolved`.

Cualquier expresión que no parsee queda `Unresolved` (nunca se degrada en silencio).

## 2. Cómo se excluyen las generadas del INSERT/apply (lo crítico)

Ambos motores **fallan** si se intenta escribir una columna generada. Los dos únicos puntos de escritura se hicieron saltar la columna generada, de forma aditiva:

- **`insertRow` (`compat/store.go`)** — usado por `ImportSnapshot` y por el apply de INSERT en replicación: `if column.Generated != nil { continue }`. Además los placeholders pasaron a numerarse por orden de inserción (`len(arguments)+1`) en vez del índice crudo, para que al saltar una columna no queden huecos en la secuencia `$N` de PostgreSQL.
- **`updateRow` (`compat/replicate.go`)** — apply de UPDATE: `if column.Generated != nil { continue }` en el `SET` (el contador `position` sólo avanza en columnas realmente asignadas).

`loadRow` y las filas capturadas **sí** incluyen el valor generado (lectura), en ambos lados de la comparación `rowsEqual`: fila `Before`/`After` del journal (valor computado por la fuente) vs. fila viva del destino (valor computado por el destino). Con expresión determinista e idéntica convergen, así que **no hay `ConflictError` espurio**. Esa recomputación idéntica en el destino es la prueba de equivalencia.

## 3. No-regresión de la cadena de fidelidad

- El marcador `\x01real`, `canonicalValue`, `normalizeFloat`, `cutRealDecimalMarker` y los triggers de captura **no se tocaron**. El único cambio en la ruta de datos es el `continue` sobre columnas generadas en `insertRow`/`updateRow` (puramente aditivo) y la numeración de placeholders por orden (equivalente cuando no hay generadas: `len(arguments)+1 == index+1`).
- Suite de fidelidad completa **verde sin cambios de comportamiento**: `store_test.go`, `capture_test.go`, `replicate_test.go` (incluidos los tests del marcador, reconciliación decimal REAL/TEXT, Inf/NaN, conflicto de INSERT duplicado y supresión de ecos) pasan intactos. No fue necesario alterar ningún assert existente (a diferencia de FIX-R1, aquí el cambio no mueve ninguna semántica previa).

## 4. Verificación (salida real)

### 4.1 Unit congelados (`go test ./compat/`)

```
=== RUN   TestCompileGeneratedStoredColumnForBothEngines
--- PASS: TestCompileGeneratedStoredColumnForBothEngines (0.00s)
=== RUN   TestInspectExternalSQLiteGeneratedStoredColumn
--- PASS: TestInspectExternalSQLiteGeneratedStoredColumn (0.00s)
=== RUN   TestApplyChangesRecomputesGeneratedStoredColumnEndToEnd
--- PASS: TestApplyChangesRecomputesGeneratedStoredColumnEndToEnd (0.00s)
=== RUN   TestSchemaValidateAcceptsGeneratedStoredColumn
--- PASS: TestSchemaValidateAcceptsGeneratedStoredColumn (0.00s)
=== RUN   TestSchemaValidateRejectsInvalidGeneratedColumns
    --- PASS: .../virtual (0.00s)
    --- PASS: .../generated_with_default (0.00s)
    --- PASS: .../generated_in_primary_key (0.00s)
PASS
ok  	example.com/sqlite-postgres-compat/compat	2.700s
```

Cubre: compile STORED en SQLite y PG (SQL exacto); rechazo de VIRTUAL, generada+default y generada en PK; round-trip de inspección nativa SQLite (STORED → exact con expresión reconstruida; VIRTUAL → unresolved).

### 4.2 Equivalencia contra PostgreSQL 17.10 real

DSN: `postgres://postgres:***@31.220.22.176:5434/postgres?sslmode=disable`.

```
$ go test ./e2e/ -tags e2e -run 'TestSystemGeneratedStoredColumn' -v
=== RUN   TestSystemGeneratedStoredColumnSnapshotCopyEquivalent
--- PASS: TestSystemGeneratedStoredColumnSnapshotCopyEquivalent (1.29s)
=== RUN   TestSystemGeneratedStoredColumnIncrementalReplication
--- PASS: TestSystemGeneratedStoredColumnIncrementalReplication (2.47s)
=== RUN   TestSystemGeneratedStoredColumnNativeInspectionRoundTrip
--- PASS: TestSystemGeneratedStoredColumnNativeInspectionRoundTrip (1.55s)
PASS
ok  	example.com/sqlite-postgres-compat/e2e	8.588s
```

- **(a) snapshot copy** SQLite → PG real de una tabla con columna generada STORED → `equivalent=true`; spot-check en PG: `total = 42` (recomputado por PG desde `price*quantity`, el INSERT nunca escribió la columna).
- **(b) replicación incremental** (`ApplyChanges` estricto, INSERT + UPDATE capturados en SQLite) → PG recomputó `total=54` tras el UPDATE **sin conflicto espurio** (el `before` cross-engine con el valor generado por la fuente coincide con la recomputación de PG); snapshots equivalentes.
- **(c) inspección nativa** en ambos motores (DDL crudo, sin metadata canónica) → `Exact`, expresión de generación reconstruida (`mul`).

### 4.3 Suite completa

```
$ ./scripts/check.ps1
gofmt: OK
go vet: OK
go test: OK
ok  	example.com/sqlite-postgres-compat/compat	2.397s

$ go vet -tags=e2e ./e2e   → OK

$ go test ./e2e/ -tags e2e -count=1
```

La e2e completa corre verde **excepto** el fallo intencional preexistente `TestSystemClaimsExactCoverageForRequiredFeatureFamilies` (documenta que las familias genéricas no-canónicas siguen `unknown`); ningún otro test falla.

### 4.4 Conteo de tests e2e

3 tests e2e nuevos de nivel superior. Conteo verificado:

```
$ grep -rhE "^func Test[A-Z]" e2e/*.go | grep -v TestMain | wc -l
45
```

Antes 42 → ahora **45** (44 superadas + 1 fallida intencional). Actualizado en `README.md` y `docs/TESTING.md`.

## 5. Limpieza

Las tablas de prueba usan prefijo `cuboa3_` y se dropean vía `t.Cleanup`; todas viven dentro de la base efímera `compat_e2e_<ns>` que `TestMain` crea y **dropea** al terminar (con `pg_terminate_backend` previo). No queda ninguna base ni tabla temporal en el servidor. No se hizo `git add/commit/stash`; ningún proceso en foreground; nada fuera del repo; password nunca literal.

## 6. Docs

- `AGENTS.md` §1: subsección "Generated columns (STORED)" (modelo, reglas, inspección) y §7: VIRTUAL/generada+default/generada-en-PK añadidos a "Always rejected".
- `docs/COMPATIBILITY.md`: fila "Tablas y columnas" ampliada + nueva sección "Columnas generadas" (STORED soportada, VIRTUAL rechazada, exclusión del INSERT/UPDATE como prueba de equivalencia).
