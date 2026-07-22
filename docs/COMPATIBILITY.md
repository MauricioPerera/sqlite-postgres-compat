# Matriz de compatibilidad

## Significado de los estados

- **Exacto canónico**: expresado en el AST común, compilado para ambos motores y cubierto por pruebas conductuales.
- **Traducible externo**: el inspector reconoce un subconjunto definido de SQL nativo y lo convierte al AST.
- **No resuelto**: se detecta, se incluye en `Inspection.Unresolved` y bloquea una afirmación exacta.

“Traducible externo” no significa que cualquier variante de esa familia esté soportada.

| Familia | AST canónico | Catálogo externo traducible | Fuera de cobertura actual |
|---|---|---|---|
| Tablas y columnas | Tipos escalares del modelo | Afinidades SQLite y tipos PostgreSQL conocidos | Tipos de usuario, dominios, arrays y semántica específica no modelada |
| Clave primaria y `UNIQUE` | Simples y compuestas | Pragmas SQLite y `pg_constraint` | Diferimiento y extensiones específicas |
| Clave foránea | Compuesta; `NO ACTION`, `RESTRICT`, `CASCADE`, `SET NULL`, `SET DEFAULT` | Acciones comunes de ambos catálogos | `MATCH` no común, diferimiento y extensiones del motor |
| `CHECK` | Expresiones del AST común | Operadores, literales y funciones comunes reconocidas | Expresiones arbitrarias del dialecto |
| Índices | Únicos, parciales, varias columnas, `ASC`/`DESC` | B-tree por columnas y predicados comunes | Índices de expresión, métodos, colaciones y clases de operador específicas |
| Vistas | Proyección, filtro, joins, agregación, orden, límite y desplazamiento | `INNER`, `LEFT`, `CROSS`, `GROUP BY`, `HAVING` y agregados comunes | Subconsultas, ventanas, CTE y operaciones de conjuntos |
| Triggers | `BEFORE`/`AFTER`; eventos y acciones `INSERT`/`UPDATE`/`DELETE` | Auditoría basada en `NEW`/`OLD` | Control de flujo, SQL dinámico y código procedural arbitrario |
| Rutinas | Acciones transaccionales del runtime común | Procedimientos SQL/PLpgSQL parametrizados de inserción | Funciones con retorno, modos avanzados y lógica procedural arbitraria |
| JSON | Texto canónico sin pérdida de representación | Columnas JSON/JSONB se reconocen | Operadores e índices JSON específicos |
| UUID | Texto canónico | UUID nativo se reconoce | Operadores o extensiones específicas |
| Vector | `vector(N)` con dimensión canónica | `F32_BLOB(N)` de libSQL y `vector` de pgvector se reconocen | Tipos vectoriales no modelados, índices ANN y funciones de distancia nativas |
| Timestamp | Texto canónico RFC3339Nano | Tipos timestamp se reconocen | Semántica arbitraria de zona, infinidades y funciones específicas |
| Búsqueda textual | Tokenización Unicode determinista en Go | No se traduce FTS nativo | Ranking, stemming, diccionarios, FTS5, `tsvector`/`tsquery` arbitrarios |

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
| Date | `TEXT` | `DATE` | Conversión mediante el adaptador |
| Timestamp | `TEXT` | `TEXT` | Preservar RFC3339Nano sin truncar a microsegundos |
| JSON | `TEXT` | `TEXT` | Preservar orden, espacios, duplicados y números originales |
| UUID | `TEXT` | `TEXT` | Preservar la representación textual exacta |
| Vector | `TEXT` | `vector(N)` | Carrier interoperable texto `'[1,2,3]'`; SQLite (modernc) sin funciones vectoriales, pgvector requiere extensión en el destino |

## Límite del 100 %

El sistema no declara compatibilidad total. La aceptación global seguirá fallando mientras exista al menos una familia genérica requerida con estado diferente de `exact`. El detalle actualizado está en [VALIDATION_REPORT.md](reports/VALIDATION_REPORT.md).
