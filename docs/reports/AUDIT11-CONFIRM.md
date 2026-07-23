# AUDIT11-CONFIRM — Confirmación de cierre de M1/M2 (AUDIT10) y ataque adversarial al fix `338edb3`

**Fecha:** 2026-07-23
**Auditor:** adversarial independiente, sin memoria previa.
**Commit auditado:** `338edb3` (`fix(grammar): reject non-deterministic CHECK and self-referential non-recursive CTE`), sobre HEAD.
**Motores:** SQLite real (`modernc.org/sqlite`) y **PostgreSQL 17 real**
(`postgres://postgres:***@31.220.22.176:5434/postgres?sslmode=disable`, password enmascarada).
El harness E2E crea y **dropea** una base efímera `compat_e2e_<nanos>`; todas mis relaciones
(`audit11_*`, `t`) vivieron dentro de ella. **0 bases/relaciones remanentes** (verificado, ver Limpieza).
**Alcance de cambios:** READ-ONLY sobre el repo. Probes temporales `compat/zz_audit11_test.go` y
`e2e/zz_audit11_test.go`, **borrados al terminar**. Único fichero permanente: este reporte.
**Binarios/exit codes:** `go test` (exit code observado por corrida). Sin procesos en foreground colgados.

---

## Parte 1 — Confirmación de cierre (repros exactos de FIX-AUDIT10-REPORT.md)

Re-ejecución sobre HEAD de las pruebas congeladas del fix y de la reproducción E2E contra PG17 real.

| # | Caso (repro exacto) | Ruta | Salida real | Estado |
|---|---|---|---|---|
| M1 | `CHECK (is_not_null(gen_random_uuid()))` en tabla | `Schema.Validate` + `CompileDDL` (sqlite+postgres) | `TestSchemaValidateRejectsGenRandomUUIDInCheck` **PASS** | **CERRADO** |
| M1 | `gen_random_uuid()` anidado en `CHECK` (`lt`) | idem | `TestSchemaValidateRejectsGenRandomUUIDNestedInCheck` **PASS** | **CERRADO** |
| M1 | `gen_random_uuid()` en CHECK de **dominio** | `Schema.Validate` | `TestSchemaValidateRejectsGenRandomUUIDInDomainCheck` **PASS** | **CERRADO** |
| M2 | `WITH t AS (SELECT id FROM t) SELECT id FROM t` | parser `stripCatalogWith` | `TestParseCatalogSelectRejectsSelfReferentialCTE` **PASS** | **CERRADO** |
| M2 | self-ref vía JOIN | parser | `TestParseCatalogSelectRejectsSelfReferentialCTEInJoin` **PASS** | **CERRADO** |
| M2 | self-ref en AST hand-built | `compileWith` | `TestCompileRejectsSelfReferentialCTE` **PASS** | **CERRADO** |
| M1+M2 | ambos schemas ofensivos aplicados a **SQLite + PG17 reales** vía `ApplySchema`; relación NO existe en PG | `e2e` | `TestSystemRejectsAudit10DivergentDDLBeforeEngines` (+2 subtests) **PASS** | **CERRADO** |

### No-regresiones confirmadas

| Caso | Salida | Estado |
|---|---|---|
| `Column.Default = gen_random_uuid()` sigue validando+compilando (ambos motores) | `TestSchemaValidateAcceptsGenRandomUUIDInDefault` **PASS** | OK |
| Acción de trigger con `gen_random_uuid()` sigue validando+compilando | `TestSchemaValidateAcceptsGenRandomUUIDInTriggerAction` **PASS** | OK |
| `WITH a AS (...), b AS (SELECT id FROM a)` (CTE hermana, no self-ref) sigue aceptada | `TestParseCatalogSelectAcceptsSiblingCTEReference` **PASS** | OK |
| Rechazo de `WITH RECURSIVE` intacto | `TestParseCatalogSelectRejectsRecursiveCTE` **PASS** | OK |
| Vista con CTE hermana aplicada+consultada idéntica en **ambos motores reales** (2 vs 2 filas) | probe `TestAudit11SiblingAndCubeAOnRealEngines` **PASS** | OK |

Salida (extractos reales):

```
=== FROZEN UNIT (compat) ===
--- PASS: TestCompileRejectsSelfReferentialCTE
--- PASS: TestSchemaValidateRejectsGenRandomUUIDInCheck
--- PASS: TestSchemaValidateRejectsGenRandomUUIDNestedInCheck
--- PASS: TestSchemaValidateRejectsGenRandomUUIDInDomainCheck
--- PASS: TestSchemaValidateAcceptsGenRandomUUIDInDefault
--- PASS: TestSchemaValidateAcceptsGenRandomUUIDInTriggerAction
--- PASS: TestParseCatalogSelectRejectsRecursiveCTE
--- PASS: TestParseCatalogSelectRejectsSelfReferentialCTE
--- PASS: TestParseCatalogSelectRejectsSelfReferentialCTEInJoin
--- PASS: TestParseCatalogSelectAcceptsSiblingCTEReference
ok  	example.com/sqlite-postgres-compat/compat	2.457s

=== FROZEN E2E (real PG) ===
--- PASS: TestSystemRejectsAudit10DivergentDDLBeforeEngines (0.78s)
    --- PASS: .../M1_gen_random_uuid_in_CHECK (0.40s)
    --- PASS: .../M2_self-referential_CTE (0.37s)
ok  	example.com/sqlite-postgres-compat/e2e	3.972s
```

**Veredicto Parte 1: M1 CERRADO, M2 CERRADO** para las formas exactas del reporte. (M2 tiene un
flanco NO cubierto — ver Hallazgo A11-1 abajo.)

---

## Parte 2 — Ataque adversarial al código nuevo del fix

Evidencia ejecutada en `compat/zz_audit11_test.go` (unit) y `e2e/zz_audit11_test.go` (PG17 real), ambos borrados.

### 2.1 M1 — recorrido del árbol a cualquier profundidad — LIMPIO

`expressionIsNonDeterministic` (compat/schema.go:663) recorre `expression.Kind` + todo `expression.Args`.
El struct `Expression` sólo tiene `Kind/Value/Args`: **todo hijo de cualquier nodo (incluido `CASE`,
que se modela como `Kind:"case", Args:[...]`) vive en `Args`**, así que el recorrido es exhaustivo por
construcción.

```
[deep nest] ne(lower(coalesce(gen_random_uuid(),'x')),'x')  (3 niveles) en CHECK de tabla
  Validate: rejected "...CHECK constraint uses gen_random_uuid..."
  CompileDDL[sqlite]  : rejected     CompileDDL[postgres]: rejected
[CASE] case(is_not_null(gen_random_uuid()),1,0) en CHECK de tabla
  Validate: rejected
[domain deep nest] mismo predicado de 3 niveles en CHECK de DOMINIO
  Validate: rejected "domain ... CHECK uses gen_random_uuid..."
```

- **Profundidad:** detectado a 3 niveles y dentro de `CASE`. No sólo el nivel superior. **Limpio.**
- **CHECK de columna vs CHECK de tabla:** en este AST **no existe** un "CHECK de columna" separado —
  `Column` (schema.go:63) no tiene campo `Check`; todo CHECK es un `Constraint{Kind:Check}` en
  `Table.Constraints`, con un único camino de validación (`validateTable`, guardado). Un CHECK de
  dominio es el otro camino (`validateDomains`, guardado). **Ambos caminos atrapan el nodo.** Limpio.
- **Dominio:** rechazado igual que en tabla (mismo criterio, mensaje propio). Limpio.
- **No-regresión:** un CHECK limpio (`n > 0`, sin `gen_random_uuid`) sigue validando y compilando en
  ambos motores (`TestAudit11M1CleanCheckStillAccepted` PASS). Limpio.

### 2.2 M2 — variantes de self-reference (parser) — todas DETECTADAS

`cteQueryReferencesName` (sqlselect.go:742) baja por `With` (con shadowing), `From`, `Joins`,
`Compounds` y subqueries (`tableSourceReferencesName` → `Subquery`). Probado:

```
[nested FROM-subquery] WITH t AS (SELECT id FROM (SELECT id FROM t) x) ...
  rejected: self-referential CTE "t" requires RECURSIVE...
[compound UNION branch] WITH t AS (SELECT id FROM other UNION SELECT id FROM t) ...
  rejected: self-referential CTE "t" requires RECURSIVE...
[JOIN-subquery] WITH t AS (SELECT a.id FROM other AS a JOIN (SELECT id FROM t) b ON ...) ...
  rejected: self-referential CTE "t" requires RECURSIVE...
```

Baja recursivamente a subconsultas anidadas, a cada rama del compound, y por JOIN (directo y con
subquery a la derecha). **Los cuatro caminos de auto-referencia se detectan.** Limpio.

### 2.3 M2 — sin falsos positivos entre nombres/vistas distintos — LIMPIO

```
[distinct names] WITH x AS (SELECT id FROM t) SELECT id FROM x   -> accepted (t base, x CTE)
[cross-view]     V1: WITH t AS (SELECT id FROM src) SELECT id FROM t  -> accepted
                 V2: SELECT id FROM t                                  -> accepted
[sibling via subquery] WITH a AS (...), b AS (SELECT id FROM (SELECT id FROM a) z) ... -> accepted
```

El helper sólo inspecciona referencias al **propio** nombre de la CTE; dos vistas se parsean en scopes
independientes, sin contaminación cruzada. Ningún falso positivo. Limpio.

### 2.4 Cubo A no se rompió — LIMPIO

```
[Cube A combo, no self-ref] WITH a AS (SELECT id,kind FROM src WHERE kind='x')
  SELECT CASE WHEN kind='x' THEN 1 ELSE 0 END AS flag FROM a UNION SELECT 2 AS flag FROM other
  -> parse OK; compile[sqlite] OK; compile[postgres] OK
```

Una vista con CTE + set-op + CASE (no self-referencial) sigue parseando y compilando en ambos motores.
Confirmado además contra motores reales con la vista de CTE hermana (2 vs 2 filas idénticas). Limpio.

---

## Hallazgo A11-1 — Self-reference case-fold: `WITH T AS (SELECT id FROM t)` diverge en runtime · **MEDIA**

**El fix cierra la auto-referencia de mismo-nombre EXACTO, pero no la de nombres que difieren sólo en
mayús/minús ASCII.** El helper compara **case-sensitive** (`cte.Name == name`, `source.Table == name`,
sqlselect.go:747/778) y el compilador **cita todos los identificadores** (`quoteIdentifier`, ddl.go:1018).
Bajo ese modelo `"T"` (CTE) y `"t"` (tabla) son distintos, así que compat **NO** lo marca como
auto-referencia y emite DDL byte-idéntico a ambos motores. Pero:

- **SQLite pliega identificadores ASCII citados case-insensitive** → la CTE `"T"` **sí** sombrea el
  `FROM "t"` interior → auto-referencia → `circular reference`.
- **PostgreSQL trata `"T"` y `"t"` como distintos** → el `FROM "t"` interior liga a la tabla base → filas.

Mismo `CREATE VIEW`, comportamiento divergente en runtime — **exactamente la clase M2 que el fix se
propuso cerrar.** Evidencia ejecutada contra motores reales:

```
compat parser: WITH T AS (SELECT id FROM t) SELECT id FROM T   -> ACCEPTED
  compile[sqlite]   = WITH "T" AS (SELECT "id" FROM "t") SELECT "id" FROM "T"
  compile[postgres] = WITH "T" AS (SELECT "id" FROM "t") SELECT "id" FROM "T"   (byte-idéntico)

e2e (AST hand-built, tabla base "t" con 2 filas, PG17 + SQLite reales):
  [sqlite]   applyErr=<nil>  queryErr=SQL logic error: circular reference: T (1)  rows=0
  [postgres] applyErr=<nil>  queryErr=<nil>                                        rows=2
  >>> RUNTIME DIVERGENCE (case-fold self-ref): sqlite usable=false, postgres usable=true
```

**Alcance/precondición:** requiere que el nombre de la CTE y la tabla auto-referenciada difieran sólo en
case ASCII **y** que exista una tabla base con el nombre plegado (para que PG devuelva datos en lugar de
error "relation does not exist"). Cuando esa tabla base existe, es **divergencia silenciosa de datos**
(PG devuelve filas, SQLite queda inutilizable). Alcanzable por **ambos** caminos: el parser SQL acepta
`WITH T AS (SELECT id FROM t) …` y el AST hand-built compila igual (los dos probados arriba).

**Atribución honesta:** es un **flanco no cubierto del propio fix M2**, no un caso pre-existente ajeno:
el commit `338edb3` introdujo el guard por-propiedad precisamente para esta clase de divergencia, y esta
variante (colisión por case-fold de SQLite) queda fuera porque el helper compara con `==` en vez de
plegar case como lo hace SQLite. La forma exacta-case (la del reporte) sí quedó cerrada.

**Severidad MEDIA:** misma clase y misma severidad que el M2 original de AUDIT10 (divergencia runtime
sobre DDL byte-idéntico). No ALTA porque exige una colisión de nombres por diferencia de case (el camino
todo-minúsculas, el común, sí se atrapa) más una tabla base homónima para volverse divergencia de datos.

**Recomendación:** en `cteQueryReferencesName`/`tableSourceReferencesName` comparar los identificadores
**case-insensitive para ASCII** (equiparando el plegado de SQLite), o —de raíz— que la gramática rechace
identificadores que colisionan bajo el plegado de un motor pero no del otro. Nota estructural más amplia:
compat emite identificadores citados (semántica case-sensitive), lo que choca con el plegado
case-insensitive de SQLite incluso citando; este hallazgo es la manifestación concreta y explotable de
esa discordancia dentro del alcance CTE.

---

## Conteo final por severidad

| Severidad | # | Hallazgos |
|---|---|---|
| **ALTA** | 0 | — |
| **MEDIA** | 1 | **A11-1** self-reference case-fold (`WITH T AS (SELECT id FROM t)`): compat acepta y emite DDL byte-idéntico; SQLite da `circular reference`, PG17 devuelve filas — flanco no cubierto del fix M2. |
| **BAJA** | 0 | — |

### Áreas verificadas y LIMPIAS (explícito)
- **M1 recorrido del árbol:** detectado a 3 niveles, dentro de `CASE`, en CHECK de tabla y de dominio;
  CHECK limpio sigue aceptándose. `Column` no tiene CHECK propio → único camino de tabla, guardado. **Limpio.**
- **M2 variantes de self-ref:** nested FROM-subquery, rama de compound UNION, JOIN-subquery — **todas detectadas.** **Limpio.**
- **M2 falsos positivos:** nombres distintos, dos vistas separadas homónimas, sibling vía subquery — **ninguno.** **Limpio.**
- **Cubo A:** vista con CTE + set-op + CASE (no self-ref) parsea y compila en ambos motores; CTE hermana idéntica en motores reales. **Limpio.**
- **No-regresiones:** Default y trigger con `gen_random_uuid()` siguen aceptados; `WITH RECURSIVE` sigue rechazado; CTE hermana sigue aceptada. **Limpio.**

### Veredicto global
- **M1: CERRADO** (recorrido exhaustivo del árbol; tabla y dominio; no-regresiones verdes).
- **M2: CERRADO para la forma exacta-case** del reporte, con **1 flanco MEDIA nuevo** (A11-1, case-fold)
  que reintroduce la misma clase de divergencia por una colisión de identificadores case-insensitive de SQLite.
- **1 MEDIA / 0 ALTA / 0 BAJA.**

## Limpieza
- Probes temporales `compat/zz_audit11_test.go` y `e2e/zz_audit11_test.go`: **borrados**.
- Sin binarios remanentes; sin procesos en foreground colgados; sin `git add/commit/stash`.
- Password del DSN siempre `***`. Bases efímeras (`compat_e2e_*`) dropeadas por el harness;
  verificado por consulta directa: `leaked_dbs=0 relations_named_audit11_or_t_in_base=0`.
- `git status` final: sólo `docs/reports/AUDIT11-CONFIRM.md` como adición nueva.
