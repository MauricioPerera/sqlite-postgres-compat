# AUDIT4-A — Ronda 4: Confirmación de cierre + ataque al código nuevo (núcleo SQL)

Auditor efímero, sin memoria previa. **Scope: núcleo de traducción SQL**
(`compat/sqlparse.go`, `sqlselect.go`, `sqltrigger.go`, `sqlroutine.go`, `ddl.go`,
`schema.go`, `features.go`). Otros 2 auditores cubren runtime y CLIs/docs.

- **HEAD:** `4dba30d` (rama `main`).
- **Método:** un único test temporal `compat/zz_audit4a_test.go` (`package compat`),
  ejecutado con `go test ./compat/ -run TestAudit4A -v -count=1` (Go 1.26.4). El
  archivo **se borró** al terminar (`git status --short compat/` no lo lista;
  verificado). Evidencia real pegada más abajo. READ-ONLY: no se modificó código de
  producción; única creación permanente es este reporte.
- **Baseline:** `go build ./...` verde y `go test ./compat/ -count=1` verde antes
  y después de la auditoría. (Nota: `go vet ./compat/` marca un error de sintaxis
  en `compat/zz_audit4_test.go:421` — archivo temporal **ajeno** del auditor de
  runtime, en progreso, no creado por mí y no tocado; no afecta a `go build` ni a
  `go test`, que pasan.)

## Resumen ejecutivo

Los **5 hallazgos previos de mi scope (ALTA + MEDIA) están CERRADOS**: la
re-ejecución de cada repro original sobre HEAD muestra que el bug ya no existe.
Atacando el código nuevo (FIX-R2: `isCatalogNumber` con gramática numérica y
`keywordMatchSpan` con whitespace; FIX-M2: `compileExpressionIn`/`knownTypeFamilies`;
helpers de QUAL-C4) con inputs adversariales no cubiertos antes **no se halló
ningún ALTA ni MEDIA nuevo**: la gramática numérica es estrictamente más
conservadora y correcta (sin falsos positivos ni falsos negativos frente a
SQLite), `keywordMatchSpan` no matchea keywords dentro de identificadores citados
ni en bordes de palabra, y la propagación `inTrigger` anidada es correcta.

Se halló **1 BAJA nueva** (N1: `GROUP BY`/`ORDER BY` con operando vacío se aceptan
silenciosamente y la cláusula se descarta al emitir — pre-existente, no introducido
por los fixes, encontrado al atacar el borde "keyword al fin de string") y **1
observación** (N2: literales límite con puntos/dígitos caen a referencias de
columna cualificadas — raíz pre-existente BAJA-1, no un bug del fix).

**Conteo final: cerrados 5/5 previos (ALTA+MEDIA). Nuevos: ALTA 0 / MEDIA 0 / BAJA 1.**

---

## Parte 1 — Confirmación de cierre

Por cada hallazgo ALTA/MEDIA de mi scope en AUDIT2-A y AUDIT3-A se re-ejecutó la
repro original (o equivalente) sobre HEAD.

| # | Hallazgo (origen) | Repro re-ejecutada sobre HEAD | Resultado |
|---|-------------------|------------------------------|-----------|
| 1 | AUDIT2-A ALTA — hex fuera de rango int64 → valor equivocado (`catalogHexLiteral`) | `catalogHexLiteral("0xFFFFFFFFFFFFFFFF")` / `0x8000000000000000` / `0x7FFFFFFFFFFFFFFF` + round-trip a literal Postgres | **CERRADO** |
| 2 | AUDIT2-A MEDIA #1 — esquema citado `"Public"` confundido con `public` (`parsePostgresCatalogTrigger`) | trigger con `"Public"`/`"PUBLIC"`/`"public"`/`PUBLIC`/`otherschema` | **CERRADO** |
| 3 | AUDIT2-A MEDIA #2 — columna `new`/`old` fuera de trigger emitida sin comillas (`compileExpression`) | `New`/`new`/`old` fuera de trigger vs `new.x`/`OLD.x` en trigger | **CERRADO** |
| 4 | AUDIT3-A MEDIA-1 — `isCatalogNumber` clasificaba `E5`/`e10` como decimales | `isCatalogNumber` + `parseCatalogExpression` de `E5`/`e5`/`e10`/`E3`/`e`/`E` y de `1e5`/`1E5`/`.5e3` | **CERRADO** |
| 5 | AUDIT3-A MEDIA-2 — keywords multi-palabra exigían un solo espacio (`topLevelKeyword`) | `GROUP  BY`/`GROUP\tBY`/`ORDER  BY`/`LEFT  OUTER  JOIN`/`INNER\tJOIN` | **CERRADO** |

### Evidencia ejecutada (salida real)

**#1 — hex con signo (ALTA):** reinterpretación `uint64→int64` correcta, el literal
corregido llega al SQL Postgres emitido.
```
[HEX-SIGN] 0xFFFFFFFFFFFFFFFF -> value="-1" handled=true err=<nil> want="-1" => CERRADO
[HEX-SIGN] compiled Postgres literal: -1 err=<nil> (SQLite value is -1)
[HEX-SIGN] 0x8000000000000000 -> value="-9223372036854775808" handled=true ... => CERRADO
[HEX-SIGN] 0x7FFFFFFFFFFFFFFF -> value="9223372036854775807" ... => CERRADO
[HEX-SIGN] 0x10 -> value="16" ... => CERRADO
[HEX-SIGN] 0x0 -> value="0" ... => CERRADO
```

**#2 — esquema citado (MEDIA #1):** `"Public"`/`"PUBLIC"` rechazados; `"public"`
(citado, minúscula exacta), `public`/`PUBLIC` (no citados, plegado) aceptados;
`otherschema` rechazado.
```
[SCHEMA] "Public" reject   -> err=unsupported trigger schema "Public": only "public" is allowed => CERRADO
[SCHEMA] "PUBLIC" reject   -> err=unsupported trigger schema "PUBLIC": ... => CERRADO
[SCHEMA] "public" accept   -> err=<nil> => CERRADO
[SCHEMA] PUBLIC accept     -> err=<nil> => CERRADO
[SCHEMA] public accept     -> err=<nil> => CERRADO
[SCHEMA] otherschema reject -> err=unsupported trigger schema "otherschema": ... => CERRADO
```

**#3 — new/old fuera de trigger (MEDIA #2):** `new`/`old`/`New` fuera de trigger se
citan como columnas ordinarias; en trigger `new.x`/`OLD.x` → `NEW."x"`/`OLD."x"`.
```
[NEWOLD] NONTRIG "New"  "New" -> out="\"New\"" want="\"New\"" => CERRADO
[NEWOLD] NONTRIG "new"  "new" -> out="\"new\"" want="\"new\"" => CERRADO
[NEWOLD] NONTRIG "old"  "old" -> out="\"old\"" want="\"old\"" => CERRADO
[NEWOLD] TRIG new.x     "new.x" -> out="NEW.\"x\"" want="NEW.\"x\"" => CERRADO
[NEWOLD] TRIG OLD.x     "OLD.x" -> out="OLD.\"x\"" want="OLD.\"x\"" => CERRADO
[CHECK-expr] "New > 0" -> ast={Kind:gt} out="(\"New\" > 0)"        # citada, no pliega a new
```

**#4 — clasificación E5 (MEDIA-1):** `E5`/`e5`/`e10`/`E3`/`e`/`E` → `isCatalogNumber=false` → `column` citada; `1e5`/`1E5`/`.5e3`/`0.5`/`1.5`/`1.`/`.5` siguen siendo números.
```
[NUM-CLASS] isCatalogNumber("E5")=false  -> column Value:"E5" out="\"E5\""
[NUM-CLASS] isCatalogNumber("e10")=false -> column Value:"e10" out="\"e10\""
[NUM-VALID]  isCatalogNumber("1e5")=true / ("1E5")=true / (".5e3")=true / ("1.")=true / (".5")=true
[E5-in-expr] "E5 > 0" -> out="(\"E5\" > 0)"      # E5 columna citada, no decimal
```

**#5 — whitespace interno en keywords (MEDIA-2):** doble espacio, tab y
whitespace mixto (tab+newline) dentro de keywords multi-palabra parsean igual que
la forma canónica; JOINs con whitespace interno también.
```
[WS] GROUP double      err=<nil> groupby=1 => CERRADO
[WS] GROUP tab         err=<nil> groupby=1 => CERRADO
[WS] ORDER double      err=<nil> orderby=1 => CERRADO
[WS] GROUP mixed       err=<nil> groupby=1 => CERRADO   # "GROUP \t\nBY x"
[WS] LEFT OUTER double err=<nil> joins=1 => CERRADO     # "LEFT  OUTER  JOIN"
[WS] INNER tab         err=<nil> joins=1 => CERRADO    # "INNER\tJOIN"
```

> BAJA previas de AUDIT3-A (BAJA-1/2/3) **no estaban en el cierre requerido**
> (sólo ALTA+MEDIA). Se observa que persisten y **no fueron reintroducidas ni
> empeoradas** por los fixes: `-0x10` sigue rechazado (BAJA-2); `0x`/`0xG` siguen
> cayendo a columna (BAJA-1). No se re-testeó BAJA-3 (identificador citado con
> punto) por estar fuera del cierre exigido.

---

## Parte 2 — Ataque al código nuevo

### Literales límite (FIX-R2, `isCatalogNumber`)

Entradas adversariales que las repros originales no cubrían:

```
[LIM] .5     isNum=true  -> decimal ".5"            out=".5"          # válido SQLite
[LIM] 5.     isNum=true  -> decimal "5."            out="5."          # válido SQLite
[LIM] 1.     isNum=true  -> decimal "1."            out="1."          # válido SQLite
[LIM] 5.e5   isNum=true  -> decimal "5.e5"          out="5.e5"        # válido SQLite
[LIM] -.5    isNum=true  -> decimal "-.5"           out="-.5"         # válido SQLite
[LIM] +5     isNum=true  -> integer "+5"            out="+5"          # válido
[LIM] 1E5    isNum=true  -> decimal "1E5"           out="1E5"         # válido
[LIM] 0x1F   isNum=false -> integer "31"           out="31"          # vía catalogHexLiteral
[LIM] 1e+    isNum=false -> PARSE ERR: unsupported catalog expression "1e+"   # rechazo limpio
[LIM] 1e-    isNum=false -> PARSE ERR: unsupported catalog expression "1e-"   # rechazo limpio
[LIM] .e5    isNum=false -> PARSE ERR: unsupported catalog expression ".e5"    # rechazo limpio
[LIM] 1.5e+  isNum=false -> PARSE ERR: unsupported catalog expression "1.5e+"  # rechazo limpio
[LIM] .5e    isNum=false -> PARSE ERR: unsupported catalog expression ".5e"   # rechazo limpio
[LIM] +      isNum=false -> PARSE ERR: unsupported catalog expression "+"     # rechazo limpio
[LIM] .      isNum=false -> PARSE ERR: unsupported catalog expression "."     # rechazo limpio
[LIM] 1e     isNum=false -> column "1e"            out="\"1e\""        # identificador (BAJA-1 root)
[LIM] 0x     isNum=false -> column "0x"            out="\"0x\""        # identificador (BAJA-1 root)
[LIM] 1.2.3  isNum=false -> column "1.2.3"         out="\"1\".\"2\".\"3\""     # ver N2
[LIM] 1.5e   isNum=false -> column "1.5e"          out="\"1\".\"5e\""          # ver N2
[LIM] 5.e    isNum=false -> column "5.e"           out="\"5\".\"e\""           # ver N2
[LIM] 1e5e   isNum=false -> column "1e5e"          out="\"1e5e\""              # ver N2
[LIM] e5.5   isNum=false -> column "e5.5"          out="\"e5\".\"5\""          # ver N2
```

**Veredicto:** la nueva `isCatalogNumber` es **correcta y estrictamente más
conservadora** que la anterior.

- **Sin falsos positivos** (la clase del bug original): ningún identificador
  válido (`E5`, `e10`, `e`, `E`) se clasifica como número. Cubierto también el
  borde de palabra prefijo: `GROUPY`/`GROUPBY` no se confunden con `GROUP` (ver
  keywords).
- **Sin falsos negativos** que produzcan corrupción silenciosa: todos los
  literales numéricos válidos en SQLite (`1`, `1.5`, `.5`, `1.`, `1e5`, `1E5`,
  `.5e3`, `1.5e-3`, `5.e5`, `-.5`, `+5`) siguen siendo `decimal`/`integer`; las
  formas **inválidas** en SQLite (`1e`, `1e+`, `.e5`, `1.5e`, `.5e`, `+`, `.`)
  dejan de ser número y **o se rechazan limpiamente** (`unsupported catalog
  expression`) **o caen al identificador** (BAJA-1 root, ver N2). Ninguna forma
  inválida se emite verbatim como número (mejora respecto al comportamiento
  previo, que emitía `1.5e`/`5.e` como decimal invalid que Postgres rechazaba
  tarde).

No se halló ALTA ni MEDIA en este código.

### Keywords multi-palabra en bordes (FIX-R2, `keywordMatchSpan`)

```
[EDGE] GROUPY BY (no match)    err=unsupported table source "t GROUPY BY x"   # no matchea GROUP BY
[EDGE] GROUP BYY (no boundary) err=unsupported table source "t GROUP BYY x"   # wordBoundary tras BY falla
[EDGE] GROUPBY glued (no match) err=unsupported table alias "t GROUPBY x"    # sin espacio interno
[EDGE] quoted proj has group by err=<nil> groupby=0 from="t" proj="my group by col"  # ★ keyword dentro de identificador citado NO se trata como cláusula
[EDGE] group by quoted value   err=<nil> groupby=1 from="t" proj="a"         # valor citado con espacios: ok
[EDGE] GROUP BY at end         err=<nil> groupby=0 from="t" proj="a"         # ★ ver N1
[EDGE] GROUP  BY at end dbl    err=<nil> groupby=0 from="t" proj="a"         # ★ ver N1
[EDGE] ORDER BY at end         err=<nil> groupby=0 from="t" proj="a"         # ★ ver N1
[EDGE] HAVING at end           err=empty expression                           # HAVING vacío: rechazado
[EDGE] LIMIT at end            err=strconv.Atoi: parsing "": invalid syntax  # LIMIT vacío: rechazado
[EMIT] "SELECT a FROM t GROUP BY" -> compiled="SELECT \"a\" FROM \"t\"" (cláusula vacía descartada=true)
[EMIT] "SELECT a FROM t ORDER BY" -> compiled="SELECT \"a\" FROM \"t\"" (cláusula vacía descartada=true)
```

**Veredicto `keywordMatchSpan`:** correcto en los bordes pedidos.

- **Dentro de identificadores citados** (`"my group by col"`): el `inDouble` de
  `topLevelKeyword` hace que `"group by"` dentro del identificador citado **no**
  se localice como cláusula (`groupby=0`, la proyección queda
  `"my group by col"`). El `FROM` posterior al identificador citado se detecta
  correctamente. ★ Este es el adversarial central pedido y pasa limpio.
- **Bordes de palabra**: `GROUPY BY`, `GROUP BYY`, `GROUPBY` (pegado) **no**
  matchean `GROUP BY` (la comprobación `wordBoundary(text, end)` tras el span y
  la exigencia de whitespace entre palabras lo impiden). El SQL raro se rechaza
  (no se traduce silenciosamente).
- **Whitespace interno**: tab, doble espacio y mixto tab+newline matchean (parte 1).

### `compileExpressionIn` / `knownTypeFamilies` (FIX-M2)

```
[FAM] knownTypeFamily("integer")=true / ("text")=true
[FAM] knownTypeFamily("Integer")=false / ("INTEGER")=false / ("")=false / ("nope")=false
[FAM] compileType(postgres, nope) -> err=type family "nope" is not supported by postgres
[FAM] compileType(sqlite, nope)   -> err=type family "nope" is not supported by sqlite
[TRIG-nested]    "coalesce(new.x, 0)" -> COALESCE(NEW."x", 0)      # inTrigger propagado al arg anidado
[NONTRIG-nested] "coalesce(new.x, 0)" -> COALESCE(\"new\".\"x\", 0) # fuera de trigger: citado
```

**Veredicto:** correcto. `inTrigger` se propaga a los argumentos anidados
(`compileExpressionArgs`), así `coalesce(new.x, 0)` resuelve `NEW."x"` sólo en
trigger y `"new"."x"` fuera. `knownTypeFamily` es estricto (las 11 familias en
minúsculas exactas; `"Integer"`/`"INTEGER"` no son conocidas — **by design**: la
fuente única son las constantes `TypeFamily` minúsculas). `compileType` rechaza
familia desconocida en ambos engines (defensa en profundidad además de
`Schema.Validate`). Sin bug.

### Combinaciones pedidas

```
[COMBO] E5 en GROUP BY -> groupby=1 kind=column value="E5" compiled="\"E5\""  # E5 columna (no decimal) también en GROUP BY
[CHECK-NOT-hex]      "NOT 0x10 = 1"     -> (NOT (16 = 1))        # hex + NOT + comparación
[CHECK-hex-NOTLIKE]  "0x10 NOT LIKE '%'" -> (NOT (16 ILIKE '%'))  # hex + NOT LIKE
[CHECK-hex-eq]       "0x10 = 16"        -> (16 = 16)             # hex en CHECK
```

**Veredicto:** las combinaciones funcionan. `E5` en `GROUP BY` se sigue tratando
como columna citada (el fix MEDIA-1 alcanza el camino de `GROUP BY`, que parsea
cada ítem con `parseCatalogExpression`). Hex en `CHECK` con `NOT` (prefijo y
`NOT LIKE` infijo) compila correctamente. Sin bug.

---

## Hallazgos nuevos

### BAJA-N1 — `GROUP BY`/`ORDER BY` con operando vacío se aceptan en silencio y la cláusula se descarta al emitir

**Archivo:línea:** `compat/sqlselect.go` (`applyCatalogClauses` vía
`keywordMatchSpan`; valor vacío) + `compat/ddl.go:446` y `:464`
(`if len(query.GroupBy) > 0` / `len(query.OrderBy) > 0`).

`GROUP BY`/`ORDER BY` al final del string (operando vacío) son localizados por
`topLevelKeyword`/`keywordMatchSpan`, pero el valor de la cláusula resulta `""`.
`splitTopLevelComma("")` devuelve `[]`, así `GroupBy`/`OrderBy` queda vacío **sin
error**. En la emisión, `compileSelect` sólo agrega `GROUP BY`/`ORDER BY` si
`len(...) > 0`, de modo que la cláusula **se descarta silenciosamente**: el SQL
invalid se acepta y se traduce como si la cláusula no existiera.

```
[EMIT] "SELECT a FROM t GROUP BY" -> compiled="SELECT \"a\" FROM \"t\"" (cláusula vacía descartada=true)
[EMIT] "SELECT a FROM t ORDER BY" -> compiled="SELECT \"a\" FROM \"t\"" (cláusula vacía descartada=true)
```

Inconsistencia: `HAVING` vacío sí se rechaza (`empty expression`) y `LIMIT`/`OFFSET`
vacíos también (`strconv.Atoi`), por lo que `GROUP BY`/`ORDER BY` son los únicos
que aceptan el operando vacío en silencio.

**Severidad:** BAJA. SQLite/Postgres rechazan `GROUP BY`/`ORDER BY` sin operandos
(error de sintaxis); el input es malformado y poco realista, y el efecto es
descartar una cláusula invalid (no se corrompen datos ni se emite SQL invalid en
destino — se emite SQL válido pero sin la cláusula).

**Origen:** **pre-existente**, no introducido por FIX-R2. Con la keyword de un
solo espacio anterior, `SELECT a FROM t GROUP BY` ya matcheaba y ya producía
`GroupBy` vacío; el cambio a `keywordMatchSpan` preserva ese comportamiento. Se
reporta porque cae en el borde "keyword al fin de string" pedido para atacar el
código nuevo. No es regresión del fix.

### Observación N2 — literales límite con punto/dígito caen a referencias de columna cualificadas

Formas como `1.2.3`, `1.5e`, `5.e`, `1e5e`, `e5.5` no son números válidos en
SQLite y `isCatalogNumber` correctamente las rechaza; pero caen a
`parseCatalogIdentifier`, que parte por `.` y acepta segmentos empezados por
dígito (sólo exige `letra|dígito|_`), produciendo referencias cualificadas raras
(`"1"."2"."3"`, `"1"."5e"`, `"e5"."5"`) o identificadores digit-led (`"1e5e"`).
Estas referencias no resuelven → fallan en `CREATE` (rechazo), **no** corrupción
silenciosa.

**No es un bug nuevo del fix.** Es la raíz pre-existente BAJA-1 (identificadores
que empiezan con dígito + `strings.Split` por `.` en `parseCatalogIdentifier`),
ahora **más visible** precisamente porque `isCatalogNumber` deja de clasificar
esas formas como números (comportamiento correcto). Antes del fix, `1.5e`/`5.e`
se emitían verbatim como `decimal` invalid que Postgres rechazaba tarde; ahora se
rechazan como referencias no-resolvibles. El fix es correcto y no introduce este
comportamiento. Se lista como observación, no como hallazgo nuevo.

---

## Áreas verificadas limpias (resultados negativos)

1. **`isCatalogNumber` (FIX-R2):** sin falsos positivos (ningún identificador
   válido se clasifica como número) ni falsos negativos que corrompan
   (ningún literal numérico válido de SQLite deja de ser número). Gramática
   `[+-]?(digits[.[digits]]|.digits)[([eE][+-]?digits)]` validada con `.5`, `5.`,
   `1.`, `1e5`, `1E5`, `.5e3`, `1.5e-3`, `5.e5`, `-.5`, `+5` (todos `true`) y
   `E5`/`e10`/`e`/`1e`/`.e5`/`1e+`/`.5e` (todos `false`).
2. **`keywordMatchSpan` (FIX-R2):** no matchea keywords dentro de identificadores
   citados (`"my group by col"`) ni de literales string (ya cubierto por FIX-R2);
   respeta bordes de palabra (`GROUPY`/`GROUPBY`/`BYY` no matchean); tolera
   tab/doble espacio/mixto. Posición de corte correcta en `applyCatalogClauses` y
   `parseCatalogFrom` (sin `Y` colgado tras `GROUP  BY`).
3. **`catalogHexLiteral` (ALTA fix):** overflow >64 bit → error explícito
   (`0x10000000000000000`); `0X` mayúscula aceptado; signo two's-complement
   correcto (`0x8000000000000000`→-9223372036854775808); `0x`/`0xG`→`handled=false`
   (caen al identificador, BAJA-1 pre-existente).
4. **`compileExpressionIn` / propagación `inTrigger` (FIX-M2):** `coalesce(new.x,0)`
   anidado resuelve `NEW."x"` sólo en trigger, `"new"."x"` fuera; los 5 call-sites
   de trigger emiten `NEW`/`OLD` y los no-trigger citan. Sin call-site con contexto
   equivocado (coincide con AUDIT3-A §1).
5. **`knownTypeFamilies` (FIX-M2):** las 11 familias aceptadas; typos/variants
   de casing (`"Integer"`) y `""` rechazados; `compileType` rechaza en ambos
   engines (defensa en profundidad). Fuente única consistente.
6. **Combinaciones:** `E5` en `GROUP BY` → columna citada; hex en `CHECK` con
   `NOT` (prefijo) y `NOT LIKE` (infijo) → compilación correcta. Sin bug.
7. **`features.go`:** no re-auditado en profundidad (sin lógica de riesgo de
   traducción en esta ronda); sin cambios recientes en mi scope que introduzcan
   riesgo. Sin hallazgo.

## Conteo final

| Métrica | Valor |
|---------|-------|
| Hallazgos previos (ALTA+MEDIA) confirmados cerrados | **5 / 5** |
| Nuevos ALTA | 0 |
| Nuevos MEDIA | 0 |
| Nuevos BAJA | 1 (N1; pre-existente, no regresión del fix) |
| Observaciones (no bug nuevo) | 1 (N2) |

No se ejecutó `git add`/`commit` (regla). Única creación permanente:
`docs/reports/AUDIT4-A.md`. El test temporal `compat/zz_audit4a_test.go` se borró
(verificado con `git status --short compat/`, que no lo lista). El archivo
`compat/zz_audit4_test.go` que aparece como no rastreado es temporal **ajeno** del
auditor de runtime (en progreso, con error de sintaxis en vet); no fue creado ni
tocado por mí.