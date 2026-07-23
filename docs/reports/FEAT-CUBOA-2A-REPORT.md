# FEAT-CUBOA-2A — Operaciones de conjuntos en el cuerpo de las vistas (UNION / UNION ALL / INTERSECT / EXCEPT)

**Fecha:** 2026-07-23
**Ámbito:** extender la gramática canónica de vistas (`compat/sqlselect.go` parse → `compat/ddl.go` compile) con compounds de conjuntos, cada uno verificado equivalente en SQLite (modernc) **y** PostgreSQL 17 real.
**Motor de PostgreSQL usado para verificación:** PostgreSQL 17 en `postgres://postgres:***@31.220.22.176:5434/postgres` (password enmascarada). Bases efímeras creadas y **dropeadas** por el harness E2E (`TestMain`, prefijo `compat_e2e_`); verificado **0** bases remanentes al terminar (query directa a `pg_database`).
**Archivos tocados:** `compat/schema.go`, `compat/sqlselect.go`, `compat/sqlselect_test.go`, `compat/ddl.go`, `compat/ddl_test.go`, `e2e/system_test.go`, `README.md`, `docs/TESTING.md`, `docs/COMPATIBILITY.md`, `AGENTS.md`, este reporte. No se tocó `sqlparse.go`, `inspect.go`, `store.go`, `capture.go`, `cmd/**`, ni reportes existentes.

---

## 1. Resumen ejecutivo

| Operador | Decisión | Motivo |
|---|---|---|
| `UNION` | **ENTRA** | Semántica de conjuntos idéntica en ambos motores; deparse plano en PG round-trips. |
| `UNION ALL` | **ENTRA** | Multiset idéntico; deparse plano. |
| `INTERSECT` (homogéneo) | **ENTRA** | Cadena homogénea agrupa igual (asociativa) en ambos. |
| `EXCEPT` | **ENTRA** | Diferencia de conjuntos idéntica; misma precedencia que UNION en ambos. |
| Cadena homogénea (`q1 UNION q2 UNION q3`) | **ENTRA** | Asociativa a izquierda idéntica en ambos. |
| Cadena mixta `{UNION, UNION ALL, EXCEPT}` sin INTERSECT | **ENTRA** | Los tres comparten precedencia y son asociativos a izquierda en **ambos** motores → compilar plano es exacto. |
| **Cadena que mezcla `INTERSECT` con `UNION`/`EXCEPT`** | **RECHAZADA** (→ Unresolved, `Exact=false`) | **Divergencia real verificada:** INTERSECT liga más fuerte en PostgreSQL, con igual precedencia en SQLite. Ver §4. |
| Ramas parentizadas (`... UNION (SELECT … INTERSECT SELECT …)`) | **RECHAZADA** | Una rama debe empezar por `SELECT`; el operador queda a `depth>0` y no se parte → Unresolved. |
| Subconsultas / CTE dentro de una rama | **FUERA DE ALCANCE** (otro lote) | No introducidas en este lote. |

**Entraron los cuatro operadores.** La única combinación rechazada es la cadena que **mezcla** INTERSECT con UNION/EXCEPT, por divergencia de precedencia entre motores verificada contra PG real (no es un "no se puede el operador", es "no se puede esa agrupación mixta sin divergir"). Regla de oro respetada: sólo se emite SQL que se pudo verificar equivalente; lo que divergiría se rechaza como objeto no resuelto, nunca se acepta en silencio.

---

## 2. Diseño

### 2.1 Modelo (aditivo, opt-in por presencia)

En `compat/schema.go`, `SelectQuery` gana **un** campo nuevo y un tipo nuevo, sin tocar ningún otro struct:

```go
Compounds []CompoundSelect `json:"compounds,omitempty"`   // entre Having y OrderBy

type CompoundSelect struct {
    Operator string      `json:"operator"` // "union"|"union_all"|"intersect"|"except"
    Query    SelectQuery `json:"query"`
}
```

- **Cadena plana asociativa a izquierda:** `q0 op1 q1 op2 q2 …`. `q0` es la propia `SelectQuery` (la rama líder); `Compounds` lleva las ramas siguientes con el operador que las une a lo de su izquierda.
- **ORDER BY / LIMIT / OFFSET del compound completo** viven en la `SelectQuery` líder (no en la última rama). Cada `CompoundSelect.Query` no lleva trailing clauses propias.
- **Byte-idéntico sin compounds:** ausencia del campo deja el comportamiento single-SELECT intacto; `omitempty` evita cambiar el snapshot JSON de vistas existentes. Verificado: toda la batería unitaria previa de vistas/DDL pasa sin cambios (§3.1).

### 2.2 Parseo (`compat/sqlselect.go`)

Refactor puramente estructural + capa nueva:
- `stripCatalogSelectHeader` se dividió en `stripCatalogViewPrefix` (quita `CREATE VIEW … AS`, deja el texto empezando en `SELECT`) y `stripCatalogSelectKeyword` (quita `SELECT [DISTINCT]` de una rama). El cuerpo del antiguo `parseCatalogSelect` es ahora `parseSingleCatalogSelect` (parsea una rama).
- `splitCompoundSelect` escanea el texto respetando comillas simples/dobles y profundidad de paréntesis (mismo patrón que `topLevelKeyword`/`splitTopLevelComma`) y parte en cada operador de nivel superior. `UNION ALL` se prueba antes que `UNION`. Tolera whitespace interno del keyword vía `keywordMatchSpan` (`UNION  ALL`, `UNION\tALL`).
- `parseCatalogSelect` nuevo: si no hay operadores → `parseSingleCatalogSelect` (camino idéntico). Si los hay: rechaza mezcla INTERSECT, parsea cada rama, exige que sólo la última rama lleve ORDER BY/LIMIT/OFFSET, y hoista esas cláusulas de la última rama a la líder.

**Round-trip contra el deparse de PG:** PG normaliza las definiciones de vista. Un probe contra PG real (creado, ejecutado y **borrado**) confirmó que una cadena **homogénea** se deparsea **plana** (sin paréntesis), por lo que el splitter la round-trips; y que PG **parentiza** las cadenas de precedencia mixta (`UNION (… INTERSECT …)`, `(… UNION …) EXCEPT …`) — esas ramas parentizadas no empiezan por `SELECT` y caen a Unresolved, comportamiento honesto y consistente con el rechazo del parser. SQLite guarda la definición **verbatim**, así que su round-trip es directo.

### 2.3 Compilación (`compat/ddl.go`)

`compileSelect` se dividió en `compileSelectCore` (SELECT…HAVING, reutilizable por rama) y `compileSelectTrailing` (ORDER BY/LIMIT/OFFSET, emitido **una vez** tras todas las ramas). `compileSelect` compila la líder, concatena `op branch` por cada compound, y añade el trailing al final. Sin compounds el resultado es byte-idéntico al anterior (mismo espaciado). Guardas defensivas en compilación (por si el AST se construye a mano): rechazo de mezcla INTERSECT (`validateCompoundChain`), operador desconocido (`compoundOperatorSQL`), y rama con trailing/compound anidado.

---

## 3. Verificación

### 3.1 `.\scripts\check.ps1` (gofmt + vet + unit) — VERDE

```
gofmt: OK
go vet: OK
go test: OK
?   	example.com/sqlite-postgres-compat/cmd/compat	[no test files]
ok  	example.com/sqlite-postgres-compat/cmd/internal/cliout	2.469s
ok  	example.com/sqlite-postgres-compat/compat	2.383s
```

`go vet -tags=e2e ./e2e` → `VET OK` (sin hallazgos).

### 3.2 Tests unitarios congelados (parse + compile en ambos motores)

`compat/sqlselect_test.go` (parse → AST) y `compat/ddl_test.go` (compile → SQL exacto). Salida real:

```
--- PASS: TestParseCatalogSelectCompoundOperators
    /union /union_all /intersect /except /union_chain
    /mixed_union_all_and_except /all_intersect_chain
--- PASS: TestParseCatalogSelectCompoundTrailingClauseAppliesToWholeCompound
--- PASS: TestParseCatalogSelectRejectsMixedIntersect            # 4 combinaciones INTERSECT mezcladas
--- PASS: TestParseCatalogSelectRejectsParenthesizedCompoundBranch
--- PASS: TestParseCatalogSelectCompoundBranchInternalWhitespace # 'UNION  ALL' y UNION dentro de string
--- PASS: TestCompileCompoundSelectForBothEngines
    /union /union_all /intersect /except /homogeneous_chain
--- PASS: TestCompileCompoundTrailingClausesApplyOnceAfterAllBranches
--- PASS: TestCompileCompoundBranchKeepsPerEngineExpressionMapping  # LIKE→ILIKE por rama en PG
--- PASS: TestCompileCompoundRejectsInvalidChains
    /mixed_intersect /unknown_operator /branch_carries_trailing_clause
PASS
ok  	example.com/sqlite-postgres-compat/compat	2.384s
```

SQL exacto asertado (idéntico en SQLite y PostgreSQL):

| Caso | SQL emitido (ambos motores) |
|---|---|
| `union` | `SELECT "a" FROM "t" UNION SELECT "b" FROM "s"` |
| `union all` | `SELECT "a" FROM "t" UNION ALL SELECT "b" FROM "s"` |
| `intersect` | `SELECT "a" FROM "t" INTERSECT SELECT "b" FROM "s"` |
| `except` | `SELECT "a" FROM "t" EXCEPT SELECT "b" FROM "s"` |
| cadena homogénea | `SELECT "a" FROM "t" UNION SELECT "b" FROM "s" UNION SELECT "c" FROM "u"` |
| trailing del compound | `SELECT "a" FROM "t" UNION SELECT "b" FROM "s" ORDER BY "a" DESC LIMIT 10 OFFSET 5` |

(`LIKE` en una rama sigue mapeando a `ILIKE` en PostgreSQL — verificado por rama.)

### 3.3 Equivalencia contra PostgreSQL 17 REAL (E2E)

Dos verificaciones contra PG real (DSN dado, password `***`):

**(a) `TestSystemCompoundViewsProduceEquivalentResults` (nuevo, top-level).** Schema canónico con dos tablas (`comp_a`, `comp_b`) y cuatro vistas compound (`UNION`, `UNION ALL`, `INTERSECT`, `EXCEPT`) sobre datos con una fila duplicada `(2,'b')` y filas exclusivas de cada tabla. `ApplySchema` en SQLite + `ImportSnapshot` en PG real (import verde ⇒ PG acepta el DDL compilado de las cuatro vistas). Se consulta cada vista en ambos motores (`SELECT id, name FROM v ORDER BY id, name`) y se comparan los resultados: coinciden fila a fila — UNION deduplica, UNION ALL retiene el duplicado, INTERSECT devuelve `(2,'b')`, EXCEPT devuelve `(1,'a'),(3,'c')` en ambos.

**(b) `TestSystemInspectsNativeSchemaObjectsWithoutMetadata` (extendido).** Se añadió una vista `native_all_codes AS SELECT code FROM native_products UNION SELECT product_code FROM native_audit` a la DDL nativa cruda de **ambos** motores. El test inspecciona un catálogo sin metadata canónica (ejercita `parseCatalogSelect` contra el deparse normalizado de PG y el verbatim de SQLite), asserta `Exact == true` y `len(Unresolved) == 0`, y ahora también que la vista compound se reconstruye con `len(Compounds)==1` y `Operator=="union"`. Esto prueba que la auditoría **`canonical_views` sigue exact** con compounds presentes en los dos motores reales.

Ejecutado DE VERDAD:

```
=== RUN   TestSystemInspectsNativeSchemaObjectsWithoutMetadata
--- PASS: TestSystemInspectsNativeSchemaObjectsWithoutMetadata (5.34s)
=== RUN   TestSystemCompoundViewsProduceEquivalentResults
--- PASS: TestSystemCompoundViewsProduceEquivalentResults (2.16s)
```

**Suite E2E completa contra PG real** (`go test -tags=e2e ./e2e -count=1`):

```
PASS count: 40
FAIL count: 1
--- FAIL: TestSystemClaimsExactCoverageForRequiredFeatureFamilies (0.00s)
```

40 top-level superadas y **1 fallida de forma intencional** (el gate del 100 %, rojo por diseño). Sin ninguna otra falla. **0** bases `compat_e2e_%`/`cuboa%` remanentes en PG tras la corrida (verificado con query a `pg_database`).

---

## 4. Divergencia rechazada — evidencia real (PostgreSQL 17)

Probe contra PG real (creado, ejecutado, **borrado**) del `view_definition` normalizado:

**Cadena homogénea → deparse PLANO (round-trips):**
```
CREATE VIEW v_chain AS SELECT id,name FROM pa UNION SELECT id,name FROM pb UNION SELECT id,name FROM pc
→  SELECT pa.id, pa.name FROM pa
   UNION
   SELECT pb.id, pb.name FROM pb
   UNION
   SELECT pc.id, pc.name FROM pc
```

**Cadena mixta con INTERSECT → PG PARENTIZA (precedencia divergente):**
```
CREATE VIEW v_mixed AS SELECT … FROM pa UNION SELECT … FROM pb INTERSECT SELECT … FROM pc
→  SELECT pa.id, pa.name FROM pa
   UNION (                              ← PG agrupa  pa UNION (pb INTERSECT pc)
         SELECT pb.id … FROM pb
        INTERSECT
         SELECT pc.id … FROM pc
   )
```

SQLite, en cambio, trata los cuatro operadores con **igual** precedencia y asociatividad a izquierda: `pa UNION pb INTERSECT pc` = `(pa UNION pb) INTERSECT pc`. Es decir, la **misma** cadena de texto produce **resultados distintos** entre motores. Compilar una cadena plana `pa UNION pb INTERSECT pc` sería incorrecto (PG la re-agruparía). Por eso una cadena que **mezcla** INTERSECT con UNION/EXCEPT se **rechaza** en parseo y compilación → objeto no resuelto (`Exact=false`), nunca SQL divergente.

**Nota de honestidad:** la premisa del enunciado ("los cuatro tienen semántica de conjuntos idéntica … asociatividad a izquierda como ambos motores") es correcta para cada operador aislado y para cadenas homogéneas o de la familia `{UNION, UNION ALL, EXCEPT}`, pero **no** para cadenas que mezclan INTERSECT: ahí la asociatividad NO es común entre motores. Se implementó lo que sí es exacto y se rechazó lo que divergiría, con la evidencia de arriba.

**Cadena mixta `{UNION, EXCEPT}` sin INTERSECT (aceptada al compilar):** UNION, UNION ALL y EXCEPT comparten precedencia y son asociativos a izquierda en **ambos** motores, así que `pa UNION pb EXCEPT pc` = `(pa UNION pb) EXCEPT pc` en los dos → compilar plano es exacto. (PG deparsea esa forma con paréntesis explícitos `(…) EXCEPT …`, por lo que su round-trip por inspección cae a Unresolved; el sentido canónico→ambos motores es exacto. La inspección exacta se cubre con cadenas homogéneas, que es lo que PG deparsea plano.)

---

## 5. Trade-offs y notas

- **Modelo plano vs árbol.** Se eligió una cadena plana (`Compounds []CompoundSelect`) por ser aditiva y suficiente para la asociatividad a izquierda común. El precio es que las agrupaciones de precedencia mixta (que requerirían un árbol o paréntesis) no se modelan; se rechazan honestamente en vez de forzarse. Consistente con el resto de la capa.
- **Trailing en el nivel top.** ORDER BY/LIMIT/OFFSET del compound viven en la `SelectQuery` líder y se hoistan de la última rama en parseo; en compilación se emiten una sola vez al final. Un ORDER BY en una rama no final se rechaza (ambos motores lo rechazan sin paréntesis).
- **Round-trip de ORDER BY en compounds.** PG deparsea el ORDER BY de un compound por **posición ordinal** (`ORDER BY 1`, no `ORDER BY id`) y emite `OFFSET` antes de `LIMIT`. Por eso el test de inspección exacta usa compounds **sin** ORDER BY (round-trip limpio); el ORDER BY/LIMIT/OFFSET de compound se congela en los tests de parse+compile unitarios. No es una regresión: el ORDER BY de vistas ya no se round-trippeaba exacto vía PG antes de este lote.
- **Sin regresión.** Campo nuevo con `omitempty`; toda la batería previa de vistas/DDL (incluida la ruta de metadata canónica y la inspección nativa base) pasa sin cambios. Cualquier compound no soportado cae a Unresolved igual que antes (antes **todos** los compounds caían a Unresolved), así que no hay pérdida de exactitud para nada que antes funcionara.

---

## 6. Limpieza

- Probe temporal contra PG (`e2e/zz_probe_test.go`) y programa inline de conteo de bases: **borrados**. `git status` sólo muestra los archivos permitidos + este reporte.
- Sin procesos en foreground que no terminen solos. Nada escrito fuera del repo. Sin `git add/commit/stash`. Password del DSN nunca en literal (enmascarada `***`). **0** bases temporales remanentes en PostgreSQL (el harness reusa la base efímera `compat_e2e_%` de `TestMain`, dropeada al terminar; no se crearon bases con prefijo propio).
