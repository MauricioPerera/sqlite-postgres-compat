# FIX-F1 — Parser de catálogo: precedencia de NOT y literal hexadecimal

## Resumen

Se corrigieron los dos bugs confirmados en `compat/sqlparse.go` (`parseCatalogExpression`)
sin tocar archivos fuera de `compat/sqlparse.go` y `compat/sqlparse_test.go`. La suite
existente se mantiene intacta (solo se agregaron tests). `go build`, `go vet` y `go test`
pasan en verde.

## BUG 1 — Precedencia de NOT invertida

**Antes:** `NOT` se aplicaba como prefijo de máxima precedencia, después de intentar
todos los niveles binarios (OR, AND, IS NULL, comparaciones, +-, */). Eso hacía que
`NOT a = b` se partiera primero en `=`, dando `eq(not(column a), column b)`.

**Fix:** Se movió `NOT` a su nivel de precedencia correcto — entre `AND` (índice 1) e
`IS NULL` / comparaciones (índice 2+) — dentro del mismo bucle de niveles. NOT ahora se
evalúa **antes** que las comparaciones / IS NULL / LIKE (ata más flojo que ellos) y
**después** que AND/OR (ata más fuerte que ellos), coincidiendo con la semántica de
SQLite/SQL estándar.

Cambio concreto en `parseCatalogExpression`: el bucle pasó de `for _, level := range [...]`
a un slice nombrado `levels` iterado por `index`, con una rama en `index == 2` que detecta
`NOT` vía `hasKeywordPrefix` y recursiona sobre el resto. Se eliminó el bloque `NOT` que
estaba después del bucle (ahora es inalcanzable/redundante).

Casos cubiertos (todos verdes):
- `NOT a = b` → `not(eq(a, b))`
- `NOT a IS NULL` → `not(is_null(a))`
- `NOT a LIKE 'x%'` → `not(like(a, 'x%'))`
- `a AND NOT b = c` → `and(a, not(eq(b, c)))`
- `NOT (a = b)` → `not(eq(a, b))` (paréntesis explícito)
- `NOT a` → `not(column a)` (sin regresión)

## BUG 2 — Literal hexadecimal como columna

**Antes:** `isCatalogNumber` rechazaba `0x10` (la `x` no está en el set `.+ -eE`), y
`parseCatalogIdentifier` lo aceptaba como identificador `"0x10"`, compilando a Postgres
como `"0x10"` (identificador citado).

**Fix:** Se agregó `catalogHexLiteral` (más el helper `isHexDigit`), invocado antes de
`isCatalogNumber`. Reconoce `0x` / `0X` seguido de dígitos hex válidos, decodifica con
`strconv.ParseUint(digits, 16, 64)` y emite el **valor decimal** como string. Literales
que exceden 64 bits producen un error explícito `unsupported catalog hexadecimal literal`.
El AST resultante compila a Postgres como el entero decimal (p.ej. `16`), no como
identificador.

Casos cubiertos (todos verdes):
- `status = 0x10` → `eq(column status, integer "16")`
- `x = 0XABCDEF` → `eq(column x, integer "11259375")`
- `0xFFFFFFFFFFFFFFFFF` (17 hex = >64 bits) → error `unsupported` (nunca aceptado en silencio)

## Trade-off / decisión de diseño

La especificación sugería emitir `kind "number"`. Los renderizadores existentes
(`compat/ddl.go` `compileExpression` caso `"integer", "decimal"` y
`compat/runtime.go` `routineValue` caso `"integer"`) **solo** manejan `integer`/`decimal`.
Emitir `kind "number"` habría requerido tocar `ddl.go` y `runtime.go` (prohibido por
ARCHIVOS) y, sin eso, cualquier literal hex en un CHECK/WHERE/WHEN real **no compilaría**
a Postgres — una regresión latente. Por eso se emite `kind "integer"` con el valor
decimal: cumple el intento funcional (compila a `16`, no a `"0x10"`) usando el caso
existente, sin tocar otros archivos. El valor decimal se conserva íntegro (ej. `11259375`).

Hex con signo (`+0x10`, `-0x10`) no se trata como hex (cae a las reglas existentes,
que ya lo rechazaban como `unsupported`); no se debilitó el parser. `0x` sin dígitos
y `0xGG` (dígitos no hex) caen al identificador, preservando el comportamiento previo.

## Verificación (salida real)

```
$ go build ./...
---BUILD EXIT 0---

$ go vet ./...
---VET EXIT 0---

$ go test ./...
?       example.com/sqlite-postgres-compat/cmd/compat-audit       [no test files]
?       example.com/sqlite-postgres-compat/cmd/compat-copy        [no test files]
ok      example.com/sqlite-postgres-compat/compat        2.353s
---TEST EXIT 0---
```

Tests nuevos (verboso, subconjunto relevante):

```
=== RUN   TestParseCatalogExpressionNotPrecedence
--- PASS: TestParseCatalogExpressionNotPrecedence (0.00s)
    --- PASS: .../not_binds_looser_than_equality
    --- PASS: .../not_binds_looser_than_is_null
    --- PASS: .../not_binds_looser_than_like
    --- PASS: .../not_binds_tighter_than_and_on_the_right_hand_side
    --- PASS: .../explicit_parentheses_keep_not_around_the_comparison
    --- PASS: .../not_over_a_bare_column_does_not_regress
=== RUN   TestParseCatalogExpressionHexLiteral
--- PASS: TestParseCatalogExpressionHexLiteral (0.00s)
    --- PASS: .../lowercase_hex_prefix
    --- PASS: .../uppercase_hex_prefix_and_digits
    --- PASS: .../hex_literal_beyond_64_bits_is_rejected
PASS
```

Tests preexistentes NO modificados: `TestParseCatalogExpression`,
`TestParseCatalogExpressionRejectsUnknownSQL`,
`TestParsePostgresCatalogDefaultRemovesKnownLiteralCast` siguen pasando sin cambios.

## Archivos tocados

- `compat/sqlparse.go` (parser + helpers `catalogHexLiteral`/`isHexDigit`, import `strconv`)
- `compat/sqlparse_test.go` (tests table-driven agregados; import `strings`)
- `FIX-F1-REPORT.md` (este reporte)

No se tocaron `store.go`, `capture.go`, `replicate.go`, `*_test.go` asociados ni ningún
otro archivo. No se cambió la API pública.