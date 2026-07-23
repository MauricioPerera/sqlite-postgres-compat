# FEAT-CUBOA-1 — Extensión de la gramática de expresiones (BETWEEN / IN / CASE / nullif)

**Fecha:** 2026-07-23
**Ámbito:** ampliar la gramática canónica de expresiones (`compat/sqlparse.go` → `compat/ddl.go`) con predicados y funciones nuevos, cada uno con semántica idéntica verificable en SQLite (modernc) **y** PostgreSQL 17 real.
**Motor de PostgreSQL usado para verificación:** PostgreSQL 17 en `postgres://postgres:***@31.220.22.176:5434/postgres` (password enmascarada). Bases efímeras creadas y **dropeadas** por el harness E2E (`TestMain`); verificado 0 bases `compat_e2e_%`/`cuboa1_%` remanentes al terminar.
**Archivos tocados:** `compat/sqlparse.go`, `compat/sqlparse_test.go`, `compat/ddl.go`, `compat/ddl_test.go`, `e2e/system_test.go`, `README.md`, `docs/TESTING.md`, `docs/COMPATIBILITY.md`, `AGENTS.md`, este reporte. No se tocó `schema.go`, `sqlselect.go` (sólo lectura/reuso de helpers), `inspect.go`, `store.go`, `cmd/**`, ni reportes existentes.

---

## 1. Resumen ejecutivo

| Constructo | Decisión | Motivo |
|---|---|---|
| `BETWEEN` / `NOT BETWEEN` | **ENTRA** | `BETWEEN` nativo, inclusivo (`x <= a <= y`), idéntico en ambos motores. |
| `IN (lista)` / `NOT IN (lista)` | **ENTRA** | `IN` nativo sobre lista de valores; pertenencia y lógica de 3 valores idénticas. |
| `CASE` searched | **ENTRA** | `CASE WHEN … THEN … [ELSE …] END` nativo, evaluación por primera coincidencia idéntica. |
| `nullif(a, b)` | **ENTRA** | Estándar; byte-idéntico en las pruebas contra PG real. |
| `substr` / `substring` | **DESCARTADO** | Índices/longitud negativos divergen (evidencia §4). |
| `cast(x AS …)` | **DESCARTADO** | `cast(... AS integer)` trunca (SQLite) vs redondea (PG) (evidencia §4). |
| `round(x[, n])` | **DESCARTADO** | `double precision` half-to-even (PG) vs half-away-from-zero (SQLite) (evidencia §4). |
| `IS DISTINCT FROM` | **DIFERIDO** | Requiere version-gating SQLite ≥ 3.39; fuera de alcance de este lote. |

Regla de oro respetada: sólo se agregó lo que se pudo compilar y **verificar equivalente** contra PostgreSQL 17 real. Todo lo que diverge se rechaza como cualquier función fuera de la allowlist (no se acepta en silencio).

---

## 2. Diseño por constructo

Patrón seguido (idéntico al existente): nuevo `Kind` producido en `parseCatalogExpression` + su rama en `compileExpressionIn`, reusando `quoteIdentifier`/`compileExpressionArgs`. Precedencia: `BETWEEN`/`IN` al nivel de las comparaciones; `CASE` es un primario opaco. Comentarios de equivalencia en inglés en el código.

### 2.1 BETWEEN / NOT BETWEEN — `Kind: "between"` / `"not_between"`, `Args = [operand, low, high]`

**Diseño elegido: emitir `BETWEEN` nativo en ambos motores** (no desugar a `and(gte, lte)`).

- **Por qué nativo y no desugar.** `x BETWEEN a AND b` es, por definición del estándar, exactamente `x >= a AND x <= b`, inclusivo, en SQLite y PostgreSQL. Como la gramática no tiene funciones volátiles ni efectos secundarios (el único no-determinismo posible, `CURRENT_TIMESTAMP`, es un literal), evaluar `x` una vez (nativo) o dos (desugar) da el mismo resultado. El nativo preserva la intención del autor, produce SQL más legible y es exactamente equivalente. `NOT BETWEEN` se emite igualmente nativo.
- **Reto de parseo (resuelto).** El `AND` que delimita el `BETWEEN` NO debe partir el predicado en el nivel del `AND` lógico. Se añadió `betweenDelimiterANDs`, que empareja cada `BETWEEN` con su `AND` mediante un contador de izquierda a derecha (una cota de `BETWEEN` no puede contener un `AND` de nivel superior sin paréntesis, así que el emparejamiento es exacto). `splitCatalogOperator` salta esos `AND` sólo cuando el nivel incluye el operador `and`. Precedencia verificada: `price BETWEEN low AND high AND active = TRUE` → `and(between(price, low, high), eq(active, TRUE))`.

### 2.2 IN / NOT IN con lista de valores — `Kind: "in"` / `"not_in"`, `Args = [operand, v1, v2, …]`

**Diseño elegido: emitir `IN (…)` nativo en ambos motores.**

- **Alcance.** Sólo **lista de valores explícita**. Las subconsultas quedan explícitamente fuera (otro lote): cualquier elemento que no sea una expresión de la gramática (p.ej. `SELECT …`) falla al parsear y el predicado se rechaza entero. Lista vacía `()` rechazada.
- **Equivalencia.** La semántica de pertenencia, incluida la lógica de tres valores con `NULL` (`NULL IN (…)` → `NULL`; `a IN (…, NULL)` puede dar `NULL`), es idéntica en ambos motores (ambos implementan el estándar SQL). Verificado en el E2E con filas `NULL` (§3).
- **Parseo.** `findCatalogKeyword` localiza el `IN` de nivel superior (fuera de paréntesis, comillas y spans de `CASE`); la lista se parte con `splitTopLevelCommas`, de modo que `'x,y'` (coma dentro de string) sobrevive intacta.

### 2.3 CASE searched — `Kind: "case"`, `Args = [c1, v1, c2, v2, …, (else)]`

**Diseño del layout de `Args` (documentado en código y en AGENTS.md):** pares `WHEN/THEN` consecutivos; si `len(Args)` es **impar**, el último elemento es el valor `ELSE`; si es **par**, no hay `ELSE`. Este layout evita un campo extra en el AST (`Expression` sólo tiene `Kind/Value/Args`) y es inequívoco.

- **Sólo la forma searched.** `CASE WHEN cond THEN val …`. La forma simple con operando (`CASE x WHEN v …`) se rechaza: sólo la searched evita sorpresas de coerción de tipo del operando y es idéntica en ambos motores sin condiciones.
- **Compilación.** `CASE WHEN … THEN … [ELSE …] END` nativo. El `END` explícito hace el constructo auto-delimitado, así que no hacen falta paréntesis extra para la precedencia cuando se anida (p.ej. `(CASE … END = 1)` lo envuelve el compilador binario).
- **Reto de parseo (resuelto).** Un `CASE … END` es **opaco** para los splitters de operadores: sus `WHEN/THEN` llevan comparaciones y `AND`/`OR` propios que no deben confundirse con operadores de nivel superior. Se añadió `caseSpanMask`, que marca cada byte dentro de un `CASE … END` de nivel superior (con anidamiento), igual que ya se ignoran paréntesis y comillas. Un `END` sin `CASE` abierto se ignora, de modo que una columna citada `"end"` no dispara un span espurio. Verificado: `CASE WHEN x BETWEEN 1 AND 2 THEN 1 ELSE 0 END` desambigua correctamente el `AND` del `BETWEEN` frente al `ELSE`/`END` del `CASE`; y `CASE … END = 1` parte primero en el `=` de nivel superior.

### 2.4 nullif — `Kind: "nullif"`, `Args = [a, b]`

Se sumó a la allowlist de funciones (exactamente dos argumentos). Compila a `NULLIF(a, b)` en ambos motores. Estándar e idéntico; verificado byte-idéntico contra PG real (§4).

---

## 3. Verificación

### 3.1 `.\scripts\check.ps1` (gofmt + vet + unit tests) — VERDE

```
gofmt: OK
go vet: OK
?   	example.com/sqlite-postgres-compat/cmd/compat	[no test files]
ok  	example.com/sqlite-postgres-compat/cmd/internal/cliout	2.368s
ok  	example.com/sqlite-postgres-compat/compat	2.385s
go test: OK
```

`go vet -tags=e2e ./e2e` → sin salida (VERDE).

### 3.2 Tests unitarios congelados (parse + compile en ambos motores)

`compat/sqlparse_test.go` (parse → AST esperado) y `compat/ddl_test.go` (compile → SQL exacto por motor), table-driven:

```
--- PASS: TestParseCatalogExpressionBetween (0.00s)
    /simple_between /not_between /between_with_column_bounds
    /between_binds_tighter_than_logical_AND /prefix_NOT_over_between_folds_to_not(between)
--- PASS: TestParseCatalogExpressionIn (0.00s)
    /integer_list /not_in_string_list /single_value_list
    /in_composes_with_OR_at_comparison_precedence /list_preserves_string_with_comma
--- PASS: TestParseCatalogExpressionInRejects (0.00s)          # IN () y IN (SELECT …) rechazados
--- PASS: TestParseCatalogExpressionCase (0.00s)
    /two_branches_with_else /single_branch_without_else
    /case_as_operand_of_a_comparison /between_inside_a_case_branch_does_not_leak_its_AND
--- PASS: TestParseCatalogExpressionCaseRejects (0.00s)        # simple CASE, ELSE/THEN vacío, sin END, sin WHEN
--- PASS: TestParseCatalogExpressionNullif (0.00s)
--- PASS: TestParseCatalogExpressionDiscardedFunctionsStayRejected (0.00s)  # round/substr/cast rechazados
--- PASS: TestParseCatalogExpressionColumnNamedLikeKeyword (0.00s)          # "main"/"end" no confundidos
--- PASS: TestCompileBetweenInCaseNullifForBothEngines (0.00s)
    /between /not_between /in_list /not_in_list /case_with_else /case_without_else /nullif
--- PASS: TestCompileInLikeInsideCaseKeepsIlikeMapping (0.00s)              # LIKE→ILIKE se hereda dentro de CASE
PASS
ok  	example.com/sqlite-postgres-compat/compat	2.309s
```

SQL exacto asertado (idéntico en SQLite y PostgreSQL para estos constructos):

| Constructo | SQL emitido (ambos motores) |
|---|---|
| `between` | `("price" BETWEEN 0 AND 100)` |
| `not_between` | `("price" NOT BETWEEN "lo" AND "hi")` |
| `in` | `("status" IN (1, 2, 3))` |
| `not_in` | `("code" NOT IN ('x', 'y'))` |
| `case` (con ELSE) | `CASE WHEN ("a" > 1) THEN 'big' WHEN ("a" = 1) THEN 'one' ELSE 'small' END` |
| `case` (sin ELSE) | `CASE WHEN ("a" > 1) THEN 'big' END` |
| `nullif` | `NULLIF("a", 0)` |

(`LIKE` anidado en cualquiera de estos constructos sigue mapeando a `ILIKE` en PostgreSQL — verificado.)

### 3.3 Prueba de equivalencia contra PostgreSQL 17 REAL (E2E)

`TestSystemCuboaConstructsProduceEquivalentResults` (tag `e2e`, en `e2e/system_test.go`). Hace dos cosas contra PG real, no sólo compilar:

1. **DDL aceptado por PG real.** Aplica una tabla `cuboa1_items` con **un CHECK por cada constructo nuevo** (`between`, `not_between`, `in`, `not_in`, `nullif`, y un `case` anidado en un `in`). `ImportSnapshot` crea esa tabla en PostgreSQL real: si algún constructo compilara a SQL que PG rechazara, fallaría aquí. Import verde ⇒ PG acepta el DDL de los seis.
2. **Semántica en runtime idéntica.** Dos vistas sobre datos idénticos (incluidas filas con `NULL` que disparan lógica de tres valores):
   - `cuboa1_filtered`: `WHERE` con `BETWEEN` + `NOT BETWEEN` + `IN` + `NOT IN`. Se comparan los `id` devueltos por SQLite vs PG.
   - `cuboa1_projected`: proyección con `CASE` (texto) y `nullif` (int/NULL), representaciones neutrales entre motores. Se comparan fila por fila.

Ejecutado DE VERDAD contra el DSN dado:

```
=== RUN   TestSystemCuboaConstructsProduceEquivalentResults
--- PASS: TestSystemCuboaConstructsProduceEquivalentResults (1.58s)
```

Suite E2E completa contra PG real (`go test -tags=e2e ./e2e -count=1`): 39 pruebas de nivel superior superadas y **1 fallida de forma intencional** (`TestSystemClaimsExactCoverageForRequiredFeatureFamilies`, el gate del 100 % que sigue rojo por diseño). Sin ninguna otra falla. 0 bases de datos temporales remanentes en PG tras la corrida.

---

## 4. Funciones descartadas — evidencia real (PostgreSQL 17)

Probe temporal (borrado) que ejecutó el mismo SQL en SQLite (modernc, memoria) y en PostgreSQL 17 real. `match=true` sólo si ambos devuelven exactamente lo mismo sin error.

### 4.1 round — DESCARTADO (half-to-even vs half-away-from-zero)

Con literal `numeric` coincide, PERO con `double precision` diverge (PG usa banker's rounding):

```
Q: SELECT round(cast(2.5 AS float))   SQLite=3   PG=2   match=false
Q: SELECT round(cast(0.5 AS float))   SQLite=1   PG=0   match=false
Q: SELECT round(cast(4.5 AS float))   SQLite=5   PG=4   match=false
Q: SELECT round(cast(3.5 AS float))   SQLite=4   PG=4   match=true   (coincide por casualidad)
```

SQLite redondea siempre half-away-from-zero; PostgreSQL `round(double precision)` redondea half-to-even. Como no se puede acotar el tipo/valor de una columna en tiempo de compilación, `round` divergiría en silencio. **No se agrega.** (Confirma la advertencia del PM con evidencia real.)

### 4.2 substr / substring — DESCARTADO (índices y longitud negativos)

El indexado 1-based positivo coincide, pero los argumentos negativos divergen:

```
Q: SELECT substr('hello', 2)      SQLite=ello  PG=ello   match=true
Q: SELECT substr('hello', 2, 3)   SQLite=ell   PG=ell    match=true
Q: SELECT substr('hello', 0, 3)   SQLite=he    PG=he     match=true
Q: SELECT substr('hello', -2, 2)  SQLite=lo    PG=(vacío)          match=false
Q: SELECT substr('hello', -2)     SQLite=lo    PG=hello            match=false
Q: SELECT substr('hello', 2, -1)  SQLite=h     PG=ERROR (negative substring length not allowed) match=false
```

Con `start` negativo SQLite cuenta desde el final mientras PG lo trata distinto; con `length` negativa SQLite recorta y PG lanza error. La gramática admite columnas como argumentos, cuyos valores negativos no se pueden descartar en compilación. **No se agrega.**

### 4.3 cast — DESCARTADO (truncar vs redondear a entero)

```
Q: SELECT cast(3.7 AS integer)   SQLite=3   PG=4   match=false
Q: SELECT cast('42' AS integer)  SQLite=42  PG=42  match=true
Q: SELECT cast(42 AS text)       SQLite=42  PG=42  match=true
```

`cast(<real> AS integer)`: SQLite trunca hacia cero (`3.7`→`3`), PostgreSQL redondea (`3.7`→`4`). Los casts a `text`/`float` de valores flotantes tienen además riesgo de formato. No hay un subconjunto que se pueda garantizar byte-idéntico sin acotar el tipo/valor runtime de la columna. **No se agrega** (ni siquiera un subconjunto: se prefiere honestidad a cleverness frágil).

### 4.4 nullif — VERIFICADO IDÉNTICO (se agrega)

```
Q: SELECT nullif(5, 6)   SQLite=5     PG=5     match=true
Q: SELECT nullif(3, 4)   SQLite=3     PG=3     match=true
Q: SELECT nullif(5, 5)   SQLite=NULL  PG=NULL  match (ambos NULL)
Q: SELECT nullif('a','a')SQLite=NULL  PG=NULL  match (ambos NULL)
```

---

## 5. Trade-offs y notas

- **BETWEEN/IN nativos vs desugar.** Se eligió nativo por legibilidad y por preservar intención; la equivalencia es exacta porque la gramática carece de expresiones volátiles/con efectos. Si en el futuro se admitieran funciones no deterministas, habría que reevaluar (una sola evaluación del operando pasaría a importar).
- **`CASE` opaco vía máscara.** `caseSpanMask` recomputa una máscara por llamada a `splitCatalogOperator`/detección de `BETWEEN`/`IN`. Es O(n) sobre entradas de tamaño de esquema (pequeñas); se privilegió claridad y correctitud sobre micro-optimización. No cambia firmas públicas.
- **Regresión evitada.** Un `END`/`CASE` suelto (columna citada `"end"`, o `end = 1` sin `CASE`) NO abre span: `caseSpanMask` sólo marca `CASE … END` emparejados; test `TestParseCatalogExpressionColumnNamedLikeKeyword` lo congela.
- **Compatibilidad con triggers.** `splitCatalogOperator` también lo usa `sqltrigger.go` (sólo con el operador `=`): la lógica de `betweenDelimiterANDs` sólo actúa cuando el nivel incluye `and`, y la máscara sólo marca `CASE … END` reales, así que ese camino queda inalterado (suite completa verde).
- **Diferido explícito:** `IS DISTINCT FROM` (necesita gating SQLite ≥ 3.39) no entra en este lote.

---

## 6. Limpieza

- Probes temporales (`compat/zz_probe_test.go`, `compat/zz_check_test.go`) y programa de chequeo de bases: **borrados**. `git status` sólo muestra los archivos permitidos modificados + este reporte.
- Sin procesos en foreground que no terminen solos. Nada escrito fuera del repo. Password del DSN nunca en literal (enmascarada `***`). 0 bases `compat_e2e_%`/`cuboa1_%` remanentes en PostgreSQL.
