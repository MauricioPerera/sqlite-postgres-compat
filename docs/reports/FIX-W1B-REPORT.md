# FIX-W1B — Close the vector dimension gap in replication

## Brecha (de FIX-W1-REPORT.md)

`canonicalValue` es variádica: `canonicalValue(kind TypeFamily, source any, dimension ...int)`.
La cuenta de componentes solo se valida cuando se pasa la dimensión declarada.
`exportTable` (snapshot) la pasaba vía `column.Type.Arguments...`, pero los caminos de
replicación la omitían:

- `compat/capture.go:225` — `decodeCapturedRow` (lectura del journal) llamaba
  `canonicalValue(column.Type.Family, source)` sin dimensión.
- `compat/replicate.go:176` — `loadRow` (reconstrucción de la fila actual del destino para
  el chequeo de conflictos) llamaba `canonicalValue(column.Type.Family, values[i])` sin
  dimensión.

Resultado: un vector con cuenta de componentes distinta a la dimensión declarada cruzaba la
replicación en silencio. El carrier TEXT de SQLite acepta cualquier texto entre corchetes,
así que el valor mal dimensionado se inserta directo y se journaliza verbatim.

## Resolución

Ambos call sites tienen la `Column` (con su `Type`) a mano porque iteran `table.Columns`, y
la `table` ya se resuelve desde el `Schema` que la función recibe (`findTable`). No hizo
falta resolución por nombre ni cambios estructurales. Se espejó el patrón de `exportTable`
(`compat/store.go:143-147`): ramificar por `VectorType` y pasar `column.Type.Arguments...`
solo para esa familia; las demás omiten el argumento variádico.

- `compat/capture.go` — `decodeCapturedRow`: ahora pasa `column.Type.Arguments...` para
  `VectorType`. Un valor mal dimensionado en el journal produce error explícito al
  `ReadCapturedChanges`.
- `compat/replicate.go` — `loadRow`: ahora pasa `column.Type.Arguments...` para
  `VectorType`. Un valor mal dimensionado almacenado en el destino produce error explícito
  al reconstruir la fila para el chequeo de conflictos (`ApplyChanges` Update/Delete).

No se cambiaron firmas públicas. Comentarios y tests en inglés, estilo del archivo. Tests
preexistentes intactos.

## Archivos tocados

- `compat/capture.go`
- `compat/capture_test.go`
- `compat/replicate.go`
- `compat/replicate_test.go`

(Solo los permitidos por el enunciado.)

## Test de regresión

1. `TestSQLiteCaptureRejectsVectorDimensionMismatch` (`capture_test.go`): esquema con
   columna `vector` dimensión 3, captura instalada en SQLite origen; se inserta DIRECTO
   `'[1, 2]'` (el carrier TEXT no lo impide); `ReadCapturedChanges` devuelve error que
   menciona `dimension`.
2. `TestSQLiteCaptureReplicatesDimensionedVector` (`capture_test.go`): mismo flujo con
   `'[1, 2.0, 3]'`; `ReadCapturedChanges` replica OK a un destino SQLite y el valor
   canónico llegado es `'[1,2,3]'`.
3. `TestApplyChangesRejectsDestinationVectorDimensionMismatch` (`replicate_test.go`):
   camino `loadRow` — el destino almacena `'[1, 2]'` contra dimensión 3; un `Update`
   vía `ApplyChanges` produce error que menciona `dimension` (no paso silencioso, no
   `ConflictError` silencioso).

## Salida real

### Tests nuevos (verboso)

```
$ go test ./compat/ -run 'TestSQLiteCaptureRejectsVectorDimensionMismatch|TestSQLiteCaptureReplicatesDimensionedVector|TestApplyChangesRejectsDestinationVectorDimensionMismatch' -v
=== RUN   TestSQLiteCaptureRejectsVectorDimensionMismatch
--- PASS: TestSQLiteCaptureRejectsVectorDimensionMismatch (0.00s)
=== RUN   TestSQLiteCaptureReplicatesDimensionedVector
--- PASS: TestSQLiteCaptureReplicatesDimensionedVector (0.00s)
=== RUN   TestApplyChangesRejectsDestinationVectorDimensionMismatch
--- PASS: TestApplyChangesRejectsDestinationVectorDimensionMismatch (0.00s)
PASS
ok  	example.com/sqlite-postgres-compat/compat	2.162s
```

### Baseline completa

```
$ go build ./... && go vet ./... && go test ./...
?   	example.com/sqlite-postgres-compat/cmd/compat-audit	[no test files]
?   	example.com/sqlite-postgres-compat/cmd/compat-copy	[no test files]
ok  	example.com/sqlite-postgres-compat/compat	2.135s
```

TODO verde: `go build`, `go vet` y `go test ./...` pasan. Tests preexistentes no modificados
ni borrados.