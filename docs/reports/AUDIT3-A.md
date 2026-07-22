# AUDIT3-A — Núcleo de traducción SQL (Ronda 3)

Auditor: efímero, sin memoria previa. Scope: **núcleo de traducción SQL**
(`compat/sqlparse.go`, `sqlselect.go`, `sqltrigger.go`, `sqlroutine.go`, `ddl.go`,
`schema.go`, `features.go`). Foco: código recién cambiado (commits `e640ebd`
gating NEW/OLD, `983c109` refactors de `parseCatalogSelect`/`Schema.Validate`/
`compileExpressionIn`, `2287037` rechazo de esquema citado, `67a8292`/`c504431`
hex con signo).

HEAD: `c09ee8f` (main, árbol limpio salvo entregables de los 3 auditores).

## Resumen ejecutivo

Los **fixes recientes funcionan correctamente** y se verifican con evidencia
ejecutada: el gating NEW/OLD por contexto de trigger cita `new`/`old` fuera de
trigger y los emite como `NEW`/`OLD` sólo en los 5 call-sites de trigger; el hex
con signo reinterpreta `uint64→int64` (`0xFFFFFFFFFFFFFFFF → -1`); el rechazo de
esquema citado distingue `"Public"`/`"PUBLIC"` (rechazados) de `"public"`/`public`
(aceptados). Los refactors de `parseCatalogSelect` y `Schema.Validate` son
**behavior-preserving** (comparé contra `983c109~1`); `knownTypeFamilies` está
consistente entre `Validate` y `compileType`.

Se hallaron **2 bugs MEDIA** reales (uno es una mala clasificación silenciosa del
parser; el otro un rechazo incorrecto de SQL válido por whitespace) y **3
BAJA**. Ningún ALTA: no se encontró traducción silenciosamente incorrecta de
datos en el código recién cambiado.

Conteo final: **ALTA 0 / MEDIA 2 / BAJA 3**.

---

## Hallazgos MEDIA

### MEDIA-1 — `isCatalogNumber` clasifica identificadores `[eE][0-9]+` como literales decimales

**Archivo:línea:** `compat/sqlparse.go:386-394` (`isCatalogNumber`), consumido en
`compat/sqlparse.go:91-97`.

`isCatalogNumber` acepta cualquier combinación de `dígitos . + - e E` y exige sólo
que haya **al menos un dígito**. Un identificador válido formado por `e`/`E`
seguido de dígitos (`e5`, `E5`, `e10`, `E3`) satisface esto y se clasifica como
`decimal` en lugar de `column`. SQLite trata `E5`/`e5` como **identificador**
(columna), no como número: la gramática de literal numérico de SQLite exige una
mantisa con dígitos antes del exponente (`digits[.digits][eE[+-]digits]` o `0x...`);
un `E5` aislado no tiene mantisa → es identificador
(https://www.sqlite.org/lang_expr.html, sección "Numeric Literals").

`compileExpression` emite los `decimal`/`integer` **verbatim y sin comillas**
(`compat/ddl.go:254-255`). Resultado: una columna `E5` referenciada en un
CHECK/DEFAULT/predicado de índice se emite como `E5` **sin comillas**, y en
Postgres el identificador sin comillas se pliega a minúsculas (`e5`). Si la
columna se creó como `"E5"` (citada, como hace `compileTable`), la referencia
`E5`→`e5` **no resuelve** → `CREATE TABLE`/`CREATE INDEX` falla con un error
confuso; si además existiera una columna `e5`, referenciaría la **columna
equivocada** en silencio. (Para `e5` minúscula el comportamiento es
accidentalmente correcto porque el plegado coincide.)

Es **silencioso a nivel de parse** (no se reporta como `unresolved`): la
expresión se acepta en el esquema canónico y el fallo aparece tarde, en `CREATE`.

**Evidencia ejecutada** (test temporal `compat/zz_audit3a_sqlcore_test.go`,
borrado al terminar; Go 1.26.4):

```
[NUM] postgres "E5 > 0" -> ast={Kind:gt} out="(E5 > 0)" err=<nil>
[NUM] postgres "col = E5" -> ast={Kind:eq} out="(\"col\" = E5)" err=<nil>
[NUM] postgres "a = e5" -> ast={Kind:eq} out="(\"a\" = e5)" err=<nil>
[NUM] postgres "e5" -> ast={Kind:decimal Value:"e5"} out="e5" err=<nil>
[NUM] postgres "e" -> ast={Kind:column Value:"e"} out="\"e\"" err=<nil>   # sin dígito: sí es columna
[NUM] postgres "1e5" -> ast={Kind:decimal Value:"1e5"} out="1e5" err=<nil> # con mantisa: correcto
```

`E5` se emite sin comillas frente a `"E5"` que esperaría una columna real. Regla
SQLite citada arriba.

**Severidad:** MEDIA. Mala clasificación silenciosa con riesgo de columna
equivocada / `CREATE` fallido. No ALTA porque el camino común termina en error
de `CREATE` (rechazo), no en datos incorrectos, y el escenario de doble columna
(`e5` y `E5`) es artificioso.

### MEDIA-2 — Palabras-clave multi-palabra exigen exactamente un espacio; whitespace múltiple interno es rechazado

**Archivo:línea:** `compat/sqlselect.go:222-254` (`topLevelKeyword`), aplicado a
`GROUP BY`/`ORDER BY`/`HAVING`/etc. vía `catalogSelectClauses` y
`nextCatalogJoin` (`LEFT OUTER JOIN`, `INNER JOIN`, `CROSS JOIN`).

`topLevelKeyword` busca la keyword como substring con `strings.HasPrefix(upper[i:], keyword)`
donde `keyword` lleva **un solo espacio** (`"GROUP BY"`). Un whitespace interno
múltiple (`GROUP  BY`, `ORDER  BY`) o un tab entre las dos palabras no coincide,
la keyword no se localiza, y el texto residual se interpreta como parte del
origen de tabla → `parseCatalogTableSource` recibe `"t GROUP  BY x"`, hace
`strings.Fields` → 4 tokens → error `unsupported table source "t GROUP  BY x"`.

SQLite y Postgres **tokenizan cualquier secuencia de whitespace (espacios, tabs,
newlines) como un separador único**, así que `GROUP  BY` es DDL perfectamente
válido. El rechazo es incorrecto y el mensaje es engañoso (culpa al "table
source"). Comportamiento preservado por el refactor `983c109` (no es una
regresión introducida ahora, pero cae en el foco de "whitespace" adversarial
pedido). El newline *entre* cláusulas sí funciona (la keyword "ORDER BY" sigue
apareciendo como substring contiguo); el problema es estrictamente el
whitespace **dentro** de la palabra-clave multi-palabra.

**Evidencia ejecutada:**

```
[WS] "SELECT a FROM t GROUP BY x"   -> err=<nil> groupby=1 orderby=0
[WS] "SELECT a FROM t GROUP  BY x"  -> err=unsupported table source "t GROUP  BY x"  groupby=0
[WS] "SELECT a FROM t ORDER BY x"   -> err=<nil> groupby=0 orderby=1
[WS] "SELECT a FROM t ORDER  BY x"  -> err=unsupported table source "t ORDER  BY x"  orderby=0
[WS] "SELECT a FROM t WHERE x = 1\nORDER BY y" -> err=<nil> orderby=1   # newline entre cláusulas: ok
```

**Severidad:** MEDIA (rechazo incorrecto de SQL válido). Frecuencia realista
baja (DDL rara vez lleva doble espacio interno), por lo que el riesgo es
menor, pero encaja en la definición de MEDIA ("rechazo incorrecto").

---

## Hallazgos BAJA

### BAJA-1 — Tokens hex sin dígitos / hex inválidos se parsean como identificador de columna

**Archivo:línea:** `compat/sqlparse.go:427-445` (`catalogHexLiteral`) y caída a
`parseCatalogIdentifier` (`compat/sqlparse.go:396`).

`catalogHexLiteral` devuelve `handled=false` para `0x` (sin dígitos) o `0xG`
(dígito no-hex). El flujo cae a `isCatalogNumber` (false: `x`/`G` no están en el
set numérico) y luego a `parseCatalogIdentifier`, que acepta `0x` y `0xG` como
identificadores (empiezan con dígito, pero `parseCatalogIdentifier` sólo exige
`letra|dígito|_`). Resultado: `0x` → columna `"0x"`, `0xG` → columna `"0xG"`,
compilados como referencias a columnas que casi seguro no existen → error de
runtime, no corrupción. SQLite trataría `0x`/`0xG` como error de sintaxis.

```
[HEX] postgres "0x"  -> "\"0x\""  err=<nil>   # silenciosamente una columna
[HEX] postgres "0xG" -> "\"0xG\"" err=<nil>
```

### BAJA-2 — Hex con signo explícito (`-0x10`) se rechaza

**Archivo:línea:** `compat/sqlparse.go:428` (`catalogHexLiteral` exige
`text[0]=='0'`).

`-0x10` no entra en `catalogHexLiteral` (empieza con `-`), cae a `isCatalogNumber`
(false: `x` no numérico) e identificador (false: `-` no válido) →
`unsupported catalog expression "-0x10"`. SQLite evalúa `-0x10` como menos unario
aplicado a `0x10` = `-16`. Caso hermano del fix de hex con signo
(`67a8292`/`c504431`), no cubierto. Frecuencia muy baja (hex con signo en
CHECK/DEFAULT es raro).

```
[HEX] PARSE "-0x10" -> ERR: unsupported catalog expression "-0x10"
```

### BAJA-3 — Identificador citado que contiene un punto se rechaza

**Archivo:línea:** `compat/sqlparse.go:396-415` (`parseCatalogIdentifier`).

`parseCatalogIdentifier` hace `strings.Split(text, ".")` **antes** de detectar
comillas, así un único identificador citado que contiene un punto
(`"a.b"`) se parte en `"a` y `b"`, ambos inválitos → `ok=false`. SQLite y
Postgres permiten `"a.b"` como identificador citado válido. Es un rechazo (la
expresión se reporta `unresolved`), no corrupción. Identificadores con punto son
inusuales; los cualificados (`"sch"."tbl"."col"`, con puntos entre comillas) sí
funcionan.

```
[IDENT] "\"a.b\"" -> "" ok=false
[IDENT] "\"sch\".\"tbl\".\"col\"" -> "sch.tbl.col" ok=true   # cualificado: ok
```

---

## Áreas verificadas limpias (resultados negativos)

1. **Gating NEW/OLD por contexto (fix `e640ebd`):** correcto. Fuera de trigger,
   `new.col`/`"new"`/`New.col` se citan como columnas ordinarias
   (`"new"."col"`, `"New"."col"`); en trigger se emiten como `NEW."col"`/`OLD."x"`.
   `compileColumnExpression` sólo trata el **primer** segmento como variable de
   transición (`i==0`), así `other.new` en trigger queda citado. Los 5 call-sites
   con `inTrigger=true` (`ddl.go:518,537,558,589,598`) son exactamente los de
   `compileTrigger`/`compileTriggerAction`; CHECK/DEFAULT/index-WHERE/views usan
   `compileExpression` (`false`). Sin call-site con contexto equivocado.
   ```
   [NONTRIG] postgres "New.col" -> "\"New\".\"col\""        # preserva mayúsculas
   [TRIG]     postgres "new.col" -> "NEW.\"col\""
   [TRIG]     postgres "other.new" -> "\"other\".\"new\""   # no-first-segment: citado
   ```

2. **Hex con signo (fix `67a8292`/`c504431`):** correcto. `0xFFFFFFFFFFFFFFFF → -1`,
   `0x8000000000000000 → -9223372036854775808`, `0x7FFFFFFFFFFFFFFF → 9223372036854775807`,
   `0x10 → 16`, emitido igual en SQLite y Postgres. Coincide con la semántica
   int64 de SQLite.

3. **Rechazo de esquema citado (fix `2287037`):** correcto. `parsePostgresCatalogTrigger`
   acepta `public`, `PUBLIC` (sin comillas, plegado) y `"public"` (citado
   minúscula exacta); rechaza `"Public"`, `"PUBLIC"`, `"public "` (con espacio),
   `other`. `EqualFold` se aplica sólo a calificadores **no citados**; los citados
   son sensibles a mayúsculas. Sin caso hermano sin cubrir en `parseCatalogSelect`
   (los nombres cualificados allí se rechazan por completo en `parseCatalogTableSource`
   vía `strings.Contains(table, ".")`).
   ```
   [TRIGSCHEMA] "ON PUBLIC.t"   -> err=<nil>
   [TRIGSCHEMA] "ON \"public\".t" -> err=<nil>
   [TRIGSCHEMA] "ON \"Public\".t" -> err=unsupported trigger schema "Public"
   [TRIGSCHEMA] "ON \"PUBLIC\".t" -> err=unsupported trigger schema "PUBLIC"
   ```

4. **`knownTypeFamilies` consistencia Validate↔compileType:** las 11 familias
   (`BooleanType, IntegerType, DecimalType, FloatType, TextType, BinaryType,
   DateType, TimestampType, JSONType, UUIDType, VectorType`) están en
   `knownTypeFamilies` (`schema.go:58-70`) Y son cubiertas por `compileType` en
   ambos engines (`ddl.go:121-181`). Un typo se rechaza en `validateTableColumn`
   y `validateRoutines`. Fuente única, consistente.

5. **Refactor `parseCatalogSelect` (`983c109`):** behavior-preserving. Comparado
   contra `983c109~1`: mismo `topLevelKeyword`, mismas keywords con un espacio,
   mismo orden de localización/aplicación (sort por posición), mismo
   `sourceEnd = located[0].position`, mismo split de proyecciones/alias. Los
   helpers extraídos (`stripCatalogSelectHeader`, `catalogSelectClauses`,
   `locateCatalogClauses`, `applyCatalogClauses`, `parseCatalogProjections`,
   `applyOrderByClause`, `applyLimitClause`, `applyOffsetClause`) reproducen el
   flujo original sin divergencia. Inputs adversariales (cláusulas en orden,
   JOIN, alias, ORDER BY DESC/ASC, LIMIT/OFFSET) dan resultados idénticos al
   esperado.

6. **Refactor `Schema.Validate` (`983c109`):** behavior-preserving. Mismo
   orden tablas→índices→vistas→triggers→routines, mismos mensajes y orden de
   errores, mismos chequeos (nombre reservado, duplicados, acción referencial,
   dimensión vectorial positiva, parámetros de rutina). Sin divergencia.

7. **Refactor `compileExpressionIn` (`983c109`):** behavior-preserving. Helpers
   `compileBinaryExpression`/`compileUnaryExpression`/`compileScalarFunction`/
   `compileExpressionArgs` propagan `inTrigger` correctamente; `coalesce`/`replace`
   validan aridad. Verificado con expresiones anidadas.

8. **Precedencia de operadores, funciones anidadas del allowlist, LIKE/ILIKE,
   casts, concat, IS NULL:** correctos.
   - `NOT a = b` → `NOT (a = b)` (NOT más flojo que `=`), `a = b AND NOT c = d`
     → `(a=b) AND (NOT(c=d))`, `a NOT LIKE b` → `NOT(a LIKE b)`.
   - `lower(upper(name))`, `abs(length(x))`, `replace(s,'a','b')`, `coalesce(a,0)+1`
     correctos; el flag `inTrigger` se propaga a los args.
   - `LIKE` → `ILIKE` en Postgres (trade-off Unicode/ASCII documentado), `LIKE`
     en SQLite.
   - Precedencia aritmética: `(a+b)*c` respeta `*` sobre `+`; `a||b||c`
     left-assoc.
   - Casts Postgres `::text`/`::date`/`::bigint` aceptados; `::public.text`,
     `::text[]`, `nextval` rechazados explícitamente (no silencioso).

9. **`features.go`:** switch de `assess`/`InferFeatures` sin lógica de riesgo;
   cubre todas las `Feature` const; sin huecos relevantes para traducción.

10. **`sqlroutine.go`:** `routineCatalogWhereExpression`/`routineCatalogExpression`
    rechazan identificadores cualificados y construye fuera de la gramática de
    comparación/lógica; los nodos `parameter` se compilan en `runtime.go`
    (`compileRoutineWhere`), fuera de `compileExpressionIn` (las rutinas no
    generan DDL vía `CompileDDL`, que sólo itera tables/indexes/views/triggers).
    Sin bug de "parameter sin compiler".

## Conteo final

| Severidad | Cantidad |
|-----------|----------|
| ALTA      | 0        |
| MEDIA     | 2        |
| BAJA      | 3        |