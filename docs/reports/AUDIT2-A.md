# AUDIT2-A — Auditoría read-only (parser/compilador de expresiones)

Alcance: `compat/sqlparse.go`, `compat/sqlselect.go`, `compat/sqlroutine.go`,
`compat/sqltrigger.go`, `compat/ddl.go` y sus `*_test.go`. Sin tocar `cmd/` ni
`e2e/` ni el resto de `compat/`.

Método: cada hallazgo se demostró con un test temporal en
`compat/zz_audit_tmp_test.go` (`package compat`), ejecutado con
`go test ./compat/ -run <Test> -v`. El archivo se borró al terminar
(verificado con `git status --short compat/`, sin salida). Baseline
`go build ./... && go vet ./... && go test ./compat/...` verde antes y
después de la auditoría.

## Hallazgos

### ALTA — hex literal fuera de rango de int64 se traduce con el valor equivocado
`compat/sqlparse.go:436` (`catalogHexLiteral`). SQLite interpreta un literal
hex de 64 bits como entero **con signo** en complemento a dos. El código usa
`strconv.ParseUint(digits, 16, 64)` y emite siempre el valor sin signo. Para
literales entre `0x8000000000000000` y `0xFFFFFFFFFFFFFFFF` esto produce un
valor decimal distinto (y positivo) al que SQLite realmente almacena
(negativo), silenciosamente — no hay error de parseo.

Evidencia real (ejecutada):
```
SQLite 0xFFFFFFFFFFFFFFFF -> -1
  catalogHexLiteral(0xFFFFFFFFFFFFFFFF) -> value="18446744073709551615" handled=true err=<nil>
SQLite 0x8000000000000000 -> -9223372036854775808
  catalogHexLiteral(0x8000000000000000) -> value="9223372036854775808" handled=true err=<nil>
SQLite 0x7FFFFFFFFFFFFFFF -> 9223372036854775807
  catalogHexLiteral(0x7FFFFFFFFFFFFFFF) -> value="9223372036854775807" handled=true err=<nil>
```
Y el round-trip de compilación a Postgres confirma que el literal mal
traducido llega tal cual al SQL emitido:
```
compiled Postgres literal: 18446744073709551615 (SQLite value is -1)
```
`18446744073709551615` además excede el rango de `BIGINT` de Postgres
(máx. `9223372036854775807`), así que en el mejor caso el DDL/CHECK falla en
Postgres; en el peor caso (si el literal no llega a un contexto BIGINT
estricto) el valor queda simplemente equivocado. La cobertura existente
(`sqlparse_test.go:293-320`) solo prueba `0x10` y el caso de rechazo por
overflow (>17 dígitos hex), no la franja de dos′s-complement negativo dentro
de 64 bits — por eso pasó inadvertido.

### MEDIA — esquema Postgres citado con mayúsculas se confunde con `public` por `EqualFold`
`compat/sqltrigger.go:62` (`parsePostgresCatalogTrigger`). El chequeo
`strings.EqualFold(schema, "public")` es case-insensitive, pero un
identificador *citado* como `"Public"` es un esquema distinto y sensible a
mayúsculas en Postgres, no el esquema `public`. El código lo acepta
silenciosamente y descarta el calificador de esquema como si fuera
`public`, exactamente lo que el comentario del propio código dice evitar
("rejected explicitly instead of being discarded silently").

Evidencia real (ejecutada):
```
quoted "Public" schema -> trigger={Name:trg Table:tbl Timing:before Event:insert ...} err=<nil>
unquoted otherschema -> trigger={...} err=unsupported trigger schema "otherschema": only "public" is allowed
```
El primer caso debería rechazarse igual que el segundo, pero no lo hace: el
trigger se acepta con `Table:"tbl"` sin rastro de que en realidad vivía en el
esquema `"Public"`. `sqltrigger_test.go:49` solo cubre el esquema no citado
`myschema`, no la variante citada con mayúscula — hueco de cobertura
confirmado.

### MEDIA — `compileExpression` trata cualquier columna llamada `new`/`old` como la variable mágica de trigger, incluso fuera de un trigger
`compat/ddl.go:255-267`. La misma función compila expresiones de CHECK,
índices `WHERE`, vistas y triggers. La rama de columna decide
"es NEW/OLD" solo mirando si el primer segmento, case-insensitive, es
`new`/`old` — sin ninguna señal de que la expresión pertenece a un trigger — y
en ese caso emite el identificador en mayúsculas **sin comillas**, saltándose
`quoteIdentifier`.

Evidencia real (ejecutada), comparando una columna ordinaria contra una
columna citada `"New"` (case-sensitive, no es la misma columna que `new`
en Postgres):
```
Postgres CHECK expr for column literally named "new": "(NEW > 0)"
Postgres CHECK expr for ordinary column "amount":     "(\"amount\" > 0)"
Postgres CHECK expr for quoted column "New":          "(NEW > 0)"
```
Una tabla con una columna citada `"New"` usada en un CHECK/índice/vista
(fuera de un trigger) compila a un `CHECK (NEW > 0)` no citado, que Postgres
pliega a minúscula `new` — columna que no existe si la real es `"New"` — el
`CREATE TABLE`/`CREATE INDEX`/`CREATE VIEW` falla en destino. Si la columna
real se llama exactamente `new` (minúscula, no citada) el plegado de Postgres
lo salva "por accidente", lo que hace el bug más traicionero (funciona en el
caso común y falla solo en el caso con mayúsculas). No hay ningún test que
ejercite `compileExpression`/CHECK/índice/vista con una columna llamada
`new` u `old`.

## Cobertura

- Bien cubierto: precedencia OR/AND/NOT/comparación/`||`/`+-`/`*/` en
  combinaciones cruzadas (`NOT a || b = c`, `a = b || c AND d = e || f`,
  `a NOT LIKE b || '%' AND c = d`, `length(a || b) = 5`,
  `coalesce(a,b) || c = d`, `a || b NOT LIKE c`) — todas verificadas por
  ejecución y compilan a la forma esperada (evidencia arriba).
- Bien cubierto: `LIKE`→`ILIKE` en Postgres compuesto con `NOT LIKE` infijo y
  con `||` en el patrón — verificado, compila correctamente
  (`(NOT ("a" ILIKE ("b" || '%')))`).
- Bien cubierto: `LIMIT`/`OFFSET` negativos — rechazados tanto en
  `sqlselect.go` (parseo) como en `ddl.go` (compilación), doble barrera.
- Hueco confirmado: literales hex en la franja `[0x8000000000000000,
  0xFFFFFFFFFFFFFFFF]` (ver ALTA).
- Hueco confirmado: esquema Postgres citado con mayúscula distinta de
  `public` (ver MEDIA #1).
- Hueco confirmado: columna llamada `new`/`old` fuera de contexto de trigger
  (ver MEDIA #2).
- Hueco menor (no es bug, es rechazo limpio, verificado): `trim(x, y)` de dos
  argumentos (forma SQLite válida) no está soportado — `parseCatalogExpression`
  solo acepta `trim` de un argumento y devuelve error explícito
  (`unsupported catalog expression "name, 'xyz'"`), no una traducción
  silenciosa incorrecta. Igual vale la pena documentarlo como límite conocido.
- No verificado con ejecución (fuera de tiempo disponible, no se afirma como
  bug): interacción de `routineCatalogWhereExpression`/`routineCatalogExpression`
  (sqlroutine.go) con `concat`/funciones escalares nuevas — el código rechaza
  explícitamente cualquier `Kind` fuera de su lista blanca reducida (no incluye
  `concat`, `length`, `coalesce`, etc.), así que en principio es rechazo, no
  mistraducción, pero no se ejecutó un test dedicado a confirmarlo formalmente.

## Trade-offs de la auditoría

- El chequeo de precedencia se validó por trazado manual del algoritmo y
  luego por ejecución real de los casos más sensibles a interacción (NOT +
  concat + comparación, LIKE/NOT LIKE + concat, funciones anidadas +
  concat); no se hizo fuzzing exhaustivo de todas las combinaciones posibles
  de los 7 niveles de precedencia — se priorizaron las combinaciones que el
  propio historial de commits (`git log`) señala como recientemente tocadas.
- El bug ALTA de hex literal se limitó a demostrar con `modernc.org/sqlite`
  (el driver real del proyecto) qué produce SQLite de verdad, en vez de
  asumir la semántica de dos′s-complement por documentación; esto costó una
  dependencia adicional en el test temporal pero da certeza real en vez de
  una afirmación teórica.
- Los hallazgos MEDIA de esquema citado y columna `new`/`old` son casos de
  borde deliberados (requieren nombres inusuales — un esquema citado con
  mayúscula, o una columna llamada `new`/`old`); se los reporta igual porque
  rompen explícitamente la garantía que el propio código comenta perseguir
  ("rejected explicitly instead of discarded silently"), pero su probabilidad
  de aparecer en un esquema real es baja.
- No se auditó `cmd/` ni `e2e/` ni el resto de `compat/` (fuera de alcance
  explícito), por lo que no se puede afirmar si estos tres hallazgos tienen
  mitigación aguas abajo (por ejemplo, si `runtime.go` o `capture.go`
  reintroducen su propia validación de esquema/columna). Esto se señala para
  que otro auditor con ese alcance lo confirme o descarte.
