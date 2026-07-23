# FEAT-CUBOA-4 — Índices de expresión

**Fecha:** 2026-07-23
**Ámbito:** permitir que una clave de índice sea una EXPRESIÓN de la gramática canónica (Sección 3 de AGENTS.md), no sólo una columna, en compilación (SQLite/PostgreSQL) e inspección nativa, con equivalencia verificada contra PostgreSQL 17 real.
**Motor de PostgreSQL usado para verificación:** PostgreSQL 17 en `postgres://postgres:***@31.220.22.176:5434/postgres?sslmode=disable` (password enmascarada). Base efímera `compat_e2e_%` creada y **dropeada** por el harness (`TestMain`); no se crearon bases con prefijo `cuboa4_` (los objetos de prueba son tablas dentro de la base efímera). Verificado **0 bases** `compat_e2e_%`/`cuboa%` remanentes al terminar.
**Archivos tocados:** `compat/schema.go`, `compat/schema_test.go`, `compat/ddl.go`, `compat/ddl_test.go`, `compat/inspect.go`, `compat/inspect_test.go`, `e2e/system_test.go`, `README.md`, `docs/TESTING.md`, `docs/COMPATIBILITY.md`, `AGENTS.md`, este reporte. No se tocó `compat/sqlparse.go`, `sqlselect.go`, `store.go`, `capture.go`, `replicate.go`, `cmd/**`, ni reportes existentes.

---

## 1. Resumen ejecutivo

| Capacidad | Estado | Evidencia |
|---|---|---|
| Compilar clave de expresión `(expr)` en SQLite y PG (función, operador, único, ASC/DESC, mixto columna+expresión) | **ENTRA** — SQL byte-idéntico entre motores | §3.2, unit congelado |
| Validación: rechaza expresión fuera de gramática; rechaza clave con columna+expresión a la vez | **ENTRA** | §3.2 |
| Round-trip vía metadata canónica (`__compat_schema`) | **ENTRA** — exacto | §3.3 |
| Inspección externa SQLite (recuperar expresión del `CREATE INDEX`) | **ENTRA** — exacto | §3.4, §4 |
| Inspección externa PostgreSQL (recuperar expresión de `pg_get_indexdef`) | **ENTRA cuando el deparse cae en la gramática**; **Unresolved honesto** cuando PG reescribe (p.ej. `::text` en un literal) | §3.4, §4 (cláusula de honestidad) |

Regla de oro respetada: sólo se reconstruye un AST cuando el deparse del motor cae dentro de la gramática canónica; cuando el motor reescribe la expresión a una forma no parseable, el índice queda en `Inspection.Unresolved` (`Exact=false`) con `Reason`, nunca un AST equivocado.

---

## 2. Diseño

### 2.1 Modelo (`compat/schema.go`)

`IndexColumn` gana un campo aditivo `omitempty`:

```go
type IndexColumn struct {
	Column     string      `json:"column,omitempty"`
	Descending bool        `json:"descending,omitempty"`
	Expression *Expression `json:"expression,omitempty"`
}
```

- Si `Expression != nil`, la clave es esa expresión compilada entre paréntesis; `Column` queda vacío. `ASC`/`DESC` sigue aplicando.
- El caso solo-columna es **byte-idéntico** al anterior: `Column` no vacío siempre serializa `"column":"x"`; `Expression` omitido cuando es nil.

### 2.2 Compilación (`compat/ddl.go`, `compileIndex`)

Por cada clave: si `Expression != nil`, `"(" + compileExpression(engine, expr) + ")"` (los paréntesis los exigen ambos motores para una clave de expresión: SQLite ≥ 3.9, PostgreSQL) `[+ " ASC"|" DESC"]`; si no, el camino actual `quoteIdentifier(Column)`. Mezcla de columnas y expresiones en el mismo índice permitida. La validez gramatical la impone `compileExpression` (misma ruta que columnas generadas y `CHECK`): una expresión fuera de la Sección 3 se rechaza con error claro.

### 2.3 Validación (`Schema.Validate` → `validateIndexes`)

Una clave con `Expression != nil` no exige nombre de columna (la gramática se verifica al compilar). Una clave que fija `Column` **y** `Expression` a la vez se rechaza (`index %q key has both a column and an expression`). Índices de expresión únicos permitidos.

### 2.4 Inspección nativa (`compat/inspect.go`)

- **SQLite** (`inspectSQLiteIndexes`): una clave de expresión aparece en `pragma_index_xinfo` con `cid<0` y `name` NULL. El texto de la expresión sólo vive en el `CREATE INDEX` de `sqlite_master`. Se extrae la lista de claves de nivel superior (`extractIndexKeyList`, partida por comas de nivel superior), se alinea posicionalmente con las filas `key=1` de `xinfo`, se le quita el `ASC`/`DESC` final (`stripIndexKeyOrdering`, la dirección la da `xinfo`) y se parsea con `parseCatalogExpression`.
- **PostgreSQL** (`inspectPostgresIndexes`): por cada columna clave, `pg_get_indexdef(oid, pos, true)`. Si es un identificador simple → columna (comportamiento previo intacto). Si no → se intenta `parseCatalogExpression`; si parsea, clave de expresión; si no (deparse fuera de gramática), el índice cae a `Unresolved`.

En ambos motores, si cualquier clave no parsea, el índice entero queda `Unresolved` con `Reason` — nunca un AST parcial o equivocado.

---

## 3. Verificación (salida real)

### 3.1 `.\scripts\check.ps1` (gofmt + vet + unit) — VERDE

```
gofmt: OK
go vet: OK
go test: OK
?   	example.com/sqlite-postgres-compat/cmd/compat	[no test files]
ok  	example.com/sqlite-postgres-compat/cmd/internal/cliout	2.384s
ok  	example.com/sqlite-postgres-compat/compat	2.295s
```

`go vet -tags=e2e ./e2e` → sin salida (VERDE, exit 0).

### 3.2 Unit congelados: compile + validación

`TestCompileExpressionIndexForBothEngines` — SQL exacto, **idéntico en SQLite y PostgreSQL**:

| Caso | SQL emitido (ambos motores) |
|---|---|
| función | `CREATE INDEX "users_lower_email" ON "users" ((LOWER("email")) ASC)` |
| operador + DESC | `CREATE INDEX "users_sum_desc" ON "users" ((("a" + "b")) DESC)` |
| único (función) | `CREATE UNIQUE INDEX "users_ulower" ON "users" ((LOWER("email")) ASC)` |
| mixto columna+expresión | `CREATE INDEX "users_mixed" ON "users" ("a" ASC, (LOWER("email")) ASC, "b" DESC)` |

`TestCompileExpressionIndexRejectsOutOfGrammar` — una clave con una función fuera de la allowlist (`substr`) es rechazada por `CompileDDL` en ambos motores.
`TestSchemaValidateExpressionIndex` — clave de expresión válida pasa `Validate`; clave con columna+expresión a la vez se rechaza.

```
--- PASS: TestCompileExpressionIndexForBothEngines (0.00s)
--- PASS: TestCompileExpressionIndexRejectsOutOfGrammar (0.00s)
--- PASS: TestSchemaValidateExpressionIndex (0.00s)
```

### 3.3 Round-trip vía metadata canónica (`__compat_schema`) — exacto

`TestInspectCanonicalMetadataExpressionIndexRoundTrip` — `ApplySchema` con un índice de expresión y uno mixto único; `InspectSchema` devuelve `Source=canonical_metadata` y `reflect.DeepEqual(schema, inspection.Schema)`.

```
--- PASS: TestInspectCanonicalMetadataExpressionIndexRoundTrip (0.00s)
```

### 3.4 Inspección externa SQLite (unit) — exacto

`TestInspectExternalSQLiteExpressionIndex` — índices creados por SQL crudo (sin `__compat_schema`), `InspectSchema` → `Source=sqlite_catalog`, `Exact=true`, reconstruye `lower(email)`, `a + b DESC`, único, y mixto. Una función fuera de allowlist (`substr(email,1,2)`) queda `Unresolved` (`Exact=false`).

```
--- PASS: TestInspectExternalSQLiteExpressionIndex (0.00s)
```

Salida real de la inspección SQLite (probe, ya borrado) confirmando la reconstrucción:

```
IDX {"name":"i_lower","table":"t","columns":[{"expression":{"kind":"lower","args":[{"kind":"column","value":"email"}]}}]}
IDX {"name":"i_expr_desc","table":"t","columns":[{"descending":true,"expression":{"kind":"add","args":[{"kind":"column","value":"a"},{"kind":"column","value":"b"}]}}]}
IDX {"name":"i_uniq","table":"t","unique":true,"columns":[{"expression":{"kind":"lower",...}}]}
IDX {"name":"i_mixed","table":"t","columns":[{"column":"a"},{"expression":{"kind":"lower",...}},{"column":"b","descending":true}]}
IDX {"name":"i_coalesce","table":"t","columns":[{"expression":{"kind":"coalesce","args":[{"kind":"column","value":"email"},{"kind":"string","value":"x"}]}}]}
```

### 3.5 EQUIVALENCIA / ROUND-TRIP contra PostgreSQL 17 REAL (E2E)

`TestSystemExpressionIndexNativeInspectionRoundTrip` (tag `e2e`, `e2e/system_test.go`). Crea la misma tabla + índices por SQL crudo en **SQLite real** y **PostgreSQL 17 real** (recreando `public` para forzar el camino de catálogo nativo, no metadata), e inspecciona nativamente:

- `CREATE INDEX cuboa4_lower ON cuboa4_expr_idx (lower(email))` → reconstruido **exacto en ambos** como `{Expression: lower(column email)}`.
- `CREATE INDEX cuboa4_coalesce ON cuboa4_expr_idx (coalesce(email, 'x'))` → **SQLite exacto** (guarda el texto del `CREATE INDEX` verbatim); **PostgreSQL Unresolved** (deparse reescribe el literal con `::text`, ver §4).

```
=== RUN   TestSystemExpressionIndexNativeInspectionRoundTrip
--- PASS: TestSystemExpressionIndexNativeInspectionRoundTrip (1.61s)
```

Suite E2E completa contra PG real (`go test -tags=e2e ./e2e -count=1`): **46 pruebas** de nivel superior, **45 superadas** y **1 fallida de forma intencional** (`TestSystemClaimsExactCoverageForRequiredFeatureFamilies`, el gate del 100 % rojo por diseño). Sin ninguna otra falla. **0 bases temporales remanentes** en PostgreSQL al terminar (verificado por consulta a `pg_database`).

---

## 4. Cláusula de honestidad — evidencia del deparse REAL (PostgreSQL 17)

Salida real de `pg_get_indexdef` (por columna clave) contra el DSN dado (probe borrado):

```
PG INDEX zz_lower      col[1] desc=false => "lower(email)"                 -> parsea -> EXACTO
PG INDEX zz_expr_desc  col[1] desc=true  => "(a + b)"                      -> parsea -> EXACTO (DESC vía indoption)
PG INDEX zz_mixed      col[1] => "a"  col[2] => "lower(email)"  col[3] desc => "b"  -> EXACTO
PG INDEX zz_uniq       col[1] => "lower(email)"                            -> parsea -> EXACTO
PG INDEX zz_coalesce   col[1] => "COALESCE(email, 'x'::text)"             -> NO parsea (::text) -> UNRESOLVED
```

`lower(email)`, `(a + b)` y las claves mixtas caen dentro de la gramática canónica y se reconstruyen exacto. En cambio PostgreSQL **reescribe** el literal `'x'` de `coalesce(email, 'x')` a `'x'::text` al deparsearlo; el cast `::type` está fuera de la gramática (`parseCatalogExpression` lo rechaza), así que ese índice cae honestamente a `Unresolved`:

```
UNRESOLVED kind=index name=cuboa4_coalesce reason=index method, expression, collation, operator class or predicate is outside the canonical grammar
```

Asimetría documentada: el **mismo** índice `coalesce(email, 'x')` reconstruye **exacto en SQLite** (que guarda el `CREATE INDEX` textual) y **Unresolved en PostgreSQL** (que lo reescribe). No se fabrica un AST equivocado en ningún caso. La ruta canónica (`__compat_schema`) round-tripea exacto para ambos motores independientemente de esto (§3.3).

---

## 5. Trade-offs y notas

- **Nativo `(expr)` y no desugar.** La expresión se emite tal cual entre paréntesis; ambos motores exigen los paréntesis para una clave de expresión, y el resultado es SQL idéntico entre motores para las construcciones de la gramática (funciones/operadores). El `LIKE→ILIKE` heredado sigue aplicando si una clave contiene `LIKE` (una clave de expresión con `LIKE` diverge en el texto pero es semánticamente equivalente, igual que en `CHECK`/vistas).
- **Alineación posicional en SQLite.** `pragma_index_xinfo` da las claves en orden de declaración; la lista del `CREATE INDEX` partida por comas de nivel superior se alinea 1:1. Una clave con nombre usa `xinfo`; una clave de expresión toma el término posicional correspondiente. `COLLATE` en una clave de expresión se deja intacto a propósito: `parseCatalogExpression` lo rechaza y el índice cae a `Unresolved` en vez de descartar una colación no-default en silencio.
- **PG: columna vs expresión sin ambigüedad.** Se intenta primero `parseCatalogIdentifier` (columna simple, camino previo intacto); sólo si falla se intenta la expresión. Una columna cualificada con esquema sigue siendo inválida (como antes).
- **Sin cambios de firma pública ni de `sqlparse.go`.** Se reutiliza `parseCatalogExpression`/`compileExpression` y los splitters existentes; los helpers nuevos (`extractIndexKeyList`, `indexKeyListStart`, `stripIndexKeyOrdering`) son privados de `inspect.go`.

---

## 6. Limpieza

- Probes temporales (`compat/zz_probe_test.go`, `e2e/zz_probe_test.go`, `chkdb_tmp.go`): **borrados**. `git status` sólo muestra los archivos permitidos modificados + este reporte.
- Sin procesos en foreground que no terminen solos. Nada escrito fuera del repo. Password del DSN nunca en literal (enmascarada `***`). **0 bases** `compat_e2e_%`/`cuboa%` remanentes en PostgreSQL.
