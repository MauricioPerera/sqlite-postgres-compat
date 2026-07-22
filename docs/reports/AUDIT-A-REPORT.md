# AUDIT-A-REPORT — Auditoría READ-ONLY del núcleo de traducción SQL

Scope auditado (solo lectura): `compat/sqlparse.go`, `compat/sqlselect.go`,
`compat/sqlroutine.go`, `compat/sqltrigger.go`, `compat/ddl.go` y sus `*_test.go`.
No se modificó ningún archivo del repo. Tests temporales se ejecutaron en `%TEMP%`
con copias verbatim de `sqlparse.go`/`sqlselect.go` + stubs mínimos de tipos
(ver "Trade-offs").

Resumen de severidad: **ALTA: 2 · MEDIA: 3 · BAJA: 5** (además: cobertura de
tests e inconsistencias, listadas en sus secciones).

---

## (a) Hallazgos

### H1 — ALTA — Precedencia de `NOT` incorrecta (traducción silenciosa de SQL válido)
- **Archivo:línea:** `compat/sqlparse.go:45-51` (prefijo `NOT` aplicado *después*
  del split de comparación), con el orden de niveles en `sqlparse.go:21-28`.
- **Descripción:** `NOT` se parsea como prefijo de máxima precedencia (se aplica
  sólo cuando ningún operador binario matchea). En SQL estándar/SQLite `NOT` tiene
  precedencia *menor* que los operadores de comparación e `IS NULL`. Por eso
  `NOT a = b` → `eq(not(a), b)` = `(NOT a) = b`, cuando SQLite lo evalúa como
  `NOT (a = b)`. Afecta a vistas (WHERE/HAVING), `WHEN` de triggers, CHECK y
  defaults de Postgres, y WHERE/valores de acciones de trigger, pues todos usan
  `parseCatalogExpression`.
- **Evidencia (go test real en %TEMP%, copia verbatim de sqlparse.go):**
```
"NOT a = b"        -> eq(not(column("a")),column("b")))        // SQLite: NOT(a=b)
"NOT a IS NULL"    -> is_null(not(column("a")))               // SQLite: NOT(a IS NULL)
"NOT a LIKE 'x%'"  -> like(not(column("a")),string("x%"))     // SQLite: NOT(a LIKE 'x%')
"a AND NOT b = c"  -> and(column("a"),eq(not(column("b")),column("c")))  // SQLite: a AND (NOT(b=c))
```
  `go test -run TestNotPrecedence` PASS (sin error: la entrada se acepta y se
  arboliza mal). Compilado a Postgres, `NOT a = b` sale `((NOT "a") = "b")`
  (ver `ddl.go:271,282`).
- **SQL de entrada / salida incorrecta:** Entrada `WHERE NOT a = b`
  → salida `WHERE ((NOT "a") = "b")` (debería ser `WHERE (NOT ("a" = "b"))`).

### H2 — ALTA — Literal hexadecimal `0x...` de SQLite se traduce como referencia a columna
- **Archivo:línea:** `compat/sqlparse.go:64-69` (`isCatalogNumber` rechaza `x`)
  y `sqlparse.go:266-285` (`parseCatalogIdentifier` acepta alfanuméricos). Emisión
  en `ddl.go:240-252`.
- **Descripción:** SQLite admite literales enteros hexadecimales (`0x10` = 16).
  `isCatalogNumber("0x10")` devuelve `false` (`x` no está en `.+-eE` ni es dígito),
  y `parseCatalogIdentifier` acepta `0x10` como identificador de columna. El
  resultado se compila (vía `quoteIdentifier`) como `"0x10"`, i.e. una referencia
  a columna inexistente en vez del literal 16. Pérdida silenciosa y catastrófica.
- **Evidencia (go test real en %TEMP%):**
```
"status = 0x10"     -> eq(column("status"),column("0x10"))
"x = 0xABCDEF"      -> eq(column("x"),column("0xABCDEF"))
```
  Compilado a Postgres → `"status" = "0x10"` (columna) en lugar de `"status" = 16`.
- **SQL de entrada / salida incorrecta:** Entrada `WHERE status = 0x10`
  → salida `WHERE ("status" = "0x10")` (debería ser `WHERE ("status" = 16)`).

### H3 — MEDIA — `LIKE` pierde la semántica insensible a mayúsculas de SQLite
- **Archivo:línea:** `compat/ddl.go:255` y `compat/ddl.go:269` (`"like": "LIKE"`,
  sin分支 por motor).
- **Descripción:** `compileExpression` emite `LIKE` literal para ambos motores.
  En SQLite `LIKE` es insensible a mayúsculas (ASCII) por defecto; en Postgres
  `LIKE` es sensible a mayúsculas. Una vista/trigger/CHECK/where con `LIKE`
  traducida a Postgres cambia resultados sin error. No hay `ILIKE` ni `COLLATE`.
- **Verificación:** POR LECTURA (no ejecutado). La línea es incondicional; no
  existe rama que distinga motores ni manejo de `ILIKE`/`COLLATE` en el archivo.
- **SQL de entrada / salida incorrecta:** Entrada `WHERE name LIKE 'X%'`
  → salida `WHERE ("name" LIKE 'X%')` (mismo texto, semántica distinta: Postgres
  ya no matchea `'x...'`). NO VERIFICADO por ejecución; afirmado por lectura.

### H4 — MEDIA — Inconsistencia: trigger de Postgres descarta el esquema; SELECT lo rechaza
- **Archivo:línea:** `compat/sqltrigger.go:13` (`postgresTriggerPattern` con
  `(?:<id>\.)?` no captura el esquema) y `sqltrigger.go:53-55` (usa `match[4]`
  como tabla). Contraste: `compat/sqlselect.go:237` (`parseCatalogTableSource`
  rechaza cualquier `.`).
- **Descripción:** `parsePostgresCatalogTrigger` acepta `public.products` y deja
  `trigger.Table = "products"` (esquema `public` descornado silenciosamente), lo
  que al recompilar pierde el calificador de esquema (depende del `search_path`).
  `parseCatalogSelect`/`parseCatalogTableSource` en cambio **rechazan** cualquier
  nombre calificado con punto. Mismo concepto, dos comportamientos opuestos sin
  documentación en el scope.
- **Evidencia:** `TestParsePostgresCatalogTrigger` (repo) usa entrada
  `public.products` y asserts `trigger.Table == "products"` (confirma descarte por
  diseño). Test temporal en %TEMP%: `"SELECT a FROM public.t"` →
  `err=unsupported table source "public.t"` (confirma rechazo en SELECT).

### H5 — MEDIA — `LIMIT -1` / `OFFSET` negativo aceptados silenciosamente al parsear
- **Archivo:línea:** `compat/sqlselect.go:80-89` (Atoi sin validación de signo);
  el rechazo ocurre recién al compilar en `ddl.go:380-389`.
- **Descripción:** En SQLite `LIMIT -1` significa "sin límite" (todas las filas).
  `parseCatalogSelect` lo parsea sin error (`Limit = -1`) y guarda el valor
  equivocado; sólo `compileSelect` rechaza después (`limit must be non-negative`).
  Si un consumidor sólo inspecciona el AST, lo malinterpreta; y el significado
  real (sin límite) se pierde sin mensaje claro de "no soportado".
- **Evidencia (go test real en %TEMP%):** `"SELECT a FROM t LIMIT -1"` →
  `err=<nil>` (parsea sin error).
- **SQL de entrada / salida:** Entrada `SELECT a FROM t LIMIT -1` → parsea OK
  con `Limit=-1`; compila a error. Semántica SQLite (ilimitado) no preservada.

### H6 — BAJA — `SELECT *` y `SELECT t.*` rechazados (construcción común de vistas)
- **Archivo:línea:** `compat/sqlselect.go:130-137` → `parseCatalogExpression("*")`
  falla en `sqlparse.go:90`.
- **Descripción:** `*` y `t.*` no son expresiones válidas en el grammar canónico;
  se rechazan con error explícito. Es un caso no soportado (no silencioso), pero
  frecuente en vistas de SQLite.
- **Evidencia (go test real en %TEMP%):** `"SELECT * FROM t"` →
  `err=unsupported catalog expression "*"`; `"SELECT t.* FROM t"` →
  `err=unsupported catalog expression "t.*"`.

### H7 — BAJA — Concatenación de strings `||` rechazada
- **Archivo:línea:** `compat/sqlparse.go:21-28` (`||` no está entre los operadores)
  y `sqlparse.go:90` (identificador falla).
- **Descripción:** `a || b` (concatenación estándar SQL/SQLite) se rechaza. Caso
  no soportado, error explícito, frecuente.
- **Evidencia (go test real en %TEMP%):** `"SELECT a || b FROM t"` →
  `err=unsupported catalog expression "a || b"`.

### H8 — BAJA — `NOT LIKE` rechazado
- **Archivo:línea:** `compat/sqlparse.go:45` (prefijo `NOT`) + `sqlparse.go:193-206`.
- **Descripción:** `a NOT LIKE b`: el split de `LIKE` (nivel comparación) deja
  `left = "a NOT"`, que falla como identificador → error. Es error explícito, no
  silencioso, pero `NOT LIKE` es construcción común.
- **Verificación:** POR LECTURA (no ejecutado).

### H9 — BAJA — Funciones escalares comunes no soportadas
- **Archivo:línea:** `compat/sqlparse.go:71-84` (sólo `count,sum,avg,min,max,
  lower,upper`).
- **Descripción:** `length`, `coalesce`, `abs`, `substr`, `round`, `trim`,
  `replace`, `cast`, funciones de fecha, etc. se rechazan con
  `unsupported catalog function`. Error explícito; pero son muy comunes en SQLite.
- **Verificación:** POR LECTURA (no ejecutado).

### H10 — BAJA — Rutinas restringidas: sin funciones/operadores/NEW-OLD, sin OUT/INOUT/DEFAULT
- **Archivo:línea:** `compat/sqlroutine.go:51-53` (sólo acciones `insert`),
  `sqlroutine.go:67-80` (`routineCatalogExpression` sólo permite parámetro o
  literal), `sqlroutine.go:20-24` (rechaza `DEFAULT` y `=`), `sqlroutine.go:9-11`
  (rechaza retorno no-void si no es procedimiento).
- **Descripción:** El cuerpo de una rutina sólo admite `INSERT INTO t (c) VALUES
  (<param|literal>)`. Cualquier función/operador en el valor, parámetros
  `OUT`/`INOUT`, defaults, o acciones `UPDATE`/`DELETE` se rechazan. Es
  inconsistente con los triggers (que sí permiten funciones/operadores/NEW-OLD en
  valores y WHERE). Errores explícitos pero comunes.
- **Verificación:** POR LECTURA (no ejecutado).

---

## (b) Verificación de bugs de traducción

| Hallazgo | Demostrado con `go test` real | Por lectura (NO VERIFICADO) |
|---|---|---|
| H1 precedencia NOT | Sí — salida `eq(not(a),b)` etc. | — |
| H2 literal hex `0x10`→columna | Sí — salida `eq(column("status"),column("0x10"))` | — |
| H3 `LIKE` case-sensitivity | — | Sí (ddl.go:269 incondicional) |
| H4 esquema trigger vs select | Sí (repo + %TEMP%) | — |
| H5 `LIMIT -1` aceptado | Sí — `err=<nil>` | — |

Salidas reales de tests temporales (en `%TEMP%`, copia verbatim de
`sqlparse.go`/`sqlselect.go` con stubs de tipos; `matchingParenthesis`
stubbeado por contrato observado):

```
# TestNotPrecedence
"NOT a = b"        -> eq(not(column("a")),column("b")))
"NOT a IS NULL"    -> is_null(not(column("a")))
"NOT a LIKE 'x%'"  -> like(not(column("a")),string("x%"))
"a AND NOT b = c"  -> and(column("a"),eq(not(column("b")),column("c")))

# TestHexAndNumberLiterals
"status = 0x10"     -> eq(column("status"),column("0x10"))
"x = 0xABCDEF"      -> eq(column("x"),column("0xABCDEF"))

# TestSelectEdgeCases
"SELECT * FROM t"            -> err=unsupported catalog expression "*"
"SELECT t.* FROM t"         -> err=unsupported catalog expression "t.*"
"SELECT a || b FROM t"      -> err=unsupported catalog expression "a || b"
"SELECT a FROM t WHERE NOT a = b" -> err=<nil>   (arbolizado mal, ver H1)
"SELECT a FROM t LIMIT -1"  -> err=<nil>          (ver H5)
"SELECT a FROM public.t"   -> err=unsupported table source "public.t"  (ver H4)
```

Todos los `go test` temporales PASS (las entradas problemáticas se aceptan sin
error y se arbolizan mal, o se rechazan explícitamente según el caso). El
directorio temporal se eliminó tras la auditoría.

---

## (c) Cobertura de tests (huecos)

Funciones/ramas exportadas o de traducción **sin cobertura** en los tests del
scope (observado en `*_test.go`):

- **`compileExpression`** (`ddl.go:222`): los kinds `not`, `is_null`,
  `is_not_null`, `div`, `sub`, `mul`, `ne` (`<>`), `lte`/`gt` parcial, y los
  agregados `sum`/`avg`/`min`/`max`/`lower`/`upper` no se prueban directamente
  (sólo `count` indirectamente vía vista). `current_timestamp` sin test. **La
  rama `like` (H3) no se prueba.**
- **`compileReferentialAction`** (`ddl.go:207`): sólo `Cascade` y `SetNull`
  testeados (`TestCompileCanonicalForeignKeyActions`). `Restrict`, `SetDefault`
  y `NoAction` (default) sin test.
- **Rama de precedencia `NOT`** (H1): sin test — y es un bug.
- **`WHEN` de triggers**: ni `parseSQLiteCatalogTrigger` ni
  `parsePostgresCatalogTrigger` ejercen la rama `WHEN` (ambos tests omiten WHEN).
- **`compileTrigger` Postgres** (`ddl.go:402`): la rama `RETURN OLD` para
  evento `delete` (`ddl.go:429`) y la compilación del `WHEN` (`ddl.go:435-441`)
  sin test.
- **`parseCatalogSelect`** (`sqlselect.go:11`): sólo 2 casos (1 vista simple, 1
  join+group). Sin test de: compilación de `DISTINCT`, `HAVING`, `OFFSET`,
  `CROSS JOIN`, `INNER JOIN`, multi-join, `LEFT OUTER JOIN`, `ORDER BY ASC`,
  ORDER BY multicolumna, alias sin `AS`, rechazo de subquery, `LIMIT`/`OFFSET`
  negativos, `*`/`t.*`.
- **`parsePostgresCatalogRoutine`** (`sqlroutine.go:8`): 1 happy path + 1
  rechazo. Sin test de: multi-parámetro, rama de strip `IN` (`sqlroutine.go:20`),
  rechazo `DEFAULT`/`=` (`sqlroutine.go:23`), language `sql`, rechazo `OUT`/`INOUT`,
  rechazo de acción no-`insert` (`sqlroutine.go:51`), validación de parámetro con
  punto (`sqlroutine.go:69`).
- **`compileType`** (`ddl.go:106`): `DateType`→`DATE` (Postgres) sin test directo;
  SQLite `FloatType`→`REAL`, `BinaryType`→`BLOB`, `DateType`→`TEXT`,
  `TimestampType`→`TEXT` sin aserción directa.
- **`parsePostgresCatalogDefault`** (`sqlparse.go:107`): sólo cast `::text` y
  rechazo `nextval`. Otros casts válidos, doble cast y expresión sin cast sin
  test.
- **`catalogFunctionCall`**: llamadas anidadas (p.ej. `lower(upper(x))`) sin test.

---

## (d) Trade-offs / limitaciones de esta auditoría

- **Scope acotado por instrucción.** Sólo se leyeron los 5 archivos de traducción
  + sus tests. Las definiciones de tipos (`spec.go`) y la orquestación de nivel
  superior (`capture.go`, `replicate.go`, `runtime.go`, `verify.go`, `inspect.go`,
  `store.go`) están fuera de scope y no se leyeron. Por ello **no se ejecutó un
  round-trip SQLite→Postgres end-to-end**; los bugs de parseo se demuestran sobre
  copias verbatim de `sqlparse.go`/`sqlselect.go` aisladas en `%TEMP%`.
- **Stubs de tipos.** En `%TEMP%` se reconstruyeron `Expression`, `SelectQuery`,
  `Projection`, `TableSource`, `Join`, `Ordering` con los campos observados en el
  scope (sin leer `spec.go`), y `matchingParenthesis` se stubbeó por su contrato
  observado (`text[position]=='('` → índice del `)` que cierra). La **lógica de
  parseo es byte-idéntica al source del repo** (copia verbatim), de modo que los
  hallazgos H1, H2, H5 y la parte de H4 sobre SELECT son fiables; los hallazgos
  marcados "POR LECTURA" (H3, H8, H9, H10) no se ejecutaron.
- **No se evaluó la capa de datos** (`replicate`/fidelidad de valores); las
  decisiones de almacenamiento como `TEXT` para decimal/timestamp/json/uuid en
  Postgres son de diseño declarado en `ddl.go` y no se clasifican como bugs.
- **No se modificó ningún archivo del repo.** El único artefacto escrito es este
  `AUDIT-A-REPORT.md`. El directorio temporal se eliminó.
- **Categorías sin hallazgos:** (5) TODOs/FIXME explícitos — **ninguno** encontrado
  en el scope (los `unsupported` son errores retornados, no comentarios TODO).