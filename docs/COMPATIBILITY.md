# Matriz de compatibilidad

## Significado de los estados

- **Exacto canónico**: expresado en el AST común, compilado para ambos motores y cubierto por pruebas conductuales.
- **Traducible externo**: el inspector reconoce un subconjunto definido de SQL nativo y lo convierte al AST.
- **No resuelto**: se detecta, se incluye en `Inspection.Unresolved` y bloquea una afirmación exacta.

“Traducible externo” no significa que cualquier variante de esa familia esté soportada.

| Familia | AST canónico | Catálogo externo traducible | Fuera de cobertura actual |
|---|---|---|---|
| Tablas y columnas | Tipos escalares del modelo; columnas generadas `STORED` (`GENERATED ALWAYS AS (<expr>) STORED`) con expresión en la gramática canónica | Afinidades SQLite y tipos PostgreSQL conocidos; columnas generadas `STORED` reconstruidas desde el catálogo (`pragma_table_xinfo` hidden=3 en SQLite, `is_generated='ALWAYS'` en PostgreSQL) | Tipos de usuario, dominios, arrays y semántica específica no modelada; columnas generadas `VIRTUAL` (PostgreSQL no las soporta), de identidad, y expresiones de generación fuera de la gramática (quedan `No resuelto`) |
| Clave primaria y `UNIQUE` | Simples y compuestas | Pragmas SQLite y `pg_constraint` | Diferimiento y extensiones específicas |
| Clave foránea | Compuesta; `NO ACTION`, `RESTRICT`, `CASCADE`, `SET NULL`, `SET DEFAULT` | Acciones comunes de ambos catálogos | `MATCH` no común, diferimiento y extensiones del motor |
| `CHECK` | Expresiones del AST común | Operadores, literales y funciones comunes reconocidas | Expresiones arbitrarias del dialecto |
| Índices | Únicos, parciales, varias columnas, `ASC`/`DESC` | B-tree por columnas y predicados comunes | Índices de expresión, métodos, colaciones y clases de operador específicas |
| Vistas | Proyección, filtro, joins, agregación, orden, límite, desplazamiento, operaciones de conjuntos (`UNION`, `UNION ALL`, `INTERSECT`, `EXCEPT`), tablas derivadas (`FROM (SELECT …) alias`) y CTE no recursivas (`WITH … AS (…)`) | `INNER`, `LEFT`, `CROSS`, `GROUP BY`, `HAVING`, agregados comunes, compounds de conjuntos con precedencia igual entre motores, subconsultas en `FROM` (tabla derivada, no correlacionable) y `WITH` no recursivo | `WITH RECURSIVE`; subconsultas en la gramática de expresiones (incluido `x IN (SELECT …)`); subconsultas correlacionadas y `EXISTS`; ventanas; tabla derivada sin alias; y cadenas de compound que mezclan `INTERSECT` con `UNION`/`EXCEPT` (precedencia divergente entre motores) |
| Triggers | `BEFORE`/`AFTER`; eventos y acciones `INSERT`/`UPDATE`/`DELETE` | Auditoría basada en `NEW`/`OLD` | Control de flujo, SQL dinámico y código procedural arbitrario |
| Rutinas | Acciones transaccionales del runtime común (`INSERT`/`UPDATE`/`DELETE`) | Procedimientos SQL/PLpgSQL parametrizados con acciones `INSERT`/`UPDATE`/`DELETE` y `WHERE` restringido a comparaciones columna↔parámetro/literal compuestas con `AND`/`OR`/`NOT` | Funciones con retorno, modos avanzados y lógica procedural arbitraria |
| JSON | Texto canónico sin pérdida de representación | Columnas JSON/JSONB se reconocen | Operadores e índices JSON específicos |
| UUID | Texto canónico | UUID nativo se reconoce | Operadores o extensiones específicas |
| Vector | `vector(N)` con dimensión canónica | `F32_BLOB(N)` de libSQL y `vector` de pgvector se reconocen | Tipos vectoriales no modelados, índices ANN y funciones de distancia nativas |
| Timestamp | Texto canónico RFC3339Nano | Tipos timestamp se reconocen | Semántica arbitraria de zona, infinidades y funciones específicas |
| Búsqueda textual | Tokenización Unicode determinista en Go | No se traduce FTS nativo | Ranking, stemming, diccionarios, FTS5, `tsvector`/`tsquery` arbitrarios |

## Columnas generadas

Se soportan columnas generadas **`STORED`** (`col TIPO GENERATED ALWAYS AS (<expr>) STORED`), sintaxis idéntica en SQLite (≥ 3.31) y PostgreSQL (≥ 12). La expresión de generación usa la gramática canónica de expresiones (misma que `CHECK`/`DEFAULT`). Ambos motores recomputan el valor de forma determinista en el destino: esa recomputación idéntica **es** la prueba de equivalencia. La cadena de datos nunca escribe una columna generada — se excluye de la lista de columnas de todo `INSERT`/`UPDATE` (snapshot y replicación) y el motor destino la recalcula.

Restricciones (validadas en `Schema.Validate`, ambos motores las imponen): una columna generada no puede tener `DEFAULT` a la vez, no puede formar parte de una clave primaria, y **`VIRTUAL` se rechaza explícitamente** (PostgreSQL no lo soporta) — nunca se emite; en inspección nativa una columna `VIRTUAL`, de identidad o con expresión fuera de la gramática queda `No resuelto` (`Exact = false`), no degradada en silencio.

## Gramática de expresiones y funciones

El parser canónico (`compat/sqlparse.go`) reconoce un subconjunto deliberadamente acotado de SQL. Las expresiones, predicados `CHECK`, `WHERE` de índices parciales, condiciones de trigger y cuerpos de vista se traducen sólo dentro de esa gramática; todo lo demás se rechaza con error explícito en vez de aceptarse de forma silenciosa.

Operadores por nivel de precedencia (de menor a mayor): `OR` · `AND` · `NOT` · `IS NULL`/`IS NOT NULL` · comparaciones (`<=`, `>=`, `<>`, `!=`, `=`, `<`, `>`, `LIKE`) y los predicados `BETWEEN`/`IN` (mismo nivel que las comparaciones) · `||` (concatenación) · `+`/`-` · `*`/`/`. `NOT` se resuelve entre `AND` y las comparaciones, de modo que `NOT a = b` se parsea como `not(eq(a, b))`. La forma `a NOT LIKE b` se pliega a `not(like(a, b))`.

`LIKE` se compila a `ILIKE` en PostgreSQL para preservar la semántica de SQLite (insensible a mayúsculas/minúsculas en ASCII); es el mapeo pragmático estándar y se acepta como compromiso conocido (ILIKE pliega todo el rango Unicode, SQLite sólo ASCII).

Predicados y expresiones condicionales admitidos:

- `a BETWEEN x AND y` y `a NOT BETWEEN x AND y`: se compilan a `BETWEEN`/`NOT BETWEEN` nativo en ambos motores. La forma es inclusiva (`x <= a <= y`) e idéntica en SQLite y PostgreSQL; el `AND` delimitador del `BETWEEN` no divide el predicado al nivel del `AND` lógico.
- `a IN (v1, v2, …)` y `a NOT IN (…)` sobre una **lista de valores explícita** (sin subconsultas, que quedan fuera de alcance). Se compilan a `IN`/`NOT IN` nativo; la semántica de pertenencia, incluida la lógica de tres valores con `NULL`, coincide en ambos motores. Una lista vacía o un elemento que no sea una expresión de la gramática se rechaza.
- `CASE` con búsqueda (searched): `CASE WHEN cond THEN val [WHEN …] [ELSE val] END`. Se compila a `CASE` nativo en ambos motores, con evaluación por primera coincidencia idéntica. La forma simple con operando (`CASE x WHEN …`) se rechaza.

Funciones escalares admitidas (allowlist exacta): agregadas `count`, `sum`, `avg`, `min`, `max` (aceptan `*` o una expresión); de caja `lower`, `upper` (una expresión); `length`, `abs`, `trim` (una expresión); `coalesce` (al menos un argumento); `nullif` (exactamente dos argumentos); `replace` (exactamente tres argumentos). Cualquier otra función se rechaza con `unsupported catalog function`.

Funciones descartadas por divergencia entre motores (verificado contra PostgreSQL 17 real; evidencia en [reports/FEAT-CUBOA-1-REPORT.md](reports/FEAT-CUBOA-1-REPORT.md)): `round` (PostgreSQL redondea `double precision` half-to-even, SQLite half-away-from-zero), `substr` (índices y longitud negativos divergen: SQLite cuenta desde el final / recorta, PostgreSQL devuelve vacío o lanza error) y `cast(... AS integer)` (SQLite trunca hacia cero, PostgreSQL redondea). Se rechazan como cualquier otra función no incluida. `IS DISTINCT FROM` queda diferido (requiere gating de versión de SQLite ≥ 3.39).

Literales admitidos: cadenas `'...'` (con `''` como escape), booleanos `TRUE`/`FALSE`, `NULL`, `CURRENT_TIMESTAMP`, enteros y decimales (`123`, `1.5`, `1e3`), y literales hexadecimales SQLite `0x10`/`0XABCDEF` convertidos a su valor decimal. Identificadores pueden cualificarse con `.` y citarse con `"..."`.

## Capacidades de auditoría

Las capacidades canónicas exactas actuales son:

- `canonical_foreign_keys`;
- `canonical_check_constraints`;
- `canonical_indexes`;
- `canonical_views`;
- `canonical_triggers`;
- `canonical_routines`;
- `canonical_full_text`;
- `canonical_vectors`.

Las familias genéricas `foreign_keys`, `check_constraints`, `indexes`, `views`, `triggers`, `stored_routines` y `full_text` permanecen `unknown`, porque representan todas las variantes posibles del SQL nativo.

## Representación de valores

| Familia canónica | SQLite físico | PostgreSQL físico | Motivo |
|---|---|---|---|
| Boolean | `INTEGER` | `BOOLEAN` | Conversión canónica `true`/`false` |
| Integer | `INTEGER` | `BIGINT` | Rango entero común usado por el modelo |
| Decimal | `TEXT` | `NUMERIC(p,s)` o `NUMERIC` | Evitar redondeo binario en SQLite |
| Float | `REAL` | `DOUBLE PRECISION` | Valor de punto flotante explícito |
| Text | `TEXT` | `TEXT` | Representación común |
| Binary | `BLOB` | `BYTEA` | Journal en hexadecimal y artefacto canónico en base64 |
| Date | `TEXT` | `TEXT` | Preserva el valor canónico exacto (`DATE` nativo haría que pgx devuelva `time.Time` y se pliegue a timestamp, divergiendo del origen) |
| Timestamp | `TEXT` | `TEXT` | Preservar RFC3339Nano sin truncar a microsegundos |
| JSON | `TEXT` | `TEXT` | Preservar orden, espacios, duplicados y números originales |
| UUID | `TEXT` | `TEXT` | Preservar la representación textual exacta |
| Vector | `TEXT` | `vector(N)` | Carrier interoperable texto `'[1,2,3]'`; SQLite (modernc) sin funciones vectoriales, pgvector requiere extensión en el destino |

## Límite del 100 %

El sistema no declara compatibilidad total. La aceptación global seguirá fallando mientras exista al menos una familia genérica requerida con estado diferente de `exact`. El detalle actualizado está en [VALIDATION_REPORT.md](reports/VALIDATION_REPORT.md).
