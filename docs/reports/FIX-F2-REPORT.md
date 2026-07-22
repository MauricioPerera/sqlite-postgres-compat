# FIX-F2 — Replicación de columnas FloatType (SQLite REAL)

## Causa raíz (confirmada)

Dos productoras de texto distintas alimentan la misma comparación byte a byte
(`rowsEqual`, `replicate.go:248`) sin normalización previa para `FloatType`:

- **Captura** (`capture.go:129`): el trigger usa SQL `CAST(col AS TEXT)`.
  SQLite devuelve `CAST(1.0 AS TEXT)` = `"1.0"`. Ese texto llega al `before`/`after`
  del journal vía `decodeCapturedRow` → `canonicalValue(FloatType, "1.0")`.
- **Reconstrucción de fila actual** (`replicate.go:148` `loadRow` →
  `store.go:244` `canonicalValue` → `stringify`): escanea `float64(1.0)` y
  `fmt.Sprint(float64(1.0))` = `"1"`.

`canonicalValue` para `FloatType` copiaba el texto tal cual (`store.go:266-267`),
sin normalizar. Resultado: `before = "1.0"` vs `actual = "1"` → `rowsEqual`
falso negativo → `ConflictError` espurio en update/delete; los deletes dejaban la
fila en el destino (divergencia silenciosa, porque el `ConflictError` abortaba
el `ApplyChanges` y la transacción entera se rollbackeaba).

## Fix

Normalización en la capa de canonicalización de valores, **sin debilitar la
comparación**. `rowsEqual` y la detección de conflictos quedan estrictas.

`compat/store.go` — rama `FloatType` de `canonicalValue` ahora parsea el texto
como `float64` y lo re-formatea canónicamente vía un helper nuevo
`normalizeFloat`:

```go
func normalizeFloat(text string) (string, error) {
	parsed, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return "", err
	}
	return strconv.FormatFloat(parsed, 'g', -1, 64), nil
}
```

Ambas productoras convergen: `"1.0"` → `1.0` → `"1"` y `float64(1)` →
`fmt.Sprint` `"1"` → `1.0` → `"1"`. `"1.5"` se preserva. Texto no numérico →
`ParseFloat` error explícito envuelto en `fmt.Errorf("invalid float %q: %w", ...)`.

## Archivos tocados

- `compat/store.go` — rama `FloatType` de `canonicalValue` + helper `normalizeFloat`.
- `compat/store_test.go` — `TestCanonicalFloatNormalization`.
- `compat/replicate_test.go` — `TestApplyChangesReplicatesFloatColumn`,
  `TestApplyChangesReplicatesFloatUpdateApplied` + helper `floatTestSchema`.

**No se tocaron**: `compat/capture.go` (no hizo falta), `compat/sqlparse.go`,
`compat/sqlparse_test.go`, ni ningún otro archivo. API pública sin cambios.
Ramas `TimestampType`/`JSONType`/`UUID`/`Boolean` de `canonicalValue` intactas.

## Tests

- **Regresión nuevo** (`replicate_test.go`): esquema con columna `FloatType`,
  captura en origen SQLite (`InstallChangeCapture`), `insert` + `update` +
  `delete` replicados a destino SQLite vía `ReadCapturedChanges`/`ApplyChanges`.
  Verifica: sin `ConflictError`, `update` aplicado (`flt = 2.5` observable) y
  fila ausente tras `delete`. Incluye reaplicación idempotente.
- **Unitario nuevo** (`store_test.go`): `canonicalValue(FloatType, "1.0")` ==
  `canonicalValue(FloatType, "1")` == `"1"`; `"1.5"` preservado;
  `float64(1)` coincide con el canónico `"1"`; texto no numérico → error.
- **Conflicto genuino preexistente**
  (`TestApplyChangesDetectsConflictAndRollsBack`): **verde sin modificarse** —
  un conflicto real (texto distinto) sigue detectado y la fila local se
  preserva por rollback.

## Trade-offs declarados

- **Precisión / round-trip de floats**: se usa `strconv.FormatFloat(f, 'g', -1,
  64)`, que produce la representación más corta que round-trip-ea al mismo
  `float64`. Es canónica y estable para igualdad, pero **no es round-trip
  estable respecto al texto original**: un valor capturado como `"1.0"` se
  persiste/reformattea como `"1"`, y `"1.50"` como `"1.5"`. Es el
  comportamiento deseado (canonizar), pero implica que el texto float en el
  destino puede diferir en forma del texto capturado en origen aunque
  numéricamente sean iguales. Para valores muy grandes/muy chicos, `'g'` puede
  emitir notación científica (p.ej. `1e+20`); sigue siendo canónico y
  consistente entre ambas productoras, pero la forma difiere de un `'f'`
  literal. Esto se acepta a favor de una forma canónica única y compacta.
- **No se debilita `rowsEqual`**: sigue siendo comparación JSON byte a byte. La
  igualdad se logra por canonización previa, no por tolerancia en la
  comparación. Dos floats numéricamente distintos siguen produciendo
  `ConflictError` legítimo (verificado por el test de conflicto preexistente).

## Salida real de los comandos

```
$ go build ./... && go vet ./... && go test ./compat/... -run 'Float|Conflict|ApplyChanges' -v
=== RUN   TestApplyChangesIsTransactionalAndIdempotent
--- PASS: TestApplyChangesIsTransactionalAndIdempotent (0.00s)
=== RUN   TestApplyChangesDetectsConflictAndRollsBack
--- PASS: TestApplyChangesDetectsConflictAndRollsBack (0.00s)
=== RUN   TestApplyChangesReplicatesFloatColumn
--- PASS: TestApplyChangesReplicatesFloatColumn (0.00s)
=== RUN   TestApplyChangesReplicatesFloatUpdateApplied
--- PASS: TestApplyChangesReplicatesFloatUpdateApplied (0.00s)
=== RUN   TestCanonicalFloatNormalization
--- PASS: TestCanonicalFloatNormalization (0.00s)
PASS
ok  	example.com/sqlite-postgres-compat/compat	2.237s
```

```
$ go build ./... && go vet ./... && go test ./...
?   	example.com/sqlite-postgres-compat/cmd/compat-audit	[no test files]
?   	example.com/sqlite-postgres-compat/cmd/compat-copy	[no test files]
ok  	example.com/sqlite-postgres-compat/compat	2.111s
```