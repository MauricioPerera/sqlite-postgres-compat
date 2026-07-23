# Matriz de compatibilidad

## Significado de los estados

- **Exacto canónico**: expresado en el AST común, compilado para ambos motores y cubierto por pruebas conductuales.
- **Traducible externo**: el inspector reconoce un subconjunto definido de SQL nativo y lo convierte al AST.
- **No resuelto**: se detecta, se incluye en `Inspection.Unresolved` y bloquea una afirmación exacta.

“Traducible externo” no significa que cualquier variante de esa familia esté soportada.

| Familia | AST canónico | Catálogo externo traducible | Fuera de cobertura actual |
|---|---|---|---|
| Tablas y columnas | Tipos escalares del modelo; columnas generadas `STORED` (`GENERATED ALWAYS AS (<expr>) STORED`) con expresión en la gramática canónica; **dominios** (`CREATE DOMAIN` nativo en PostgreSQL, inline base+CHECK en SQLite) con CHECK en la gramática canónica | Afinidades SQLite y tipos PostgreSQL conocidos; columnas generadas `STORED` reconstruidas desde el catálogo (`pragma_table_xinfo` hidden=3 en SQLite, `is_generated='ALWAYS'` en PostgreSQL) | Tipos de usuario, arrays y semántica específica no modelada; columnas generadas `VIRTUAL` (PostgreSQL no las soporta), de identidad, y expresiones de generación fuera de la gramática (quedan `No resuelto`); **inspección externa de un dominio** — nunca existió como tal en SQLite (aparece como columna+CHECK) y PostgreSQL deparsa el CHECK con `VALUE` fuera de la gramática, por lo que una columna de tipo dominio queda `No resuelto` (`domain_column`) |
| Clave primaria y `UNIQUE` | Simples y compuestas | Pragmas SQLite y `pg_constraint` | Diferimiento y extensiones específicas |
| Clave foránea | Compuesta; `NO ACTION`, `RESTRICT`, `CASCADE`, `SET NULL`, `SET DEFAULT` | Acciones comunes de ambos catálogos | `MATCH` no común, diferimiento y extensiones del motor |
| `CHECK` | Expresiones del AST común | Operadores, literales y funciones comunes reconocidas | Expresiones arbitrarias del dialecto; y expresiones con nodos no deterministas (`gen_random_uuid()`), rechazadas para evitar divergencia por fila entre motores |
| Índices | Únicos, parciales, varias columnas, `ASC`/`DESC`, y **claves de expresión** `(expr)` de la gramática canónica (función/operador; únicos y mezclados con columnas) | B-tree por columnas y por expresión (reconstruida desde `CREATE INDEX` en SQLite y `pg_get_indexdef` en PostgreSQL cuando el deparse cae dentro de la gramática) más predicados parciales comunes | Expresiones que el motor reescribe fuera de la gramática (p.ej. PostgreSQL añade `::text` a un literal: `coalesce(email, 'x')` → `COALESCE(email, 'x'::text)`) quedan `No resuelto`; también métodos, colaciones y clases de operador específicas |
| Vistas | Proyección, filtro, joins, agregación, orden, límite, desplazamiento, operaciones de conjuntos (`UNION`, `UNION ALL`, `INTERSECT`, `EXCEPT`), tablas derivadas (`FROM (SELECT …) alias`) y CTE no recursivas (`WITH … AS (…)`) | `INNER`, `LEFT`, `CROSS`, `GROUP BY`, `HAVING`, agregados comunes, compounds de conjuntos con precedencia igual entre motores, subconsultas en `FROM` (tabla derivada, no correlacionable) y `WITH` no recursivo | `WITH RECURSIVE` (investigado contra motores reales; ver abajo); CTE auto-referencial sin `RECURSIVE` (ver abajo); subconsultas en la gramática de expresiones (incluido `x IN (SELECT …)`); subconsultas correlacionadas y `EXISTS`; ventanas; tabla derivada sin alias; y cadenas de compound que mezclan `INTERSECT` con `UNION`/`EXCEPT` (precedencia divergente entre motores) |
| Triggers | `BEFORE`/`AFTER`; eventos y acciones `INSERT`/`UPDATE`/`DELETE` | Auditoría basada en `NEW`/`OLD` | Control de flujo, SQL dinámico y código procedural arbitrario |
| Rutinas | Acciones transaccionales del runtime común (`INSERT`/`UPDATE`/`DELETE`) | Procedimientos SQL/PLpgSQL parametrizados con acciones `INSERT`/`UPDATE`/`DELETE` y `WHERE` restringido a comparaciones columna↔parámetro/literal compuestas con `AND`/`OR`/`NOT` | Funciones con retorno, modos avanzados y lógica procedural arbitraria |
| JSON | Texto canónico sin pérdida de representación | Columnas JSON/JSONB se reconocen | Operadores e índices JSON específicos |
| UUID | Texto canónico | UUID nativo se reconoce | Operadores o extensiones específicas |
| Vector | `vector(N)` con dimensión canónica | `F32_BLOB(N)` de libSQL y `vector` de pgvector se reconocen | Tipos vectoriales no modelados, índices ANN y funciones de distancia nativas |
| Timestamp | Texto canónico RFC3339Nano | Tipos timestamp se reconocen | Semántica arbitraria de zona, infinidades y funciones específicas |
| Búsqueda textual | Tokenización Unicode determinista en Go | No se traduce FTS nativo | Ranking, stemming, diccionarios, FTS5, `tsvector`/`tsquery` arbitrarios |

## `WITH RECURSIVE` (rechazado, con evidencia)

`WITH RECURSIVE` se rechaza (`Exact = false`), no por falta de sintaxis sino por
**riesgo semántico real** verificado contra SQLite real y PostgreSQL 17 real
(evidencia completa en [reports/FEAT-RECURSIVECTE-REPORT.md](reports/FEAT-RECURSIVECTE-REPORT.md)):

- **Terminación (bloqueante).** Sobre datos cíclicos (`A→B→A`) alimentando el
  acumulador de profundidad/path que lleva casi toda query de árbol, el dedup de
  filas completas de `UNION` nunca se dispara (cada fila es única por la
  profundidad creciente) y **ambos** motores se desbocan de forma **divergente**:
  SQLite no tiene guardia de iteración del lado servidor (3.178.564 filas en 6 s,
  detenido sólo por cancelación de cliente); PostgreSQL tampoco tiene límite
  nativo (se detuvo por `statement_timeout`, con 222.940 filas — un conteo
  distinto). No hay un resultado común y acotado que comparar. La aciclicidad **no
  es verificable en compilación** (una `FOREIGN KEY` no impide `A→B→A`).
- **Sin guarda de ciclo nativa portable.** La cláusula `CYCLE` del estándar SQL
  (PostgreSQL 14+) es un **error de sintaxis en SQLite**, y la guarda por arrays
  (`= ANY(path)`) no tiene equivalente en SQLite (sin tipo array). La gramática
  canónica no puede expresar una guarda de path portable, y la corrección de una
  guarda ad-hoc no es verificable sintácticamente.
- **Orden (arreglable, pero no es el bloqueante).** Sin `ORDER BY` el orden de
  filas diverge sobre el mismo árbol acíclico (SQLite `1,2,3,4,5,6` vs PostgreSQL
  `1,3,2,6,5,4`); con `ORDER BY depth,id` explícito ambos coinciden.

Como ningún subconjunto puede garantizarse byte-idéntico ni con guardas
sintácticas obligatorias, se conserva el rechazo.

### CTE auto-referencial (rechazada por propiedad)

Además de la *keyword* `RECURSIVE`, se rechaza una CTE no recursiva que **se
referencia a sí misma** en su propio cuerpo — `WITH t AS (SELECT id FROM t) ...`
con `t` también una tabla base real. Ese DDL parsea y compila **byte-idéntico**
en ambos motores, pero diverge en runtime: SQLite falla con `circular reference: t`
(vista inutilizable) y PostgreSQL liga el `t` interior a la tabla base y devuelve
filas. La guarda detecta la **propiedad** de auto-referencia (no sólo la keyword),
tanto al parsear (`stripCatalogWith`) como al compilar (`compileWith`, para un AST
construido a mano). Una CTE que referencia a **otra CTE anterior** de la misma
cláusula `WITH` es legítima y se sigue aceptando. Evidencia en
[reports/FIX-AUDIT10-REPORT.md](reports/FIX-AUDIT10-REPORT.md).

La comparación de nombres es **case-insensitive para ASCII**, equiparando el
plegado que ambos motores aplican a identificadores no citados (SQLite compara
letras ASCII sin distinguir mayúsculas; PostgreSQL pliega a minúscula), así que
`WITH T AS (SELECT id FROM t) …` también se rechaza: `T` y `t` nombran la misma
relación en ambos motores. El AST no conserva si un identificador vino citado
(`parseCatalogIdentifier` quita las comillas), de modo que la comparación no puede
distinguir un `"T"` citado deliberadamente de un `T` sin citar; plegar el case es
la elección conservadora (a lo sumo rechaza la vista rara que cita `"T"` para
diferenciarlo de una tabla `t`, ya de por sí un riesgo de divergencia entre
motores) y cierra el flanco de AUDIT11 A11-1 para todo el camino no citado, que es
el común y peligroso. Evidencia en
[reports/FIX-AUDIT11-REPORT.md](reports/FIX-AUDIT11-REPORT.md).

## Columnas generadas

Se soportan columnas generadas **`STORED`** (`col TIPO GENERATED ALWAYS AS (<expr>) STORED`), sintaxis idéntica en SQLite (≥ 3.31) y PostgreSQL (≥ 12). La expresión de generación usa la gramática canónica de expresiones (misma que `CHECK`/`DEFAULT`). Ambos motores recomputan el valor de forma determinista en el destino: esa recomputación idéntica **es** la prueba de equivalencia. La cadena de datos nunca escribe una columna generada — se excluye de la lista de columnas de todo `INSERT`/`UPDATE` (snapshot y replicación) y el motor destino la recalcula.

Restricciones (validadas en `Schema.Validate`, ambos motores las imponen): una columna generada no puede tener `DEFAULT` a la vez, no puede formar parte de una clave primaria, y **`VIRTUAL` se rechaza explícitamente** (PostgreSQL no lo soporta) — nunca se emite; en inspección nativa una columna `VIRTUAL`, de identidad o con expresión fuera de la gramática queda `No resuelto` (`Exact = false`), no degradada en silencio.

## Dominios

Un **dominio** (`Domain{Name, Type base, Check, NotNull, Default}`) es un tipo con nombre = tipo base + `CHECK` opcional (+ `NOT NULL`/`DEFAULT` opcionales). Se soporta como objeto canónico, con una **asimetría explícita e inevitable** entre motores:

- **PostgreSQL** tiene dominios nativos: se emite `CREATE DOMAIN "nombre" AS <tipo base> [DEFAULT …] [NOT NULL] [CHECK (…)]` antes de las tablas, y la columna usa el nombre del dominio como tipo. El `CHECK` referencia el valor con la palabra clave `VALUE`.
- **SQLite no tiene dominios**: la única forma portable es **inline** — cada columna que usa el dominio recibe el tipo base + el `CHECK` (+ `NOT NULL`/`DEFAULT`) del dominio, con el placeholder del valor resuelto al nombre de la columna. El resultado es **semánticamente equivalente** (mismo tipo base, misma restricción) al dominio de PostgreSQL, y el dato almacenado es idéntico.

Una columna referencia un dominio con `Column.DomainRef` (campo aditivo `omitempty`); debe conservar `Type` igual al tipo base del dominio (la cadena de datos canoniza por `Column.Type.Family`) y ser neutra (`Nullable=true`, sin `Default` ni `Generated` propios), de modo que el dominio sea la única fuente de la restricción. El `CHECK` del dominio usa la gramática canónica de expresiones (el valor bajo prueba es el nodo `domain_value`); una expresión fuera de la gramática se rechaza al compilar. `Schema.Validate` rechaza dominios sin nombre/duplicados/sin tipo base, y columnas que referencian un dominio inexistente, con tipo que no coincide, o no neutras.

**Round-trip y asimetría.** La ruta canónica (esquema creado por esta capa, guardado en `__compat_schema`) preserva el dominio en metadata y **round-tripea exacto en ambos motores** — es la prueba principal. La **inspección externa** no reconstruye "un dominio":

- En **SQLite** un dominio nunca existió; la columna inline aparece como columna + `CHECK` y se reconstruye exacta **como columna** (no como dominio). No es una divergencia de datos, sólo una diferencia de forma, y es aceptable.
- En **PostgreSQL** el deparse del `CHECK` del dominio usa `VALUE` (y añade `::type`), fuera de la gramática canónica, así que una columna de tipo dominio queda en `Inspection.Unresolved` (`kind=domain_column`, `Exact=false`) en vez de degradarse en silencio a su tipo escalar subyacente.

## Gramática de expresiones y funciones

El parser canónico (`compat/sqlparse.go`) reconoce un subconjunto deliberadamente acotado de SQL. Las expresiones, predicados `CHECK`, `WHERE` de índices parciales, **claves de expresión de índices** (`CREATE INDEX … (lower(email))`), condiciones de trigger y cuerpos de vista se traducen sólo dentro de esa gramática; todo lo demás se rechaza con error explícito en vez de aceptarse de forma silenciosa.

Operadores por nivel de precedencia (de menor a mayor): `OR` · `AND` · `NOT` · `IS NULL`/`IS NOT NULL` · comparaciones (`<=`, `>=`, `<>`, `!=`, `=`, `<`, `>`, `LIKE`) y los predicados `BETWEEN`/`IN` (mismo nivel que las comparaciones) · `||` (concatenación) · `+`/`-` · `*`/`/`. `NOT` se resuelve entre `AND` y las comparaciones, de modo que `NOT a = b` se parsea como `not(eq(a, b))`. La forma `a NOT LIKE b` se pliega a `not(like(a, b))`.

`LIKE` se compila a `ILIKE` en PostgreSQL para preservar la semántica de SQLite (insensible a mayúsculas/minúsculas en ASCII); es el mapeo pragmático estándar y se acepta como compromiso conocido (ILIKE pliega todo el rango Unicode, SQLite sólo ASCII).

Predicados y expresiones condicionales admitidos:

- `a BETWEEN x AND y` y `a NOT BETWEEN x AND y`: se compilan a `BETWEEN`/`NOT BETWEEN` nativo en ambos motores. La forma es inclusiva (`x <= a <= y`) e idéntica en SQLite y PostgreSQL; el `AND` delimitador del `BETWEEN` no divide el predicado al nivel del `AND` lógico.
- `a IN (v1, v2, …)` y `a NOT IN (…)` sobre una **lista de valores explícita** (sin subconsultas, que quedan fuera de alcance). Se compilan a `IN`/`NOT IN` nativo; la semántica de pertenencia, incluida la lógica de tres valores con `NULL`, coincide en ambos motores. Una lista vacía o un elemento que no sea una expresión de la gramática se rechaza.
- `CASE` con búsqueda (searched): `CASE WHEN cond THEN val [WHEN …] [ELSE val] END`. Se compila a `CASE` nativo en ambos motores, con evaluación por primera coincidencia idéntica. La forma simple con operando (`CASE x WHEN …`) se rechaza.

Funciones escalares admitidas (allowlist exacta): agregadas `count`, `sum`, `avg`, `min`, `max` (aceptan `*` o una expresión); de caja `lower`, `upper` (una expresión); `length`, `abs`, `trim` (una expresión); `coalesce` (al menos un argumento); `nullif` (exactamente dos argumentos); `replace` (exactamente tres argumentos); `gen_random_uuid` (exactamente cero argumentos). Cualquier otra función se rechaza con `unsupported catalog function`.

`gen_random_uuid()` es un caso especial **no determinista**: genera un UUID v4 (RFC 4122) aleatorio nuevo en cada evaluación. Compila al built-in nativo `gen_random_uuid()` en PostgreSQL (núcleo desde PG13; el proyecto exige PG17, sin extensión ni gating) y a una expresión inline sobre `randomblob`/`hex` que arma un v4 válido (formato 8-4-4-4-12, nibble de versión `4`, nibble de variante en `{8,9,a,b}`) en SQLite. A diferencia del resto de la gramática **no** aplica la equivalencia byte-idéntica (una fuente aleatoria no puede producir el mismo valor en ambos motores): la prueba de equivalencia es que ambos motores compilan y ejecutan sin error, cada valor es un v4 válido y las llamadas sucesivas difieren. No es una traducción literal de `hex(randomblob(16))` (idiom de SQLite que PostgreSQL de núcleo no replica): es una función canónica de propósito equivalente. Se puede usar en `DEFAULT` de una columna `family=uuid` y como valor dentro de una acción de trigger (`INSERT ... VALUES (gen_random_uuid(), ...)`) — los dos contextos sancionados, donde el valor se genera una vez y se almacena/copia como dato. Se **rechaza dentro de un `CHECK`** (`Schema.Validate`; cubre `CHECK` de tabla y de dominio): un `CHECK` reevalúa su predicado por fila en cada motor de forma independiente, así que un predicado no determinista aceptaría o rechazaría la misma fila de forma distinta entre SQLite y PostgreSQL. Los índices de expresión y las columnas generadas `STORED` no necesitan esta guarda: ambos motores ya rechazan ahí una función no determinista de forma consistente. Verificado contra PostgreSQL 17 real y SQLite real; evidencia en [reports/FEAT-RANDOMUUID-REPORT.md](reports/FEAT-RANDOMUUID-REPORT.md) y [reports/FIX-AUDIT10-REPORT.md](reports/FIX-AUDIT10-REPORT.md).

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
