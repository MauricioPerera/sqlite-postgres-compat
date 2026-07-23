# FEAT-CUBOA-2B — Tablas derivadas (FROM-subquery) y CTE no recursivas en el cuerpo de las vistas

**Fecha:** 2026-07-23
**Ámbito:** extender la gramática canónica de vistas (`compat/sqlselect.go` parse → `compat/ddl.go` compile) con (1) subconsulta como origen en `FROM` (tabla derivada) y (2) CTE **no recursivas** (`WITH … AS (…)`), cada una verificada equivalente en SQLite (modernc) **y** PostgreSQL 17 real.
**Motor de PostgreSQL usado para verificación:** PostgreSQL 17 en `postgres://postgres:***@31.220.22.176:5434/postgres` (password enmascarada). Bases efímeras creadas y **dropeadas** por el harness E2E (`TestMain`, prefijo `compat_e2e_`); verificado **0** bases remanentes al terminar (query directa a `pg_database`, ver §6).
**Archivos tocados:** `compat/schema.go`, `compat/sqlselect.go`, `compat/sqlselect_test.go`, `compat/ddl.go`, `compat/ddl_test.go`, `e2e/system_test.go`, `README.md`, `docs/TESTING.md`, `docs/COMPATIBILITY.md`, `AGENTS.md`, este reporte. **No** se tocó `compat/sqlparse.go`, `store.go`, `capture.go`, `cmd/**`, ni reportes existentes. **No** se tocó `compat/inspect.go`: el round-trip de inspección funciona reusando `parseCatalogSelect` sin cambios en el inspector.

---

## 1. Resumen ejecutivo

| Constructo | Decisión | Motivo |
|---|---|---|
| **1. Tabla derivada** `FROM (SELECT …) alias` | **ENTRA** | Una tabla derivada no es correlacionable con la consulta externa en SQL estándar → ambos motores la materializan idéntica. Alias obligatorio (regla de PG). |
| **2. CTE no recursiva** `WITH a AS (…), b AS (…) SELECT …` | **ENTRA** | Semántica de resultado materializado y de sombreado de nombres idéntica en ambos motores. |
| `WITH RECURSIVE` | **RECHAZADA** (→ Unresolved, `Exact=false`) | Terminación y orden observable de la recursión no garantizados byte-idénticos entre motores. Rechazo explícito. |
| Tabla derivada **sin alias** | **RECHAZADA** | PG exige alias; exigirlo mantiene la forma canónica exacta para ambos (SQLite lo aceptaría, PG no). |
| **3. Subconsulta no correlacionada en `IN`** (`x IN (SELECT …)`) | **DIFERIDA** (no bloqueo) | El predicado `IN` se parsea en `compat/sqlparse.go` (`parseCatalogIn`), archivo **fuera de alcance** de esta tarea. No se puede implementar sin tocarlo. Ver §4. |
| Subconsulta correlacionada / `EXISTS` | **RECHAZADA** por diseño | Correlación = riesgo alto; fuera de este lote (y también dependería de `sqlparse.go`). |

**Entraron los dos constructos de prioridad alta (tabla derivada y CTE no recursiva).** El opcional (subconsulta en `IN`) se **difiere con evidencia concreta**: su parser vive en un archivo prohibido para esta tarea. Regla de oro respetada: sólo se emite SQL que se pudo verificar equivalente contra PG real; lo que divergiría o no se puede probar se rechaza/difiere honestamente, nunca se acepta en silencio.

---

## 2. Diseño (aditivo, opt-in por presencia de campo)

### 2.1 Modelo (`compat/schema.go`)

Dos cambios aditivos, ambos `omitempty` para dejar los snapshots de vistas existentes **byte-idénticos**:

```go
// TableSource gana un tercer campo; exactamente uno de Table o Subquery se usa.
type TableSource struct {
    Table    string       `json:"table,omitempty"`
    Alias    string       `json:"alias,omitempty"`
    Subquery *SelectQuery `json:"subquery,omitempty"`
}

// SelectQuery gana la lista de CTEs, delante de todo.
type SelectQuery struct {
    With []CommonTableExpr `json:"with,omitempty"`
    // … (Distinct, Columns, From, Joins, Where, GroupBy, Having, Compounds, OrderBy, Limit, Offset sin cambios)
}

type CommonTableExpr struct {
    Name  string      `json:"name"`
    Query SelectQuery `json:"query"`
}
```

- `Table` pasó de `json:"table"` a `json:"table,omitempty"`: para un origen de tabla normal `Table` es no vacío, así que sigue serializándose igual; sólo se omite cuando el origen es una subconsulta (donde `Table==""`). Snapshots existentes intactos.
- La subconsulta y cada CTE son un `SelectQuery` de la **misma** gramática (recursión total): pueden a su vez llevar joins, agregación, compounds, otra tabla derivada u otra CTE.

### 2.2 Parseo (`compat/sqlselect.go`)

- **`parseCatalogSelect`** ahora: `stripCatalogViewPrefix` → `stripCatalogWith` (extrae CTEs) → `parseCatalogSelectBody` (la lógica de compounds anterior, extraída sin cambios de comportamiento) → adjunta `With`.
- **`stripCatalogViewPrefix`**: el límite `AS` de la vista puede ir seguido de `SELECT` **o** `WITH` (una vista con CTE). Se toma el primer match top-level de `AS SELECT`/`AS WITH` (`earliestKeyword`); el `AS (` interno de una CTE nunca colisiona con ninguno de los dos.
- **`stripCatalogWith`**: reconoce `WITH name AS ( query ) [, …] <mainquery>`, respetando comillas y profundidad de paréntesis (`matchingParenthesis`). Rechaza `WITH RECURSIVE` explícitamente. Cada cuerpo de CTE se parsea recursivamente con `parseCatalogSelect`.
- **`parseCatalogTableSource`**: si el texto empieza por `(`, delega en `parseCatalogSubquerySource`, que cierra el paréntesis (`matchingParenthesis`), parsea el interior con `parseCatalogSelect`, y exige un alias simple (`[AS] alias`). El resto del camino (tabla simple, alias, rechazo de fuentes compuestas con coma) queda igual.

### 2.3 Compilación (`compat/ddl.go`)

- **`compileSelect`** antepone `compileWith(engine, query.With)` (`WITH "a" AS (…), "b" AS (…) `) cuando hay CTEs. Sin CTEs el prefijo es vacío → salida byte-idéntica a la anterior.
- **`compileTableSource`** ahora recibe `engine` y devuelve `error`; si `Subquery != nil` emite `(` + `compileSelect(engine, sub)` + `) AS "alias"`. Reutiliza toda la maquinaria de compilación recursivamente (incluido el mapeo por motor: p.ej. `LIKE`→`ILIKE` dentro de la subconsulta).
- **`compileSelectCore`** admite `From.Subquery != nil` como origen válido (además de `From.Table != ""`).

El keyword y la sintaxis de `WITH` y de `( subquery ) AS alias` son **idénticos** en SQLite y PostgreSQL, por eso el SQL emitido es el mismo string para ambos motores.

---

## 3. Verificación

### 3.1 `.\scripts\check.ps1` (gofmt + vet + unit) — VERDE

```
gofmt: OK
go vet: OK
?   	example.com/sqlite-postgres-compat/cmd/compat	[no test files]
ok  	example.com/sqlite-postgres-compat/cmd/internal/cliout	2.523s
ok  	example.com/sqlite-postgres-compat/compat	2.430s
go test: OK
```

`go vet -tags=e2e ./e2e` → `E2E VET OK` (sin hallazgos).

### 3.2 Tests unitarios congelados (parse + compile en ambos motores)

`compat/sqlselect_test.go` (parse → AST) y `compat/ddl_test.go` (compile → SQL exacto). Salida real (`go test ./compat -run 'CTE|Subquery|FromSubquery|Recursive' -v`):

```
--- PASS: TestParseCatalogSelectFromSubquery
--- PASS: TestParseCatalogSelectFromSubqueryWithoutAlias
--- PASS: TestParseCatalogSelectFromSubqueryAcceptsPostgresDeparse
--- PASS: TestParseCatalogSelectSingleCTE
--- PASS: TestParseCatalogSelectMultipleCTEs
--- PASS: TestParseCatalogSelectCTEAcceptsPostgresDeparse
--- PASS: TestParseCatalogSelectRejectsRecursiveCTE
--- PASS: TestParseCatalogSelectCTEInCreateView
--- PASS: TestParseCatalogSelectCTEOverCompound
--- PASS: TestCompileFromSubqueryForBothEngines
--- PASS: TestCompileCTEForBothEngines
--- PASS: TestCompileMultipleCTEsForBothEngines
--- PASS: TestCompileFromSubqueryKeepsPerEngineExpressionMapping
PASS
ok  	example.com/sqlite-postgres-compat/compat	2.399s
```

SQL exacto asertado (idéntico en SQLite y PostgreSQL):

| Caso | SQL emitido (ambos motores) |
|---|---|
| tabla derivada | `SELECT "s"."id" FROM (SELECT "id" FROM "t" WHERE ("active" = TRUE)) AS "s"` |
| CTE simple | `WITH "a" AS (SELECT "id" FROM "t") SELECT "id" FROM "a"` |
| CTE múltiple + join | `WITH "a" AS (SELECT "id" FROM "t"), "b" AS (SELECT "id" FROM "u") SELECT "a"."id" FROM "a" INNER JOIN "b" ON ("b"."id" = "a"."id")` |

Los tests `…AcceptsPostgresDeparse` congelan además el **deparse normalizado real de PostgreSQL 17** (capturado de `information_schema.views`, con columnas cualificadas, paréntesis y saltos de línea) y verifican que `parseCatalogSelect` lo round-trippea a un AST resuelto — la base de que `canonical_views` siga exact en la inspección nativa.

### 3.3 Equivalencia y round-trip contra PostgreSQL 17 REAL (E2E)

**Test nuevo top-level `TestSystemDerivedTableAndCTEViewsProduceEquivalentResults`** (DSN dado, password `***`). Hace dos cosas contra PG real:

1. **Equivalencia canónica → ambos motores.** Schema canónico con una tabla `sub_items(id, name, active)` y dos vistas: `sub_derived` (tabla derivada `FROM (SELECT … WHERE active) AS s`) y `sub_cte` (`WITH a AS (SELECT … WHERE active) SELECT … FROM a`). `ApplySchema` en SQLite + `ImportSnapshot` en PG real (import verde ⇒ PG acepta el DDL compilado de ambas vistas). Se consulta cada vista en ambos motores (`SELECT id, name FROM v ORDER BY id, name`) y coinciden fila a fila: ambas devuelven exactamente las 2 filas activas `(1,'a'),(3,'c')`.
2. **Round-trip de inspección nativa (canonical_views exact).** Se recrean las **mismas** dos vistas como DDL nativo crudo en un schema **sin metadata canónica** en **ambos** motores (`sub_items` + `CREATE VIEW … FROM (…) AS s` y `… WITH a AS (…)`). `InspectSchema` en cada motor asserta `Exact == true` y `len(Unresolved) == 0`, y que `sub_derived` se reconstruye con `From.Subquery != nil` (alias `s`) y `sub_cte` con `len(With)==1`, `With[0].Name=="a"`, `From.Table=="a"`. Esto prueba que el deparse propio de cada motor (verbatim en SQLite; normalizado en PG) vuelve a un `SelectQuery` resuelto — no cae a un AST equivocado ni a Unresolved.

Ejecutado DE VERDAD:

```
=== RUN   TestSystemInspectsNativeSchemaObjectsWithoutMetadata
--- PASS: TestSystemInspectsNativeSchemaObjectsWithoutMetadata (5.30s)
=== RUN   TestSystemCompoundViewsProduceEquivalentResults
--- PASS: TestSystemCompoundViewsProduceEquivalentResults (2.21s)
=== RUN   TestSystemDerivedTableAndCTEViewsProduceEquivalentResults
--- PASS: TestSystemDerivedTableAndCTEViewsProduceEquivalentResults (3.10s)
PASS
ok  	example.com/sqlite-postgres-compat/e2e	13.886s
```

**Suite E2E completa contra PG real** (`.\scripts\test-system.ps1`):

```
test-system: OK - the only e2e failure is the documented baseline
'TestSystemClaimsExactCoverageForRequiredFeatureFamilies' (docs/reports/AUDIT7-C.md, finding H2).
```

Sólo falla el gate intencional del 100 % (rojo por diseño); ninguna otra. El conteo de pruebas top-level pasó a **43 (42 superadas + 1 fallida intencional)**: +1 test top-level nuevo. (El doc previo decía 41; el conteo real ya committeado era 42 — deriva pre-existente de −1 corregida a la vez.) README.md y docs/TESTING.md actualizados a 43.

---

## 4. Constructo 3 (subconsulta en `IN`) — diferido con evidencia

El opcional pedido era `x IN (SELECT …)` no correlacionado. Su semántica de conjuntos (incluida la lógica de tres valores con `NULL`) **sí** es idéntica entre motores, pero **no se puede implementar en esta tarea** por una razón estructural, no semántica:

- El predicado `IN` se parsea en **`compat/sqlparse.go`** (`parseCatalogIn`, que hoy sólo acepta una **lista de valores** y rechaza subconsultas), archivo que el enunciado marca como **prohibido** (`NO toques compat/sqlparse.go`). El `WHERE` de una vista llega a ese parser vía `parseCatalogExpression`; no hay forma de interceptar la subconsulta desde `sqlselect.go` sin modificar `sqlparse.go`.

Por eso se **difiere** (no es bloqueo: entraron los dos constructos de prioridad alta). Verificación de que sigue rechazado hoy: el probe contra PG mostró que PG deparsea `WHERE (id IN ( SELECT pu.id FROM pu))`, y `parseCatalogExpression`/`parseCatalogIn` lo rechaza (`unsupported catalog expression`), por lo que una vista así cae a `Unresolved` honestamente. Correlacionadas y `EXISTS` quedan fuera por diseño (correlación = riesgo alto).

---

## 5. Riesgos verificados explícitamente

- **Materialización idéntica (mismas filas).** Verificado por el test de equivalencia (§3.3-1): ambas vistas devuelven fila a fila lo mismo en SQLite y PG.
- **Colisión de nombre CTE con tabla real.** Es semántica SQL estándar: la CTE sombrea la tabla homónima durante la consulta, idéntico en ambos motores. No requiere manejo especial (el nombre CTE se cita como cualquier identificador y `FROM "a"` resuelve a la CTE). Documentado en `schema.go`/`AGENTS.md`.
- **CTE referida cero o múltiples veces.** Una CTE no referida se define y se ignora (ambos motores la aceptan); referida múltiples veces se materializa/expande igual en ambos. El compilador emite la lista `WITH` tal cual; no depende del número de referencias.
- **Round-trip de inspección.** Verificado (§3.3-2) que el deparse nativo de cada motor reconstruye el AST correcto y mantiene `Exact`. El deparse de PG (columnas cualificadas, paréntesis, saltos de línea) se congela además en los unit tests `…AcceptsPostgresDeparse`.
- **Tabla derivada correlacionada.** Imposible por construcción: una tabla derivada en `FROM` no es correlacionable con la consulta externa en SQL estándar, así que no hay divergencia posible por correlación.

---

## 6. Limpieza

- Probes temporales contra PG (`e2e/zz_probe_test.go`, `e2e/zz_leftover_test.go`, `e2e/zz_cleanup_test.go`): **borrados**. `git status` sólo muestra los archivos permitidos + este reporte.
- Durante el desarrollo, dos truncados de salida (`Select-Object`) mataron un `go test` antes del cleanup de `TestMain`, dejando 1 base huérfana `compat_e2e_%`; se **dropeó** con un probe efímero y se verificó **0** bases `compat_e2e_%`/`cuboa%` remanentes (query a `pg_database`: `LEFTOVER_DBS=[]`).
- Sin procesos en foreground que no terminen solos. Nada escrito fuera del repo. Sin `git add/commit/stash`. Password del DSN nunca en literal (enmascarada `***`). No se crearon bases con prefijo propio `cuboa2b_` (los tests reusan la base efímera `compat_e2e_%` de `TestMain`).

---

## 7. Trade-offs y notas

- **Alias obligatorio en tabla derivada.** SQLite lo aceptaría sin alias; PG no. Exigirlo mantiene la forma canónica exacta para ambos y da a la tabla derivada un nombre canónico estable. Una tabla derivada sin alias cae a Unresolved.
- **`WITH` a nivel de compound completo.** Una CTE precede a toda la cadena de compound (se adjunta a la consulta líder), no a una rama individual — consistente con cómo ambos motores parsean `WITH … SELECT … UNION …`. Una rama sí puede usar una tabla derivada en su `FROM`.
- **Sin regresión.** Campos nuevos con `omitempty`; toda la batería previa de vistas/DDL (incluida la ruta de metadata canónica, la inspección nativa base y los compounds de 2A) pasa sin cambios. Cualquier constructo no soportado (`WITH RECURSIVE`, subconsulta en `IN`, correlacionada, `EXISTS`) cae a Unresolved igual que antes.
