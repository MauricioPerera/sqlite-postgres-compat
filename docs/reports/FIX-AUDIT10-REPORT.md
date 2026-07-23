# FIX-AUDIT10-REPORT — Cierre de las dos MEDIA (M1 y M2)

**Fecha:** 2026-07-23
**Alcance:** las dos MEDIA reales de `docs/reports/AUDIT10-CONFIRM.md`.
**Motores:** SQLite real (`modernc.org/sqlite`) y **PostgreSQL 17 real**
(`postgres://postgres:***@31.220.22.176:5434/postgres`, password enmascarada).
**Archivos de producción tocados:** `compat/schema.go`, `compat/sqlselect.go`, `compat/ddl.go`.
**Tests:** `compat/schema_test.go`, `compat/sqlselect_test.go`, `compat/ddl_test.go`, `e2e/system_test.go`.
**Docs:** `AGENTS.md`, `docs/COMPATIBILITY.md`, `README.md`, `docs/TESTING.md`.

Ambos fixes entraron. Ninguno rompió tests existentes. No se tocó ningún archivo prohibido.

---

## M1 — `gen_random_uuid()` rechazado en un `CHECK` no determinista

### Diseño

`gen_random_uuid()` es el único nodo no determinista de la gramática. En un `CHECK`
el predicado se reevalúa **por fila y por motor de forma independiente**, así que un
predicado con resultado variable (p.ej. `gen_random_uuid() < '8000…'`) aceptaría o
rechazaría la misma fila de forma distinta entre SQLite y PostgreSQL — la divergencia
silenciosa de datos que la disciplina byte-idéntica prohíbe.

La guarda vive en `Schema.Validate` (engine-agnóstica), que `CompileDDL` corre antes de
emitir cualquier statement:

- Helper nuevo `expressionIsNonDeterministic(Expression) bool` (compat/schema.go): recorre
  el árbol (`Kind == "gen_random_uuid"` o cualquier `Args` anidado).
- En `validateTable`, dentro del bucle de `Constraints`: si `constraint.Kind == Check` y su
  expresión contiene el nodo → error `table %q CHECK constraint uses gen_random_uuid, which
  is non-deterministic and cannot appear in a CHECK constraint`.
- En `validateDomains`: mismo criterio para el `CHECK` de un dominio (compila por la misma
  ruta `compileExpression` y tiene el mismo riesgo por fila).

### Trade-offs / decisiones

- **Sólo se restringe `CHECK` (tabla y dominio).** `DEFAULT` de columna y valor de acción de
  trigger — los dos contextos sancionados — **no cambian**: ahí el valor se genera una vez y
  viaja como dato. Índice de expresión y columna generada `STORED` **no** se guardan aquí a
  propósito: AUDIT10 §2.4 muestra que **ambos motores** ya los rechazan de forma consistente
  (sin divergencia, sólo BAJA). `CHECK` era el único contexto donde ambos aceptaban.
- La guarda es por **propiedad** (recorrido del árbol), no por forma superficial: atrapa el
  nodo anidado dentro de comparaciones, `is_not_null`, etc.

---

## M2 — CTE auto-referencial no recursiva rechazada por propiedad

### Diseño

`WITH t AS (SELECT id FROM t) SELECT id FROM t` (con `t` también tabla base real) parsea y
compila **byte-idéntico** en ambos motores pero diverge en runtime: SQLite falla
(`circular reference: t`), PostgreSQL liga el `t` interior a la tabla base y devuelve filas.
El guard previo de `RECURSIVE` detecta la *keyword*, no la *propiedad* de auto-referencia.

Helpers nuevos en `compat/sqlselect.go`:

- `cteQueryReferencesName(query SelectQuery, name string) bool`: recorre `From`, `Joins`,
  subqueries (`TableSource.Subquery`), `Compounds` y las CTEs internas. **Respeta shadowing
  interno**: si un scope anidado re-liga `name` con su propia cláusula `WITH`, ese scope no
  cuenta como auto-referencia.
- `tableSourceReferencesName(source TableSource, name string) bool`.

Puntos de rechazo (ambos con el mismo mensaje `self-referential CTE %q requires RECURSIVE,
which is outside the canonical grammar`):

- **Parse** — `stripCatalogWith`: al terminar de parsear cada CTE, si su cuerpo referencia su
  propio nombre → rechazo. Cubre las vistas definidas por SQL.
- **Compile** — `compileWith` (compat/ddl.go): defensa en profundidad para un AST construido a
  mano que no pasa por el parser. Simétrico a cómo M1 se guarda en `Schema.Validate`.

### Trade-offs / decisiones

- **Una CTE que referencia a OTRA CTE anterior de la misma cláusula `WITH` es legítima y se
  sigue aceptando** (`WITH a AS (...), b AS (SELECT id FROM a) ...`). Sólo el mismo-nombre es
  auto-referencia. Test de no-regresión explícito
  (`TestParseCatalogSelectAcceptsSiblingCTEReference`).
- Doble guarda (parse + compile) porque, a diferencia de `RECURSIVE` (que no tiene
  representación en el AST), una auto-referencia **sí** es expresable en un AST hand-built y
  compilaría a DDL divergente sin el guard de `compileWith`.

---

## Verificación (salida real)

### Unit congelados — `go test ./compat/` (SQLite real + compilador de ambos motores)

```
--- PASS: TestCompileCTEForBothEngines (0.00s)
--- PASS: TestCompileRejectsSelfReferentialCTE (0.00s)
--- PASS: TestCompileMultipleCTEsForBothEngines (0.00s)
--- PASS: TestSchemaValidateRejectsGenRandomUUIDInCheck (0.00s)
--- PASS: TestSchemaValidateRejectsGenRandomUUIDInDomainCheck (0.00s)
--- PASS: TestSchemaValidateAcceptsGenRandomUUIDInDefault (0.00s)
--- PASS: TestSchemaValidateAcceptsGenRandomUUIDInTriggerAction (0.00s)
--- PASS: TestParseCatalogSelectSingleCTE (0.00s)
--- PASS: TestParseCatalogSelectMultipleCTEs (0.00s)
--- PASS: TestParseCatalogSelectRejectsRecursiveCTE (0.00s)
--- PASS: TestParseCatalogSelectRejectsSelfReferentialCTE (0.00s)
--- PASS: TestParseCatalogSelectRejectsSelfReferentialCTEInJoin (0.00s)
--- PASS: TestParseCatalogSelectAcceptsSiblingCTEReference (0.00s)
ok  	example.com/sqlite-postgres-compat/compat	2.533s
```

Cobertura del enunciado:
- **M1** CHECK con `gen_random_uuid()` rechazado (tabla, dominio y anidado); `Column.Default`
  con `gen_random_uuid()` sigue validando y compilando (no-regresión); acción de trigger con
  `gen_random_uuid()` sigue validando y compilando (no-regresión).
- **M2** self-reference rechazada en parse y en compile; CTE-a-CTE-hermana sigue aceptada
  (no-regresión); los tests congelados existentes de `WITH` (`TestParseCatalogSelect*CTE*`,
  `TestCompile*CTE*`, `TestParseCatalogSelectRejectsRecursiveCTE`) siguen verdes **sin
  modificarlos**.

### Confirmación contra PG17 real — reproducción de los dos casos EXACTOS de AUDIT10

`e2e/system_test.go::TestSystemRejectsAudit10DivergentDDLBeforeEngines` aplica cada schema
ofensivo a SQLite real y PostgreSQL 17 real vía `Store.ApplySchema` (que llama `CompileDDL`
→ `Schema.Validate`/`compileWith` **antes** de ejecutar DDL) y confirma que compat lo rechaza
en ambos motores y que la relación **no** existe en PostgreSQL (no escapó DDL al motor).

```
$env:COMPAT_POSTGRES_DSN = "postgres://postgres:***@31.220.22.176:5434/postgres?sslmode=disable"
go test -tags=e2e ./e2e -run TestSystemRejectsAudit10DivergentDDLBeforeEngines -v

=== RUN   TestSystemRejectsAudit10DivergentDDLBeforeEngines
=== RUN   TestSystemRejectsAudit10DivergentDDLBeforeEngines/M1_gen_random_uuid_in_CHECK
=== RUN   TestSystemRejectsAudit10DivergentDDLBeforeEngines/M2_self-referential_CTE
--- PASS: TestSystemRejectsAudit10DivergentDDLBeforeEngines (1.04s)
    --- PASS: TestSystemRejectsAudit10DivergentDDLBeforeEngines/M1_gen_random_uuid_in_CHECK (0.44s)
    --- PASS: TestSystemRejectsAudit10DivergentDDLBeforeEngines/M2_self-referential_CTE (0.60s)
PASS
ok  	example.com/sqlite-postgres-compat/e2e	4.284s
```

### `.\scripts\check.ps1`

```
gofmt: OK
go vet: OK
?   	example.com/sqlite-postgres-compat/cmd/compat	[no test files]
ok  	example.com/sqlite-postgres-compat/cmd/internal/cliout	2.085s
ok  	example.com/sqlite-postgres-compat/compat	1.941s
go test: OK
```

### `go vet -tags=e2e ./e2e`

Verde (sin salida).

### Suite e2e completa contra PG real — no-regresión

`go test -tags=e2e ./e2e -count=1` con el DSN real. El único fallo de nivel superior es
`TestSystemClaimsExactCoverageForRequiredFeatureFamilies`:

```
--- FAIL: TestSystemClaimsExactCoverageForRequiredFeatureFamilies (0.00s)
```

**Pre-existente e independiente de este cambio.** Ese test exige `Exact` para las familias
genéricas no-canónicas (`foreign_keys`, `check_constraints`, `indexes`, `views`, `triggers`,
`stored_routines`, `full_text`), pero `compat/features.go::assess` las devuelve `Unknown`
("requires parser and semantic compiler") de forma incondicional. `features.go` **no fue
tocado** (está fuera de la lista de archivos permitidos) y no es alcanzable desde ninguno de
los dos fixes, así que su rojo es idéntico antes y después. Ya estaba documentado como fallo
intencional en `README.md` / `docs/TESTING.md`. Todos los demás tests e2e (incluido el nuevo)
pasan.

---

## Conteo de tests e2e

`grep -rhE "^func Test[A-Z]" e2e/*.go | grep -v TestMain | wc -l` → **49** (antes 48; +1 por
`TestSystemRejectsAudit10DivergentDDLBeforeEngines`). `README.md` y `docs/TESTING.md`
actualizados a 49 totales / 48 superadas / 1 fallo intencional.

## Docs actualizadas en la misma tarea

- `AGENTS.md`: nota de `gen_random_uuid` (rechazo en CHECK), nota de CTE (rechazo
  self-referencial), listas de rechazo de SELECT y de `Schema.Validate`.
- `docs/COMPATIBILITY.md`: nota de `gen_random_uuid`, nueva subsección "CTE auto-referencial",
  filas de la tabla (`CHECK`, `Vistas`).

## Limpieza

- Sin bases PG temporales propias: el harness e2e crea/dropea una `compat_e2e_<nanos>` efímera;
  0 remanentes. Password siempre `***` en este reporte.
- Sin procesos en foreground colgados. Sin `git add/commit/stash`.
