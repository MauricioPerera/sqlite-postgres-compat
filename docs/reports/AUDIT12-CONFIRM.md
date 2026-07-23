# AUDIT12-CONFIRM — Confirmación de cierre de A11-1 (self-reference de CTE por case-fold) y ataque adversarial al alcance ASCII del fold

**Fecha:** 2026-07-23
**Auditor:** adversarial independiente, sin memoria previa.
**Commit auditado:** `a5cf78d` (`fix(grammar): fold ASCII case when detecting self-referential CTEs`), HEAD de `main`, árbol limpio.
**Fix bajo confirmación:** `cteQueryReferencesName`/`tableSourceReferencesName` (compat/sqlselect.go) ahora comparan
identificadores con `identifiersFoldEqual` (plegado **ASCII-only**, byte a byte) en vez de `==`, para detectar
auto-referencia de CTE cuando el nombre de la CTE y la tabla difieren sólo en case ASCII.
**Motores:** SQLite real (`modernc.org/sqlite`) y **PostgreSQL 17 real**
(`postgres://postgres:***@31.220.22.176:5434/postgres?sslmode=disable`, password enmascarada).
El harness E2E crea y **dropea** una base efímera `compat_e2e_<nanos>`; todas mis relaciones (`audit12_*`) vivieron
dentro de ella y se destruyeron al cerrar el binario de test.
**Alcance de cambios:** READ-ONLY sobre el repo. Probes temporales `compat/zz_audit12_test.go` (parser) y
`e2e/zz_audit12_test.go` (PG17+SQLite reales), **borrados al terminar**. Único fichero permanente: este reporte.
**Binarios/exit codes:** vía `go test` (unit y `-tags=e2e`), exit code observado por corrida. Sin procesos en foreground colgados.

---

## Parte 1 — Confirmación de cierre de A11-1 + no-regresiones

Re-ejecución sobre HEAD de los tests congelados del fix (unit + e2e contra PG17 real) y del repro exacto de A11-1.

### 1.1 Tests congelados (compat) — `go test ./compat/`

| # | Caso | Test congelado | Salida | Estado |
|---|---|---|---|---|
| **A11-1 repro exacto** | `WITH T AS (SELECT id FROM t) SELECT id FROM T` (parser) | `TestParseCatalogSelectRejectsCaseFoldSelfReferentialCTE` | **PASS** (rechazado, "self-referential … RECURSIVE") | **CERRADO** |
| **A11-1 repro exacto** | idem, AST hand-built, ambos motores | `TestCompileRejectsCaseFoldSelfReferentialCTE` | **PASS** | **CERRADO** |
| M2 original (mismo case exacto) | `WITH t AS (SELECT id FROM t)` | `TestParseCatalogSelectRejectsSelfReferentialCTE` | **PASS** (sigue rechazado) | OK no-regresión |
| M2 self-ref vía JOIN | `… INNER JOIN t …` | `TestParseCatalogSelectRejectsSelfReferentialCTEInJoin` | **PASS** | OK no-regresión |
| M2 compile AST | self-ref hand-built | `TestCompileRejectsSelfReferentialCTE` | **PASS** | OK no-regresión |
| No-FP nombres distintos | `WITH summary AS (SELECT id FROM orders) …` | `TestParseCatalogSelectAcceptsDistinctNamedCTE` | **PASS** (aceptado) | OK |
| No-FP dos CTEs distintos / sibling | `WITH a … , b AS (SELECT id FROM a) …` | `TestParseCatalogSelectAcceptsSiblingCTEReference` | **PASS** (aceptado) | OK |
| `WITH RECURSIVE` sigue rechazado | — | `TestParseCatalogSelectRejectsRecursiveCTE` | **PASS** | OK |
| **M1** CHECK con `gen_random_uuid()` | tabla | `TestSchemaValidateRejectsGenRandomUUIDInCheck` | **PASS** (rechazado) | OK |
| **M1** anidado en CHECK | `lt(…)` | `TestSchemaValidateRejectsGenRandomUUIDNestedInCheck` | **PASS** | OK |
| **M1** CHECK de dominio | dominio | `TestSchemaValidateRejectsGenRandomUUIDInDomainCheck` | **PASS** | OK |
| **M1** no-regresión Default | `Column.Default` | `TestSchemaValidateAcceptsGenRandomUUIDInDefault` | **PASS** (aceptado) | OK |
| **M1** no-regresión trigger | acción trigger | `TestSchemaValidateAcceptsGenRandomUUIDInTriggerAction` | **PASS** (aceptado) | OK |

`go test ./compat/` completo: **ok** (toda la suite del paquete verde). La familia M1 (`GenRandomUUID|NonDeterministic|Check`)
rechaza correctamente todos los CHECK no deterministas y sigue aceptando Default/trigger/CHECK canónico.

### 1.2 Tests congelados (e2e, PG17 real) — `go test -tags=e2e ./e2e`

```
--- PASS: TestSystemRejectsAudit10DivergentDDLBeforeEngines (0.84s)
    --- PASS: .../M1_gen_random_uuid_in_CHECK (0.44s)
    --- PASS: .../M2_self-referential_CTE (0.40s)
--- PASS: TestSystemRejectsAudit11CaseFoldSelfReferentialCTE (0.40s)
```

`TestSystemRejectsAudit11CaseFoldSelfReferentialCTE` confirma sobre motores reales que `ApplySchema` rechaza
`WITH T AS (SELECT id FROM t)` (AST hand-built) en SQLite y en PG17 **antes** de emitir DDL, y que la vista
`audit11_casefold_selfref` **no existe** en `information_schema.views` de PG tras el rechazo (0 filas).

**Suite e2e completa** (`go test -tags=e2e ./e2e -count=1`): el **único** `--- FAIL` es
`TestSystemClaimsExactCoverageForRequiredFeatureFamilies` — el fallo **intencional documentado** (familias genéricas
no-canónicas quedan `unknown` a propósito). **0 regresiones.**

### Repro exacto verificado también en mi probe independiente

Probe propio `TestA12_Part1_Confirm` (parser, independiente de los tests congelados):

```
REJECTED (self-ref) OK: "WITH T AS (SELECT id FROM t) SELECT id FROM T"        <- A11-1 CERRADO
REJECTED (self-ref) OK: "WITH t AS (SELECT id FROM t) SELECT id FROM t"        <- M2 exacto sigue rechazado
ACCEPTED OK: "WITH a AS (SELECT id FROM products), b AS (SELECT id FROM orders) SELECT id FROM b"  <- 2 CTEs distintos aceptado
ACCEPTED OK: "WITH a AS (SELECT id FROM products), b AS (SELECT id FROM a) SELECT id FROM b"       <- sibling aceptado
```

**Veredicto Parte 1: A11-1 CERRADO.** El repro exacto de FIX-AUDIT11-REPORT.md se rechaza en parser y compile
(ambos motores); M2 original, `WITH RECURSIVE`, y toda la familia M1 siguen rechazando; nombres genuinamente
distintos y siblings siguen aceptados. Sin regresiones fuera del fallo intencional preexistente.

---

## Parte 2 — Ataque adversarial al alcance "ASCII" del fold

El propio fix documentó (FIX-AUDIT11-REPORT §2) que el plegado es **ASCII-only** a propósito, para igualar el
plegado de SQLite (`sqlite3StrICmp`, sólo A–Z) y el downcasing ASCII de PG en identificadores no citados, evitando
el plegado Unicode más amplio de `strings.EqualFold`. Ataqué ese límite en serio, con motores reales.

### 2.a — Identificadores con case NO-ASCII: ¿pliegan SQLite y PG igual entre sí? — **ÁREA LIMPIA**

**Ground truth medido contra ambos motores reales** (probe `TestA12_NonASCIIFold_RealEngines`): creé una tabla
**no citada** `audit12_café` (é = U+00E9) e intenté leerla como `audit12_cafÉ` (É = U+00C9) — mismo nombre salvo la
case Unicode de la última letra.

```
SQLite    : unquoted non-ASCII fold é/É -> folded=false  selErr=no such table: audit12_cafÉ
PostgreSQL: unquoted non-ASCII fold é/É -> folded=false  selErr=relation "audit12_cafÉ" does not exist (42P01)
AGREEMENT: ambos motores folded=false para case no-ASCII
```

**Ninguno de los dos motores pliega la case no-ASCII** por defecto (PG17 sobre base UTF-8: su downcasing sólo baja
A–Z ASCII, los bytes multibyte quedan intactos; SQLite nunca pliega fuera de A–Z). Como **ambos coinciden** en tratar
`café` y `cafÉ` como nombres **distintos**, el plegado ASCII-only del código **coincide exactamente con el
comportamiento real** — no pliega lo que los motores no pliegan. Confirmado además a nivel runtime con el escenario
concreto de self-reference que el parser **acepta** (probe `TestA12_NonASCIISelfRef_RealEngines`): vista con CTE
`audit12_café` que lee de tabla base `audit12_cafÉ` (difieren sólo en case Unicode), DDL byte-idéntico emitido a
ambos motores:

```
SQLite    : applyErr=<nil> queryErr=<nil> rows=1 usable=true
PostgreSQL: applyErr=<nil> queryErr=<nil> rows=1 usable=true
no runtime divergence (sqlite usable=true postgres usable=true)
```

En **ambos** motores la CTE `café` NO sombrea la tabla base `cafÉ` (ninguno pliega el acento), así que el `FROM
audit12_cafÉ` interior liga a la tabla base y ambos devuelven 1 fila. **Sin divergencia.** compat aceptó
correctamente: plegar aquí (rechazar) habría sido un **falso positivo** contra un caso que ambos motores tratan como
seguro. El fold ASCII-only es **correcto**. **ÁREA LIMPIA.**

> Nota de límite (no hallazgo): el downcasing no-ASCII de PostgreSQL depende del **encoding de la base**. En una base
> UTF-8 (el default y el target real PG17 auditado) PG no pliega el acento — coincide con SQLite. Un hipotético
> encoding single-byte (LATIN1) donde PG plegara É→é sí divergiría de SQLite; queda fuera del target realista
> (UTF-8) y no es alcanzable contra el motor auditado. Lo dejo anotado como frontera teórica, sin severidad.

### 2.b — Identificadores CITADOS con case distinto (`WITH "T" AS (SELECT id FROM "t")`) — **TRADE-OFF DOCUMENTADO, NO hallazgo**

La pregunta del encargo asume que citados de verdad son case-sensitive en **ambos** motores, y que el fold-siempre
generaría un **falso positivo** rechazando un caso legítimo. **Verifiqué la premisa contra motores reales
(probe `TestA12_QuotedDistinctCase_RealEngines`) y la premisa es FALSA para SQLite:**

```
SQLite    : CREATE TABLE "audit12_qt" -> ok ; CREATE TABLE "AUDIT12_QT" -> error: table already exists
PostgreSQL: CREATE TABLE "audit12_qt" -> ok ; CREATE TABLE "AUDIT12_QT" -> ok (dos tablas distintas)
=> quoted-distinct: sqlite=false  postgres=true
```

**SQLite pliega la case ASCII incluso en identificadores CITADOS** (`"T"` y `"t"` son el MISMO nombre), mientras que
**PostgreSQL los trata como distintos**. Por lo tanto `WITH "T" AS (SELECT id FROM "t")` es en sí un caso
**divergente entre motores** (SQLite: `"T"` sombrea `"t"` → circular reference; PG: distintos → filas). El
fold-siempre del fix **NO es un falso positivo dañino**: rechaza precisamente un patrón que es un peligro de
divergencia real. Es la elección **conservadora y correcta**.

- El AST **no preserva** la señal citado/no-citado (`parseCatalogIdentifier` quita las comillas — confirmado en
  FIX-AUDIT11-REPORT §1), así que el código no puede distinguir `"T"` citado de `T` sin citar.
- Este trade-off está **documentado explícitamente** en FIX-AUDIT11-REPORT §3 y en el comentario de
  `identifiersFoldEqual` (compat/sqlselect.go:788-794). La evidencia real refuerza que la decisión es correcta:
  no sólo "conservadora", sino que evita una divergencia SQLite↔PG demostrada.

Probe parser confirmatorio: `WITH "T" AS (SELECT id FROM "t")` y `WITH "T" AS (SELECT id FROM t)` → **rechazados**
("self-referential"). Coherente con el diseño y con el motor. **Trade-off documentado, NO hallazgo nuevo.**

### 2.c — Fold total, no parcial (`myTable` / `mytable` / `MyTable`, >1 mayúscula, letra en medio) — **ÁREA LIMPIA**

Probe `TestA12_MultiCaseVariants`:

```
REJECTED: WITH myTable AS (SELECT id FROM MyTable) SELECT id FROM myTable   (difieren M y T en medio)
REJECTED: WITH myTable AS (SELECT id FROM mytable) SELECT id FROM myTable
REJECTED: WITH MyTable AS (SELECT id FROM myTABLE) SELECT id FROM MyTable   (3 mayúsculas)
REJECTED: WITH tbl_A2b AS (SELECT id FROM TBL_a2B) SELECT id FROM tbl_A2b   (dígitos + underscore + varias letras)
ACCEPTED: WITH myTable AS (SELECT id FROM myTables) SELECT id FROM myTable   (difiere 1 char real -> distinto)
ACCEPTED: WITH tbl_A2b AS (SELECT id FROM tbl_A3b) SELECT id FROM tbl_A2b   (2 vs 3 -> distinto)
```

El plegado es **total byte-a-byte** (`identifiersFoldEqual` recorre todos los bytes, baja A–Z en cada uno), no
parcial: colapsa varias mayúsculas, letras intermedias, y convive con dígitos/underscore. Nombres con una diferencia
real (no de case) siguen aceptados — sin falsos positivos. **ÁREA LIMPIA.**

### 2.d — Interacción con M2 profundo + variante de case — **ÁREA LIMPIA**

Combiné el hallazgo A11-1 (case distinto) con los escenarios anidados/compound/JOIN que AUDIT11 parte 2.2 dejó
limpios. Probe `TestA12_DeepCaseVariant`:

```
REJECTED: WITH t AS (SELECT id FROM (SELECT id FROM T) x) SELECT id FROM t                         (subquery anidada)
REJECTED: WITH t AS (SELECT id FROM other UNION SELECT id FROM T) SELECT id FROM t                 (rama de compound UNION)
REJECTED: WITH t AS (SELECT s.id FROM s INNER JOIN T ON T.id = s.id) SELECT id FROM t              (JOIN directo)
REJECTED: WITH t AS (SELECT a.id FROM other AS a JOIN (SELECT id FROM T) b ON b.id=a.id) …         (JOIN con subquery)
REJECTED: WITH t AS (SELECT id FROM (SELECT id FROM (SELECT id FROM T) y) x) SELECT id FROM t      (doble anidamiento)
```

La auto-referencia con **case distinto** (`t` vs `T`) se detecta en subconsulta anidada, en cada rama de un
compound, por JOIN directo y por JOIN con subquery, y a doble profundidad. `cteQueryReferencesName` baja por
`With`/`From`/`Joins`/`Compounds`/`Subquery` y el fold se aplica en los dos puntos de comparación
(`cteQueryReferencesName` y `tableSourceReferencesName`), así que **todos los caminos M2 quedan cubiertos también
con la variante de case.** **ÁREA LIMPIA.**

---

## Conteo final por severidad

| Severidad | # | Hallazgos |
|---|---|---|
| **ALTA** | 0 | — |
| **MEDIA** | 0 | — |
| **BAJA** | 0 | — |

**Total: 0 hallazgos nuevos.**

### Áreas verificadas y LIMPIAS (explícito, con qué se probó)
- **Non-ASCII fold (2.a):** medido contra **motores reales** — SQLite y PG17 (UTF-8) **coinciden** en NO plegar case
  no-ASCII (`café`≠`cafÉ` en ambos; ambos `no such table`/`does not exist`); self-ref no-ASCII aceptada por el parser
  no diverge en runtime (ambos usable=true, 1 fila). El fold ASCII-only del código **es correcto y coincide con los
  motores**. Frontera teórica (PG en encoding single-byte) anotada, fuera de target, sin severidad.
- **Citados con case distinto (2.b):** verificado contra motores reales — SQLite pliega case **incluso citando**
  (`"T"`=`"t"`), PG no. `WITH "T" AS (SELECT id FROM "t")` es un caso **divergente**; el fold-siempre lo rechaza
  correctamente. **Trade-off documentado** (FIX-AUDIT11-REPORT §3 + comentario en sqlselect.go), NO hallazgo.
- **Fold total no parcial (2.c):** `myTable`/`mytable`/`MyTable`, 3 mayúsculas, dígitos+underscore, letra en medio —
  todos rechazados; diferencias reales de 1 char siguen aceptadas. Sin falsos positivos.
- **M2 profundo + case (2.d):** subquery anidada, rama compound UNION, JOIN directo, JOIN con subquery, doble
  anidamiento — **todos** detectados con la variante `t`/`T`.
- **No-regresiones:** M2 exacto, `WITH RECURSIVE`, familia M1 completa (CHECK no determinista tabla/dominio/anidado)
  siguen rechazando; Default/trigger/CHECK canónico, nombres distintos y siblings siguen aceptados. Suite e2e contra
  PG17: único fallo el intencional documentado.

### Veredicto global
- **A11-1: CERRADO** (repro exacto rechazado en parser y compile, ambos motores; confirmado en unit congelados,
  e2e contra PG17 real, y probe independiente).
- **Ataque al alcance ASCII del fold:** el límite ASCII-only está **bien elegido** — coincide con lo que ambos
  motores reales realmente pliegan; los casos citados-con-case-distinto que parecían falsos positivos son en verdad
  divergencias que conviene rechazar, y el trade-off ya está documentado.
- **0 ALTA / 0 MEDIA / 0 BAJA.**

## Limpieza
- Probes temporales `compat/zz_audit12_test.go` y `e2e/zz_audit12_test.go`: **borrados** (verificado, no existen).
- Sin binarios remanentes (uso de `go test`, sin `go build -o` persistente); sin procesos en foreground colgados
  (el único background fue una corrida de `go test` que terminó por sí sola).
- Password del DSN siempre `***`. Todas las relaciones `audit12_*` vivieron dentro de la base efímera
  `compat_e2e_<nanos>` que el harness (`TestMain`) **dropea** al cerrar el binario de test; ninguna base con prefijo
  `audit12_` fue creada. Sin acceso admin directo (psql no disponible en el entorno); la limpieza está garantizada
  por el `DROP DATABASE` del harness ejecutado tras `m.Run()`.
- Sin `git add/commit/stash`. `git status` final: sólo `docs/reports/AUDIT12-CONFIRM.md` como adición nueva.
