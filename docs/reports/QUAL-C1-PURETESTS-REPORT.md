# QUAL-C1 — Tests unitarios para funciones puras a 0% de cobertura

**Fecha:** 2026-07-22
**Autor:** dev efímero (calidad)
**Alcance:** tests unitarios para 5 funciones puras que no requieren PostgreSQL.
**Archivos tocados (solo tests):** `compat/inspect_test.go`, `compat/verify_test.go`, `compat/schema_test.go`.
**Archivos de producción NO tocados.** No se tocó `cmd/**` ni `e2e/**`.

## 1. Funciones testeadas

| # | Función | Archivo:línea | Cobertura lograda |
|---|---------|--------------|-------------------|
| 1 | `validReferentialAction` | `compat/schema.go:350` | 100.0% |
| 2 | `sqliteReferentialAction` | `compat/inspect.go:597` | 100.0% |
| 3 | `postgresReferentialAction` | `compat/inspect.go:614` | 100.0% |
| 4 | `extractIndexPredicate` | `compat/inspect.go:781` | 100.0% |
| 5 | `RequireEquivalent` | `compat/verify.go:55` | 100.0% |

Cobertura por función verificada con `go tool cover -func`.

## 2. Tests agregados

### `compat/schema_test.go`
- `TestValidReferentialActionAcceptsCanonicalActions` — acepta `""`, `NoAction`, `Restrict`, `Cascade`, `SetNull`, `SetDefault`.
- `TestValidReferentialActionRejectsUnknownActions` (table-driven) — rechaza variantes no canónicas: `"no action"` (spelling de Postgres), `"CASCADE"`, `"restrict "` (espacio), `"bogus"`, `" "` (solo whitespace), `"set null"` (con espacio).

### `compat/inspect_test.go`
- `TestSqliteReferentialActionParsesCanonicalKeywords` — `"NO ACTION"→""`, `"RESTRICT"→Restrict`, `"CASCADE"→Cascade`, `"SET NULL"→SetNull`, `"SET DEFAULT"→SetDefault`.
- `TestSqliteReferentialActionRejectsUnknownAndIsCaseSensitive` — `""`, `"no action"`, `"Restrict"`, `"cascade"`, `"CASCADE "` (espacios), `" CASCADE"`, `"CASCADE DELETE"`, `"bogus"`; verifica que lo desconocido retorna `("", false)`.
- `TestPostgresReferentialActionParsesSingleLetterCodes` — `"a"→""`, `"r"→Restrict`, `"c"→Cascade`, `"n"→SetNull`, `"d"→SetDefault`.
- `TestPostgresReferentialActionRejectsUnknownAndIsCaseSensitive` — `""`, `"A"` (case), `"cascade"` (palabra), `"ac"` (multi-letra), `"x"`, `" "`; verifica `("", false)`.
- `TestExtractIndexPredicate` (table-driven, 13 casos) — predicado simple, sin `WHERE`, definición vacía, preservación de case original pese a match upper, `WHERE` en case mixto, trim de espacios, `WHERE` sin espacio anterior/posterior ignorado, último `WHERE` gana, `WHERE` como subcadena dentro de otra palabra (`elsewhere`) ignorado, predicado con paréntesis anidados, y solo-keyword sin predicado.

### `compat/verify_test.go`
- `TestRequireEquivalentAcceptsMatchingSnapshots` — `Equivalent: true` → `nil`.
- `TestRequireEquivalentRejectsMismatchedSnapshots` (table-driven) — digest distintos, digests vacíos, y caso defensivo (`Equivalent: false` con digests iguales); verifica que el error menciona `"snapshot mismatch"`, el digest origen y el destino.

## 3. Comportamiento observado / hallazgos (no se modificó código)

Se testeó el comportamiento **real** de cada función. Hallazgos documentados (no son bugs a corregir bajo este alcance; se reportan como observaciones):

1. **`sqliteReferentialAction("NO ACTION")` retorna `""` (cero), no `NoAction`.**
   La cadena SQL `"NO ACTION"` se mapea al valor cero `ReferentialAction("")` y no a la constante `NoAction = "no_action"`. Ambos son aceptados por `validReferentialAction`, y `ddl.go` omite la cláusula cuando la acción es `""` o `NoAction`, así que el efecto neto es consistente. Es una dualidad `""` vs `NoAction` que conviene tener presente si alguna comparación con `==` asume un único representante.

2. **`postgresReferentialAction("a")` (NO ACTION) también retorna `""`, no `NoAction`.** Misma dualidad que el punto 1, del lado Postgres.

3. **`extractIndexPredicate` busca `" WHERE "` con espacios obligatorios a ambos lados.** Por eso `"WHERE"` pegado al paréntesis (`(a)WHERE`) o pegado al predicado (`WHEREa`) no se reconocen → retorna `""`. El match es case-insensitive (se busca sobre el texto uppercased) pero el valor retornado conserva el case original del input. Usa `LastIndex`, así que ante múltiples `WHERE` devuelve el último.

4. **`RequireEquivalent` confía en el flag `Equivalent`** y no recomputa ni compara digests; por eso un reporte con `Equivalent: false` y digests idénticos igual produce error (testeado explícitamente). Es comportamiento defensivo intencional: el flag es la fuente de verdad.

Ningún comportamiento se "arregló"; los tests fijan lo que el código hace hoy.

## 4. Definición de hecho — verificación

### 4.1 Cobertura (antes / después)

**Antes (baseline, esta sesión):**
```
ok  example.com/sqlite-postgres-compat/compat  2.194s  coverage: 65.3% of statements
```

**Después:**
```
ok  example.com/sqlite-postgres-compat/compat  2.200s  coverage: 66.4% of statements
```

> Nota: el baseline informado en el brief era 64.8%; la corrida real de esta sesión mostró 65.3% (el denominador se mueve porque otro dev refactoriza `compat/inspect.go` en paralelo). La subida neta atribuible a estos tests es +1.1 pp sobre el baseline observado (65.3% → 66.4%), y supera tanto el baseline del brief (64.8%) como el observado (65.3%).

### 4.2 Tests listados (corrida dirigida)

```
go test ./compat/ -run 'ReferentialAction|IndexPredicate|RequireEquivalent' -count=1 -v
```
Resultado: **PASS**, con `TestValidReferentialAction*` (corre bajo `ReferentialAction`), `TestSqliteReferentialAction*`, `TestPostgresReferentialAction*`, `TestExtractIndexPredicate`, `TestRequireEquivalent*` todos listados y en verde (subtests table-driven incluidos).

### 4.3 Cobertura por función (post-tests)
```
compat/schema.go:350:  validReferentialAction    100.0%
compat/inspect.go:597: sqliteReferentialAction  100.0%
compat/inspect.go:614: postgresReferentialAction 100.0%
compat/inspect.go:781: extractIndexPredicate    100.0%
compat/verify.go:55:   RequireEquivalent        100.0%
```

## 5. Restricciones respetadas
- Solo se editaron archivos `_test.go` en `compat/`. No se borraron ni modificaron tests existentes.
- No se tocaron archivos de producción (`compat/*.go` sin `_test`), ni `cmd/**`, ni `e2e/**`.
- Sin procesos en foreground persistentes; nada fuera del repo; sin `git add`/`commit`.
- Código y comentarios en inglés, tests table-driven consistentes con el estilo de cada archivo.