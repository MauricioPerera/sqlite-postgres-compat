# AUDIT7-A — Auditoría de INTERACCIONES entre cambios del núcleo SQL

**Fecha:** 2026-07-22
**Ámbito:** ataques con inputs COMBINADOS que ninguna ronda aislada probó, sobre los cambios acumulados esta semana en el núcleo SQL: hex con signo (two's-complement), gramática numérica `isCatalogNumber`, tolerancia a whitespace en keywords multi-palabra (`keywordMatchSpan`), gating NEW/OLD por contexto de trigger, `knownTypeFamilies` en `Validate`, rechazo de operandos vacíos, refactors de aplanamiento.
**Archivos atacados:** `compat/sqlparse.go`, `compat/sqlselect.go`, `compat/sqltrigger.go`, `compat/sqlroutine.go`, `compat/ddl.go`, `compat/schema.go`.
**Entorno:** Windows 11, Go 1.26.4, repo `sqlite-postgres-compat` en `main` (commit `1f9fff9`). Árbol limpio al inicio. **READ-ONLY**: no se modificó ningún fuente del repo. SQLite real vía `modernc.org/sqlite` en `:memory:` para confirmar semántica documentada. Postgres: semántica citada de la documentación oficial (no se requiere PG vivo para los hallazgos — son de parseo/compilación). Harness temporal Go en `compat/zz_audit7a*_test.go` + programas `zz_sqlite_*.go` en la raíz del repo, **todos borrados** al terminar (verificados: `git status` limpio salvo este reporte y los artefactos del auditor concurrente AUDIT7-C `.audit7c_*`, que no se tocaron).

## Metodología

Cada hallazgo se ejercitó con un programa Go temporal dentro del paquete `compat` (acceso a símbolos no exportados: `parseCatalogExpression`, `parseCatalogSelect`, `parseSQLiteCatalogTrigger`, `compileExpression`, `compileSelect`, `compileTrigger`, `CompileDDL`) compilando a SQLite y Postgres, y se contrastó contra la **semántica real de SQLite** ejecutando el SQL equivalente con `modernc.org/sqlite` en memoria. La semántica de Postgres se cota de la documentación oficial. Cada hallazgo incluye código del caso y salida real (pegada). Los temporales se borraron tras la verificación.

La matriz de combinaciones probadas está en §2. Los hallazgos con evidencia en §3. Áreas limpias en §4.

---

## 2. Matriz de combinaciones probadas

### 2.1 Expresiones (parseCatalogExpression → compileExpression)

| # | Combinación | Input | Resultado |
|---|---|---|---|
| E1 | hex con signo (two's-comp) en `=` | `col = 0xFFFFFFFFFFFFFFFF` | `("col" = -1)` ambos ✓ |
| E2 | hex min-int en `>=` | `col >= 0x8000000000000000` | `("col" >= -9223372036854775808)` ambos ✓ |
| E3 | hex + NOT + precedencia (`NOT a = hex`) | `NOT col = 0x10` | `(NOT ("col" = 16))` ambos ✓ |
| E4 | hex + NOT LIKE | `NOT col LIKE 0x10` | SQLite `(NOT ("col" LIKE 16))`, PG `ILIKE` ✓ |
| E5 | `=` con `NOT <hex>` a la derecha | `col = NOT 0x10` | `("col" = (NOT 16))` ambos — **ver H2** |
| E6 | hex + AND | `col = 0x10 AND other = 0x20` | `(("col" = 16) AND ("other" = 32))` ✓ |
| E7 | hex two's-comp + AND | `col = 0xFF..F AND other = 0x1` | `(-1 / 1)` ✓ |
| E8 | columna `e5` (gramática numérica) en `=` | `e5 = 1` | `("e5" = 1)` (citada, no decimal) ✓ |
| E9 | columna `E10` | `E10 > 5` | `("E10" > 5)` ✓ |
| E10 | número `1e5` | `1e5 = 1` | `(1e5 = 1)` (decimal) ✓ |
| E11 | `.5`, `1.` | `.5 = 1` / `1. = 1` | decimales ✓ |
| E12 | `1e` (exponente sin dígitos) | `1e = 1` | `("1e" = 1)` citada (SQLite: token no reconocido; coherente con rechazo de SQLite) |
| E13 | columna `"New"` citada (gating) | `"New" = 1` | `("New" = 1)` ✓ (no se pliega a NEW) |
| E14 | `new` lowercase (no-trigger) | `new = 1` | `("new" = 1)` ✓ |
| E15 | `other.new` cualificado | `other.new = 1` | `("other"."new" = 1)` ✓ (no NEW) |
| E16 | allowlist anidadas + hex | `abs(lower(col)) = 0x10` | `(ABS(LOWER("col")) = 16)` ✓ |
| E17 | coalesce + hex | `coalesce(col, 0x10) > 0` | `(COALESCE("col", 16) > 0)` ✓ |
| E18 | replace anidado | `replace(col, 'a', 'b') = 'x'` | `(REPLACE(...) = 'x')` ✓ |
| E19 | IS NULL 1 espacio | `col IS NULL` | `("col" IS NULL)` ✓ |
| E20 | **IS NULL doble espacio** | `col IS  NULL` | **ERR** — ver H1 |
| E21 | **IS NOT NULL doble espacio** | `col IS  NOT  NULL` | **ERR** — ver H1 |
| E22 | **IS NOT NULL con tabs** | `col IS\tNOT\tNULL` | **ERR** — ver H1 |
| E23 | AND doble espacio | `a AND  b` | `("a" AND "b")` ✓ |
| E24 | OR doble espacio | `a OR  b` | `("a" OR "b")` ✓ |
| E25 | LIKE doble espacio | `a LIKE  b` | `(LIKE/ILIKE)` ✓ |
| E26 | NOT LIKE con whitespace | `a NOT  LIKE  b` | `(NOT (LIKE/ILIKE))` ✓ |
| E27 | NOT sobre columna entera | `NOT qty` | `(NOT "qty")` ambos — ver H2 |
| E28 | NOT sobre entero literal | `NOT 1` / `NOT 0` | `(NOT 1)` / `(NOT 0)` — ver H2 |

### 2.2 SELECT de catálogo (parseCatalogSelect → compileSelect)

| # | Combinación | Resultado |
|---|---|---|
| S1 | TODAS las cláusulas en orden normal | `WHERE ... GROUP BY e5 HAVING count(*)>0 ORDER BY a ASC LIMIT 10 OFFSET 5` ✓ ambos |
| S2 | `GROUP  BY` (doble espacio) + col `e5` | tolerado, `"e5"` citada ✓ |
| S3 | `ORDER  BY` (doble espacio) | tolerado ✓ |
| S4 | TODAS las cláusulas con whitespace múltiple | tolerado, idéntico a S1 ✓ |
| S5 | columna `"New"` en vista | `SELECT "New" FROM "t"` ✓ (no NEW) |
| S6 | alias `AS "New"` | `SELECT "a" AS "New"` ✓ |
| S7 | columna `new` en vista | `SELECT "new"` ✓ |
| S8 | **`AS  SELECT` doble espacio** (header de vista) | **ERR** — ver H3 |
| S9 | **`AS\tSELECT` tab** | **ERR** — ver H3 |
| S10 | `GROUP BY` sin operando | ERR "SELECT GROUP BY clause has no operand" ✓ |
| S11 | `WHERE` sin operando | ERR ✓ |
| S12 | `LIMIT` sin operando | ERR ✓ |
| S13 | columna `orderby` (una palabra) + `ORDER BY orderby` | `SELECT "orderby" ... ORDER BY "orderby" ASC` ✓ |
| S14 | columna `"group"` citada + `GROUP BY "group"` | ✓ (no detectada como keyword) |
| S15 | `DISTINCT` | ✓ |
| S16 | `LEFT  OUTER  JOIN ... ON` | `LEFT JOIN` tolerado ✓ |
| S17 | col citada `"offset"` en WHERE | `WHERE ("offset" > 0)` ✓ (no confundida con OFFSET) |
| S18 | col citada `"order"` en WHERE + `GROUP BY "group"` | ✓ |
| S19 | col citada `"order"` en proj + `ORDER BY "order"` | ✓ |
| S20 | col citada `"limit"` en WHERE | `WHERE ("limit" > 0)` ✓ |
| S21 | col citada `"having"` en WHERE | ✓ |
| S22 | **cláusulas desordenadas** (`... ORDER BY a LIMIT 5 WHERE x>0`) | aceptado y reordenado — ver H4 |

### 2.3 Trigger (parseSQLiteCatalogTrigger → compileTrigger)

| # | Combinación | Resultado |
|---|---|---|
| T1 | WHEN hex + allowlist anidadas + IS NOT NULL | `WHEN ((ABS(NEW."x")=16) AND (LOWER(NEW."y") IS NOT NULL))` ✓ ambos |
| T2 | WHEN `NOT new.x = 0xFF..F` (hex+NOT+precedencia) | `WHEN (NOT (NEW."x" = -1))` ✓ |
| T3 | **WHEN `new.x IS  NOT  NULL` (doble espacio)** | **ERR** — ver H1 |
| T4 | action con hex two's-comp | `VALUES (-1)` ✓ ambos |
| T5 | action `new.x` → `NEW."x"` | ✓ (gating correcto en contexto trigger) |

### 2.4 DDL (CompileDDL: vector + familias TEXT-mapeadas + CHECK + índice)

| # | Combinación | Resultado |
|---|---|---|
| D1 | tabla con date/timestamp/json/uuid + vector(3) + col `"New"` + CHECK(NOT "New") | SQLite `TEXT`/`BLOB`/`INTEGER`; PG `TEXT`/`BYTEA`/`BIGINT`/`vector(3)` ✓ |
| D2 | índice sobre `date` (TEXT-mapeada) | `CREATE INDEX ... ("d" ASC)` ✓ |
| D3 | índice parcial `WHERE ("v" IS NOT NULL)` sobre vector | SQLite `TEXT` col + `IS NOT NULL`; PG `vector(3)` ✓ |
| D4 | CHECK `NOT ("New" = 0x10)` (hex+NOT+precedencia, col "New") | `CHECK ((NOT ("New" = 16)))` ✓ ambos |
| D5 | CHECK `"New" >= 0x8000000000000000` (hex min-int) | `CHECK (("New" >= -9223372036854775808))` ✓ ambos |

### 2.5 SQLite real (semántica de referencia, `modernc.org/sqlite` en memoria)

| Query SQLite | Salida real |
|---|---|
| `SELECT 1 WHERE 1 IS  NULL` | (no rows) — **doble espacio IS NULL aceptado** |
| `SELECT 1 WHERE 1 IS  NOT  NULL` | `[1]` — **doble espacio IS NOT NULL aceptado** |
| `SELECT 1 WHERE 1 IS\tNOT\tNULL` | `[1]` — **tabs aceptados** |
| `SELECT NOT 16` | `[0]` — NOT de no-cero = 0 (coerción truthy) |
| `SELECT NOT 0` | `[1]` |
| `SELECT 1 WHERE NOT 16 = 0` | `[1]` |
| `SELECT 0xFFFFFFFFFFFFFFFF` | `[-1]` (int64) — hex two's-comp = -1 |
| `SELECT 0x8000000000000000` | `[-9223372036854775808]` — min int64 |
| `SELECT 0x1e2` | `[482]` |
| `SELECT 1.` / `.5` / `1e5` | `1` (float) / `0.5` / `100000` ✓ |
| `SELECT 1e` | **ERR** "unrecognized token: 1e" |
| `CREATE VIEW v AS  SELECT 1 AS a` | **OK** — doble espacio AS SELECT aceptado |
| `CREATE VIEW v2 AS\tSELECT 1 AS a` | **OK** — tab AS SELECT aceptado |
| `CREATE TABLE t(...) ... WHERE x>0 ORDER BY x LIMIT 1` (cláusulas desordenadas) | **ERR** "near WHERE: syntax error" |
| `CREATE TABLE t("offset" int, ...)` + `WHERE "offset">0` | OK (keyword citada = columna) |
| `... WHERE limit > 0` (sin citar) | **ERR** SQLite: `limit` no es columna válida sin citar |

---

## 3. Hallazgos

### H1 — [MEDIA] `IS NULL` / `IS NOT NULL` rechazan whitespace interno múltiple; el resto de keywords multi-palabra sí lo toleran

**Síntoma.** `splitCatalogOperator` (`compat/sqlselect.go:214`, usado por `parseCatalogExpression` en `sqlparse.go:43`) reconoce los operadores multi-palabra `IS NULL` e `IS NOT NULL` comparando el span completo con `strings.EqualFold(text[start:i+1], operator.token)` — el token lleva **un solo espacio** y no tolera runs de whitespace internos. El commit `a6c406b` ("tolerate whitespace in multi-word keywords") añadió `keywordMatchSpan` (que sí tolera cualquier run de whitespace entre palabras) pero sólo lo usó en `topLevelKeyword` (cláusulas `GROUP BY`, `ORDER BY`, `JOIN`, `FROM`, `WHERE`, `HAVING`, `LIMIT`, `OFFSET`, `ON`, `AS`); **no** lo extendió a los operadores de expresión `IS NULL` / `IS NOT NULL`. El resultado es una inconsistencia nueva esta semana: las cláusulas toleran `GROUP  BY` pero una expresión no tolera `x IS  NULL`.

**Regla SQLite/Postgres.** El tokenizador de ambos trata cualquier run de whitespace (espacios, tabs, newlines) como un separador equivalente a uno. `x IS  NULL` y `x IS\tNOT\tNULL` son válidos. Confirmado en SQLite real (§2.5).

**Alcanzabilidad.** `parseCatalogExpression` es el camino de CHECK, `index WHERE`, `column DEFAULT`, `WHERE`/`HAVING` de vistas, y `WHEN`/actions de triggers. Un schema generado por un pretty-printer (o escrito a mano) con `IS  NULL` en cualquiera de esos sitios es rechazado por el parser aunque SQLite y Postgres lo aceptan.

**Evidencia (programa temporal `compat/zz_audit7a_test.go`, borrado):**

```go
for _, in := range []string{"col IS NULL", "col IS  NULL", "col IS  NOT  NULL", "col IS\tNOT\tNULL"} {
    expr, err := parseCatalogExpression(in)
    if err != nil { fmt.Printf("ERR %q -> %v\n", in, err); continue }
    s, _ := compileExpression(SQLite, expr)
    p, _ := compileExpression(Postgres, expr)
    fmt.Printf("%q -> SQLite=%s PG=%s\n", in, s, p)
}
```

Salida real:
```
"col IS NULL"   -> SQLite=("col" IS NULL)  PG=("col" IS NULL)      # 1 espacio: OK
"col IS  NULL"  -> ERR unsupported catalog expression "col IS  NULL"
"col IS  NOT  NULL" -> ERR unsupported catalog expression "col IS  NOT  NULL"
"col IS\tNOT\tNULL" -> ERR unsupported catalog expression "col IS\tNOT\tNULL"
```

Y en trigger WHEN (programa temporal):
```
TRIG "CREATE TRIGGER tr BEFORE INSERT ON t WHEN new.x IS  NOT  NULL BEGIN ..."
  -> ERR unsupported catalog expression "new.x IS  NOT  NULL"
```

Contraste (SQLite real, §2.5):
```
SELECT 1 WHERE 1 IS  NULL      -> (no rows)   # aceptado
SELECT 1 WHERE 1 IS  NOT  NULL  -> [1]         # aceptado
SELECT 1 WHERE 1 IS\tNOT\tNULL  -> [1]         # aceptado
```

**Por qué MEDIA.** Es un fallo de parseo sobre SQL válido en ambos dialectos, alcanzable en todos los caminos de expresión (CHECK, índices, defaults, vistas, triggers). La inconsistencia es introducida esta semana por el commit de tolerancia a whitespace, que dejó fuera a los operadores `IS NULL`/`IS NOT NULL`. No es silencioso (devuelve error), pero rechaza entradas válidas.

**Sugerencia (READ-ONLY, no aplicada).** Reutilizar `keywordMatchSpan` para tolerar whitespace interno en `splitCatalogOperator` cuando `catalogWordOperator(token)` es multi-palabra (`IS NULL`, `IS NOT NULL`), o normalizar runs de whitespace antes del `EqualFold`.

---

### H2 — [BAJA] `NOT` aplicado a operando no booleano compila a SQL válido en SQLite pero ilegal en PostgreSQL

**Síntoma.** El operador `NOT` (rework de precedencia `b30c183` / `811824a`) es un prefijo unario sobre cualquier sub-expresión; `compileUnaryExpression` (`ddl.go:361`) emite `(NOT <arg>)` sin validar que el operando sea booleano. Combinado con un literal entero/hex o una columna entera, el resultado es válido en SQLite (coerción truthy: `NOT 16 = 0`, `NOT 0 = 1`) pero un **error de tipo** en PostgreSQL.

**Regla Postgres.** Los operadores lógicos `NOT`/`AND`/`OR` requieren operandos `boolean`: `SELECT NOT 16` → `ERROR: argument of NOT must be type boolean, not type integer`. `CREATE TABLE t(qty int, CHECK (NOT qty))` → mismo error al `CREATE TABLE`. SQLite, en cambio, coerce enteros a booleano (0 = falso, no-cero = verdadero), así que `CHECK (NOT qty)` es válido en SQLite.

**Evidencia (programa temporal, borrado):**
```go
for _, in := range []string{"NOT qty", "NOT 0x10", "NOT 1", "NOT 0"} {
    expr, _ := parseCatalogExpression(in)
    s, _ := compileExpression(SQLite, expr)
    p, _ := compileExpression(Postgres, expr)
    fmt.Printf("%q -> SQLite=%s  PG=%s\n", in, s, p)
}
```
Salida real:
```
"NOT qty"   -> SQLite=(NOT "qty")  PG=(NOT "qty")
"NOT 0x10"  -> SQLite=(NOT 16)     PG=(NOT 16)
"NOT 1"     -> SQLite=(NOT 1)      PG=(NOT 1)
"NOT 0"     -> SQLite=(NOT 0)      PG=(NOT 0)
```
Y `col = NOT 0x10` → `("col" = (NOT 16))` en ambos engines. SQLite real: `NOT 16 = 0` (§2.5). PostgreSQL rechazaría `(NOT 16)` y `CHECK (NOT "qty")` al crear la tabla.

**Por qué BAJA.** El mecanismo `NOT` unario sobre operandos no booleanos **predesigna parcialmente** a esta semana (el soporte de `NOT` y su precedencia vienen de rondas previas; el rework de precedencia es reciente). La superficie de interacción ejercitada aquí es `NOT × hex/integer-literal × tipado estricto de PG`. Es una divergencia silenciosa real (SQLite acepta, PG rechaza al `CREATE TABLE`), pero requiere input poco común (`CHECK (NOT <int_col>)` o `NOT <hex>`); el caso común `NOT <bool_col>` es correcto. Se reporta por honestidad como divergencia latente, no como interacción recién introducida.

**Sugerencia (READ-ONLY, no aplicada).** Documentar como limitación conocida (coerción truthy de SQLite vs tipado estricto de PG en `NOT`/`AND`/`OR`), o rechazar `NOT`/`AND`/`OR` cuyo operando no sea claramente booleano cuando se compila a Postgres — requiere contexto de tipo de columna del que el compilador de expresiones hoy carece.

---

### H3 — [BAJA] Header de vista `AS SELECT` no tolera whitespace múltiple, a diferencia de las cláusulas

**Síntoma.** `stripCatalogSelectHeader` (`sqlselect.go:74`) detecta el límite `AS SELECT` con `strings.Index(upper, " AS SELECT ")` — busca el literal con **un solo espacio** a cada lado. El commit `a6c406b` hizo que las cláusulas multi-palabra (`GROUP BY`, `ORDER BY`, `LEFT OUTER JOIN`) toleraran runs de whitespace vía `keywordMatchSpan`, pero el header `AS SELECT` quedó con el matcher estricto. Así `CREATE VIEW v AS  SELECT a FROM t` (doble espacio) y `AS\tSELECT` (tab) se rechazan con "view definition has no AS SELECT".

**Regla SQLite/Postgres.** Ambos aceptan cualquier whitespace entre `AS` y `SELECT`. Confirmado en SQLite real (§2.5): `CREATE VIEW v AS  SELECT 1 AS a` → OK; `CREATE VIEW v2 AS\tSELECT 1 AS a` → OK.

**Evidencia (programa temporal, borrado):**
```
DEF "CREATE VIEW v AS  SELECT a FROM t" -> ERR view definition has no AS SELECT
DEF "CREATE VIEW v AS\tSELECT a FROM t" -> ERR view definition has no AS SELECT
```

**Por qué BAJA.** Camino angosto (sólo el header de definición de vista inspeccionada nativamente) y requiere whitespace inusual entre `AS` y `SELECT`. Mismo tema que H1 (tolerancia a whitespace inconsistente entre keywords multi-palabra), pero en otra función; no es silencioso.

**Sugerencia (READ-ONLY, no aplicada).** Reemplazar `strings.Index(upper, " AS SELECT ")` por un `keywordMatchSpan`-estilo que tolere runs de whitespace entre `AS` y `SELECT` (y normalice), coherente con el resto del parser.

---

### H4 — [BAJA] Cláusulas SELECT desordenadas son aceptadas y silenciosamente reordenadas

**Síntoma.** `locateCatalogClauses` (`sqlselect.go:124`) ordena las cláusulas halladas por **posición** en el fuente y `applyCatalogClauses` las aplica en ese orden; `compileSelect` luego las **reemite** en el orden canónico fijo (WHERE → GROUP BY → HAVING → ORDER BY → LIMIT → OFFSET). Resultado: `SELECT a FROM t ORDER BY a LIMIT 5 WHERE x > 0` (cláusulas fuera de orden) se acepta y compila a `SELECT "a" FROM "t" WHERE ("x" > 0) ORDER BY "a" ASC LIMIT 5`. SQLite **rechaza** el original con syntax error (§2.5).

**Evidencia (programa temporal, borrado):**
```
DEF "SELECT a FROM t ORDER BY a LIMIT 5 WHERE x > 0"
  SQLite: SELECT "a" FROM "t" WHERE ("x" > 0) ORDER BY "a" ASC LIMIT 5
  PG    : SELECT "a" FROM "t" WHERE ("x" > 0) ORDER BY "a" ASC LIMIT 5
DEF "SELECT a FROM t ORDER BY a GROUP BY a"
  SQLite: SELECT "a" FROM "t" GROUP BY "a" ORDER BY "a" ASC
DEF "SELECT a FROM t LIMIT 5 WHERE x > 0"
  SQLite: SELECT "a" FROM "t" WHERE ("x" > 0) LIMIT 5
DEF "SELECT a FROM t OFFSET 5 LIMIT 10"
  SQLite: SELECT "a" FROM "t" LIMIT 10 OFFSET 5
```
SQLite real: `... ORDER BY x LIMIT 1 WHERE x > 0` → `ERR near "WHERE": syntax error`.

**Por qué BAJA (y nota de procedencia).** El sort por posición en `locateCatalogClauses` **predesigna** a esta ronda (no es una interacción de los cambios de esta semana). El comportamiento resulta en sobre-aceptación: acepta SQL que SQLite rechazaría y lo reordena. Semanticamente la reordenación es equivalente (el intento del autor), pero viola el contrato de "rechazar todo lo que esté fuera de la gramática canónica". Se reporta por honestidad como observación, no como interacción recién introducida; no es silencioso en el sentido de corrupto (la salida es SQL válido y equivalente), pero sí en el sentido de sobre-aceptación.

**Sugerencia (READ-ONLY, no aplicada).** Validar el orden de las cláusulas halladas contra el orden canónico declarado en `catalogSelectClauses` y rechazar si difieren, en lugar de reordenar.

---

## 4. Áreas limpias (combinaciones probadas sin hallazgo)

- **Hex con signo (two's-complement) en CHECK con NOT y precedencia.** `0xFFFFFFFFFFFFFFFF` → `-1`, `0x8000000000000000` → `-9223372036854775808` en ambos engines (D4, D5, E1–E7). Coincide con SQLite real (§2.5). El caso `NOT col = 0x10` parsea como `not(eq(col,16))` (NO como `eq(not(col),16)`) — la precedencia de NOT es correcta.
- **Columna `e5`/`E10` (gramática numérica) en `GROUP BY` con whitespace múltiple.** `isCatalogNumber` rechaza `e5`/`E10` (sin dígito de mantisa) → se tratan como columnas y se citan `"e5"`/`"E10"`. `GROUP  BY` (doble espacio) se tolera vía `keywordMatchSpan` (S2). SQLite real confirma que `e5` sería columna y que los whitespace múltiples son válidos.
- **Columna `"New"` citada (gating NEW/OLD) en vistas con keywords multi-palabra.** En contexto no-trigger, `"New"`, `new` y `new.new` se citan y NO se pliegan a `NEW`/`OLD` (E13–E15, S5–S7). El gating por `compileExpressionIn(inTrigger=false)` es correcto; `other.new` (segmento no-líder) se cita, no se promueve a NEW.
- **Trigger cuyo WHEN combina hex + funciones del allowlist anidadas.** `abs(lower(new.x)) = 0x10 AND lower(new.y) IS NOT NULL` → `WHEN ((ABS(NEW."x")=16) AND (LOWER(NEW."y") IS NOT NULL))` en ambos engines; `new.x` resuelve a `NEW."x"` en contexto trigger (T1). `coalesce(col, 0x10)` y `replace` anidado también correctos (E16–E18).
- **Familia vector en CHECK/índices junto a las familias nuevas TEXT-mapeadas (date, timestamp, json, uuid).** `vector(3)` → `vector(3)` en PG y `TEXT` en SQLite; date/ts/json/uuid → `TEXT` en ambos; `BYTEA`/`BIGINT`/`INTEGER`/`BLOB` correctos (D1). Índice parcial `WHERE ("v" IS NOT NULL)` sobre vector compila en ambos (D2–D3). `Schema.Validate` exige `vector` con un solo argumento positivo y rechaza familias desconocidas.
- **Identificadores unicode/citados en caminos nuevos.** `parseCatalogIdentifier` rutea por runes (`unicode.IsLetter`), así que `café` se cita y compila correctamente en expresiones. Los identificadores citados que colisionan con keywords (`"offset"`, `"order"`, `"group"`, `"limit"`, `"having"`) en WHERE/GROUP BY/ORDER BY no se confunden con keywords (el tracking `inDouble` de `topLevelKeyword` los salta) y se citan correctamente (S17–S21). (Nota: el regex `catalogIdentifierPattern` de `sqltrigger.go` sólo acepta ASCII sin citar — preexistente a esta ronda, no parte de los cambios auditados; los identificadores unicode citados en triggers sí pasan por la rama `"..."`.)
- **SELECT de catálogo con TODAS las cláusulas a la vez.** En orden normal y con whitespace múltiple en cada keyword, compila idéntico en SQLite y Postgres (S1, S4). `DISTINCT`, `ORDER BY ... ASC/DESC`, `LIMIT`/`OFFSET` no-negativos, `JOIN ... ON` y `LEFT OUTER JOIN` con whitespace tolerado, todos correctos.
- **Operandos vacíos rechazados.** `GROUP BY`/`WHERE`/`LIMIT` sin operando → error explícito (S10–S12), coherente con el commit `0cfadef` y con SQLite.
- **`NOT LIKE` infix con whitespace múltiple.** `a NOT  LIKE  b` → `not(like(a,b))` (E26), tanto infix como prefijo `NOT a LIKE b` coinciden.
- **Keywords multi-palabra con whitespace.** `GROUP  BY`, `ORDER  BY`, `LEFT  OUTER  JOIN`, `IS  NULL`-fuera-de-cláusula... (las cláusulas) toleran runs de whitespace (S2–S4, S16). Sólo `IS NULL`/`IS NOT NULL` en expresiones (H1) y `AS SELECT` (H3) quedan estrictos.
- **Esquemas con nombres que colisionan con keywords (citados).** Vistas/tablas/índices con nombres citados que colisionan con keywords se manejan; el qualifier de schema de trigger sólo acepta `public` (citado o no, case-insensitive sólo sin citar) y rechaza el resto (ver `parsePostgresCatalogTrigger`).

---

## 5. Limpieza y verificación final

- Harness temporal `compat/zz_audit7a*_test.go` y programas `zz_sqlite_*.go`: **borrados**. `go build ./...` OK. `go test ./compat/` (suite existente) sin modificar.
- `git status` final: árbol limpio salvo `docs/reports/AUDIT7-A.md` (este reporte) y los artefactos `.audit7c_*` del auditor concurrente AUDIT7-C, que no se tocaron conforme las instrucciones. No `git add`/`commit`.
- No se ejecutó ningún proceso en foreground que no terminara solo; nada se escribió fuera del repo excepto `/tmp/zz_sqlite_sem.go` (tempdir del SO, borrado). No se loguearon secretos (no se usó DSN de Postgres).

## Conteo final por severidad

- **ALTA: 0**
- **MEDIA: 1** (H1 — `IS NULL`/`IS NOT NULL` rechazan whitespace interno múltiple)
- **BAJA: 3** (H2 — `NOT` sobre no-booleano: SQLite-válido/PG-inválido; H3 — header `AS SELECT` no tolera whitespace múltiple; H4 — cláusulas desordenadas aceptadas y reordenadas)