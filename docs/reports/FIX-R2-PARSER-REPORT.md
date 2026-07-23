# FIX-R2-PARSER — Fixes de parser (AUDIT3-A, hallazgos MEDIA-1 y MEDIA-2)

Auditor efímero, sin memoria previa. HEAD de partida: `c09ee8f` (main). Scope
estricto: `compat/sqlparse.go`, `compat/sqlselect.go` y sus tests
(`sqlparse_test.go`, `sqlselect_test.go`). No se tocaron `store*`, `capture*`,
`replicate*`, `ddl*`, `schema*`, `cmd/**`, `docs/**`, `e2e/**`.

## Resumen

| Hallazgo | Archivo:línea (origen) | Fix | Estado |
|----------|------------------------|-----|--------|
| MEDIA-1 | `sqlparse.go:386` (`isCatalogNumber`) | Validar la forma real del literal numérico de SQLite | Hecho, probado |
| MEDIA-2 | `sqlselect.go:222` (`topLevelKeyword`) | Tolerar cualquier whitespace entre palabras de keywords multi-palabra | Hecho, probado |

---

## MEDIA-1 — `isCatalogNumber` clasificaba `E5`/`e10` como literales decimales

**Causa.** `isCatalogNumber` sólo exigía "al menos un dígito" sobre el alfabeto
`[0-9.+-eE]`. Un identificador formado por `e`/`E` seguido de dígitos (`E5`,
`e10`) satisface eso y se clasificaba como `decimal`, emitido **verbatim y sin
comillas** por `compileExpression` (`ddl.go:254-255`). En Postgres el
identificador sin comillas se pliega a minúsculas, de modo que una columna
creada como `"E5"` (citada, como hace `compileTable`) no resolvía y el
`CREATE` fallaba tarde, o —escenario artificioso— resolvía a una columna `e5`
equivocada en silencio.

**Fix.** Reescribí `isCatalogNumber` como un parser manual chico que valida la
gramática real del literal numérico de SQLite:

```
[+-]? ( digits [. [digits]] | .digits ) [ ([eE] [+-]? digits) ]
```

- La mantisa **debe aportar al menos un dígito** antes de cualquier exponente.
  `e5`/`E5`/`e10`/`E10` (sin mantisa) → `false` → caen a `parseCatalogIdentifier`
  → `column`, emitido **citado** (`"E5"`).
- El exponente, si aparece, va sólo tras una mantisa válida y **debe llevar al
  menos un dígito** (`1e` → `false`).
- `.5`, `1.`, `1.5`, `1e5`, `1E5`, `.5e3`, `0.5`, signos `+`/`-` a la cabeza:
  siguen siendo números, como antes.
- Los hex `0x...` se manejan antes en `catalogHexLiteral` y no llegan aquí.

La función principal `parseCatalogExpression` **no se tocó**; el cambio vive
íntegramente en `isCatalogNumber`, preservando el estilo de helpers chicos del
refactor reciente.

**Formas mixtas decididas (documentadas).**

- `e5.5` / `E5.5`: sin dígito de mantisa antes del exponente → **no es número**.
  Cae al identificador; el `.` actúa como separador de cualificador →
  `column` con valor `e5.5` (referencia cualificada `e5.5`). Consistente con la
  gramática: no es literal numérico válido.
- `1e` (mantisa presente, exponente sin dígitos): **no es número**; cae al
  identificador → `column` `"1e"`. Mismo borde BAJA ya existente de
  identificadores que empiezan con dígito (`0x`, `0xG` del BAJA-1), fuera del
  scope de este fix.
- `1.`: decimal válido (mantisa `1`, punto, sin fracción) → `decimal` `"1."`.

**Trade-offs.**

- La nueva implementación es estrictamente más conservadora que la anterior:
  rechaza como número todo lo que no encaje en la gramática de SQLite. Cualquier
  input que antes pasara como `decimal`/`integer` y ahora no, **no era un
  número válido en SQLite** y era clasificación incorrecta. No se pierde
  comportamiento legítimo: los literales numéricos reales (`1`, `1.5`, `1e5`,
  `.5e3`, `0x1F`) siguen clasificándose igual.
- Los bordes digit-led (`1e`, `0xG`) siguen cayendo al identificador —no se
  introducen nuevos rechazos, sólo se corrige la clasificación de
  `E5`/`e10`—. Endurecer el identificador para rechazar esos bordes es
  BAJA-1/BAJA-2, **fuera de scope**; tocarlo mezclaría perímetros.

---

## MEDIA-2 — Keywords multi-palabra exigían exactamente un espacio

**Causa.** `topLevelKeyword` buscaba la keyword con
`strings.HasPrefix(upper[i:], keyword)` donde `keyword` llevaba **un solo
espacio** (`"GROUP BY"`). `GROUP  BY` (doble espacio), `ORDER\tBY` (tab) o
cualquier whitespace interno múltiple no matcheaba; la keyword no se localizaba
y el texto residual se interpretaba como parte del table source →
`unsupported table source "t GROUP  BY x"`. SQLite y Postgres tokenizan
cualquier secuencia de whitespace como separador único, así que el SQL era
válido y el rechazo era incorrecto y con mensaje engañoso.

**Fix.**

1. Nuevo helper `keywordMatchSpan(text, start, keyword) (int, bool)`: parte la
   keyword en palabras por el espacio simple y, en cada separador de la
   keyword, **consume cualquier secuencia de whitespace** (espacios/tabs/
   newlines vía `unicode.IsSpace`) del texto; cada palabra se compara con
   `strings.EqualFold` (case-insensitive, como hoy). Devuelve el índice
   **justo después del último carácter matcheado**. Para keywords
   mono-palabra (`FROM`, `ON`, `AS`, `HAVING`, `WHERE`, `LIMIT`, `OFFSET`,
   `JOIN`) no hay separador interno y la coincidencia es verbatim, idéntica a
   antes.
2. `topLevelKeyword` reescrito para usar `keywordMatchSpan` y comprobar
   `wordBoundary` sobre el span real `[i, end)` (antes usaba `i+len(keyword)`,
   que subestimaba el largo cuando había whitespace extra). Sigue devolviendo
   la **posición inicial** (lo que todos los callers que parten el lado
   izquierdo necesitan) y sigue respetando profundidad de paréntesis y estado
   de comillas simple/doble, así que `"GROUP  BY"` dentro de un literal de
   string **no** se ve afectado (el parser ya lo manejaba; ahora se testea).
3. **Posición de corte correcta.** Los consumers que calculaban el fin del
   keyword como `position + len(keyword)` se actualizaron al span real:
   - `applyCatalogClauses`: ahora `start, _ := keywordMatchSpan(remainder,
     current.position, clauses[i].keyword)` antes de partir el valor de la
     cláusula, para no dejar un `Y` colgado cuando la keyword tuvo espacios
     extra (`GROUP  BY` → el valor arranca limpio).
   - `parseCatalogFrom` (vía `nextCatalogJoin`): el `switch` con
     `strings.HasPrefix(... single space)` para calcular `keywordLength` se
     reemplazó por `end, _ := keywordMatchSpan(remainder, start, joinKeyword)`,
     consumiendo el número correcto de caracteres para `LEFT  OUTER  JOIN`,
     `INNER  JOIN`, etc.
   - `nextCatalogJoin` pasó a devolver también la keyword matcheada
     (`(position, kind, keyword, found)`), para que `parseCatalogFrom` sepa
     qué span calcular. Distingue `LEFT OUTER JOIN` de `LEFT JOIN` porque
     `topLevelKeyword` elige la candidata de posición más temprana.
   - Las keywords mono-palabra (`FROM`, `AS`, `ON`) siguen usando
     `len(keyword)`; para ellas el span es idéntico a `len` y no había nada
     que arreglar, así que se dejó intacto para minimizar churn.

**Trade-offs.**

- `keywordMatchSpan` permite cualquier whitespace (incluido `\n`) entre las
  palabras de la keyword. Esto coincide con SQLite/Postgres. Antes el newline
  *entre* cláusulas ya funcionaba porque la keyword seguía siendo substring
  contiguo; ahora también funciona el whitespace *dentro* de la keyword
  multi-palabra.
- El costo de matching sube ligeramente: `topLevelKeyword` ahora llama a
  `keywordMatchSpan` en cada posición candidata en vez de un `HasPrefix`
  precomputado sobre `upper`. Irrelevante para volúmenes de DDL de catálogo
  (sentencias cortas, parseadas una sola vez); no se introdujo alocación nueva
  en el camino caliente (sólo `strings.Split` sobre la keyword constante, que
  el compilador no escapa a heap de forma significativa para aridades de 2-3).
- No se cambió la firma pública ni el flujo de `parseCatalogSelect` (sigue
  `stripCatalogSelectHeader` → `topLevelKeyword(FROM)` → `catalogSelectClauses`
  → `locateCatalogClauses` → `parseCatalogFrom` → `applyCatalogClauses` →
  `parseCatalogProjections`), respetando el estilo de helpers del refactor
  `983c109`.

---

## Tests nuevos

### `compat/sqlparse_test.go` (MEDIA-1)

- `TestParseCatalogExpressionNumericVersusIdentifier` (table-driven): para
  cada forma verifica **clasificación** (`Kind`/`Value`) **y DDL emitido** vía
  `compileExpression(Postgres, …)`:
  - `e5`/`E5`/`e10`/`E10`/`E3` → `column`, DDL citado (`"E5"`, `"e10"`, …).
  - `1e5`/`1E5`/`.5e3`/`0.5` → `decimal`, verbatim.
  - `0`/`16` → `integer`, verbatim. `0x1F` → `integer` `31`, verbatim.
- `TestParseCatalogExpressionExponentColumnInComparison`: `E5 > 0` →
  `gt(column "E5", integer "0")` y DDL `("E5" > 0)` (citada, no plega a `e5`).
- `TestParseCatalogExpressionExponentPrefixedMixedForms` (table-driven):
  documenta `e5.5`/`E5.5` → `column` cualificado, `.5e3` → `decimal`, `1.` →
  `decimal`, `1e` → `column` (no número).

### `compat/sqlselect_test.go` (MEDIA-2)

- `TestParseCatalogSelectToleratesKeywordInternalWhitespace` (table-driven):
  `GROUP  BY` / `GROUP\tBY` / `ORDER  BY` / `ORDER\tBY` / `HAVING  …` /
  combinados, comparados con `reflect.DeepEqual` contra la forma canónica de
  un solo espacio (misma query).
- `TestParseCatalogSelectKeywordWhitespaceDoesNotReachStringLiterals`:
  `WHERE x = 'GROUP  BY foo' ORDER BY a` → el `'GROUP  BY foo'` se preserva
  verbatim como `string` y el `ORDER BY` real sí se detecta (1 ítem).
- `TestParseCatalogSelectJoinInternalWhitespace` (table-driven): `LEFT  JOIN`,
  `LEFT  OUTER  JOIN`, `LEFT\tOUTER\tJOIN`, `INNER  JOIN` parsean con 1 JOIN,
  table `s` y `ON` eq — cortando el keyword con el largo correcto.

---

## Verificación ejecutada (Go 1.26.4, salida real pegada)

```
$ go build ./...
---BUILD OK---
$ go vet ./...
---VET OK---
$ go test ./compat/ -run 'Parse|Select|Catalog' -count=1
ok      example.com/sqlite-postgres-compat/compat   2.437s
```

(Verbose de los subtests nuevos: todos `--- PASS`, incluidos
`.../GROUP_BY_double_space`, `.../GROUP\tBY`, `.../LEFT_OUTER_JOIN_tab`,
`.../NumericVersusIdentifier/E5_column_uppercase`, etc.)

### Fallo ajeno reportado (fuera de mi perímetro)

`go test ./compat/ -count=1` (suite completa) muestra **un** fallo:

```
--- FAIL: TestCanonicalDecimalReconcilesFloatStorage (0.00s)
    store_test.go:199: expected driver float64 and capture text to converge,
    got "1.2345678901234567e+14" vs "123456789012345.67"
```

Es en `store_test.go`/`store.go`, archivos **prohibidos** que otro dev edita en
paralelo (reescritura de la reconciliación float/decimal con
`cutRealDecimalMarker`/`realDecimalMarker`, visible en `git diff
compat/store.go`). No tiene relación con el parser de expresiones de catálogo:
`isCatalogNumber` sólo se consume en `parseCatalogExpression` (CHECK/DEFAULT/
predicados), no en el camino de captura/reconciliación de `store`. Mi cambio
no puede afectar ese test. **Veredicto: mis suites (`Parse|Select|Catalog`)
verdes; el fallo es ajeno.**

No se ejecutó `git add`/`commit`/`stash` (regla). Árbol con mis 4 archivos
modificados más los ajenos (`store.go`, `capture.go`, docs) que el otro dev
mantiene.