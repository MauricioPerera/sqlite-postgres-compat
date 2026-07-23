# FIX-R1 — DECIMAL fidelity end-to-end (ALTA-1 + BAJA-1/2/3)

**Repo:** `sqlite-postgres-compat`, rama `main`.
**Scope tocado:** `compat/store.go`, `compat/store_test.go`, `compat/capture.go`, `compat/capture_test.go`, `compat/replicate.go`, `compat/replicate_test.go` (única zona permitida).
**No tocado:** `compat/sqlparse*`, `compat/sqlselect*`, `cmd/**`, `docs/**` (salvo este reporte), `e2e/**`.
**Motor:** `go1.26.4 windows/amd64`, `modernc.org/sqlite v1.54.0`.

---

## Resumen

| Hallazgo | Severidad | Estado |
|---|---|---|
| DECIMAL de precisión arbitraria (TEXT) corrompido en la cadena captura→journal→apply→verify | ALTA-1 | Cerrado |
| `printf('%.17g')` en comentarios vs `%!.17g` en código | BAJA-1 | Cerrado (ya alineado; reafirmado en las zonas reescritas) |
| Rama `FloatType` no rechaza `Inf`/`NaN` | BAJA-2 | Cerrado |
| `ApplyChanges` estricto: INSERT duplicado da error SQL opaco | BAJA-3 | Cerrado |

`go build ./...`, `go vet ./...`, `go test ./compat/ -count=1` y `go test ./... -count=1` en verde (salida real al final).

---

## ALTA-1 — Diseño elegido: marcador reservado en el journal

### Tensión de diseño (confirmada)

La reconciliación existe porque un `DECIMAL` almacenado como `REAL` se journalea como `printf('%!.17g', col)` (17 dígitos; p. ej. `"0.10000000000000001"` **es** el `%.17g` de `float64(0.1)`), mientras el driver del destino reconstruye `"0.1"`. Por **texto solo** no se puede distinguir "REAL journaleado" de "TEXT que parece float": el heurístico anterior (`isCompactFloatText`, umbral ≤17 dígitos significativos) contaba dígitos pero no verificaba que el texto fuera la repr más corta que round-tripea, así que plegaba cualquier TEXT decimal ≤17 dígitos vía `normalizeFloat`. Verificado por el PM y por AUDIT3-B.

Un fix por round-trip-check (normalizar solo si `FormatFloat(ParseFloat(text),'g',-1,64) == text`) **no basta**: el journal REAL `"0.10000000000000001"` no round-tripea a la forma más corta (`"0.1"`), entonces quedaría verbatim y rompería la reconciliación REAL (requisito (b)). Por construcción, SQLite `printf` no emite la forma más corta round-trip, así que la desambiguación **tiene que venir del tipo de almacenamiento**, no de la forma del texto. El trigger de captura **sí** conoce el `typeof`.

### Qué cambió

1. **`compat/capture.go` — rama `DecimalType` (SQLite):** antes `CASE typeof(col) WHEN 'real' THEN printf('%!.17g', col) ELSE CAST(col AS TEXT) END`. Ahora elige la rama `real` **y** la prefija con un centinela reservado:

   ```sql
   CASE typeof(col) WHEN 'real' THEN '<marker>' || printf('%!.17g', col) ELSE CAST(col AS TEXT) END
   ```

   Las ramas `text`/`integer` (y todo Postgres) siguen emitiendo `CAST AS TEXT` verbatim, **sin marcador**. `FloatType` (siempre `REAL`) sigue usando `printf('%!.17g')` sin marcador (su semántica es float, no decimal arbitrario).

2. **`compat/capture.go` — constante:**

   ```go
   const realDecimalMarker = "\x01real"
   ```

3. **`compat/store.go` — `canonicalValue(DecimalType)`:** normaliza **solo** si el valor llega como:
   - `float64`/`float32` del driver (rama `float64Value`: columna `REAL` leída vía `database/sql`, en `ExportSnapshot`/`loadRow`/verificación), o
   - texto con el prefijo `realDecimalMarker` (journal de un `REAL`-stored), o si no,
   - **verbatim byte-a-byte** (TEXT/INTEGER arbitrary-precision).

   Se eliminó el heurístico `isCompactFloatText`/`significantDigits` (queda obsoleto: la desambiguación ya no viene del conteo de dígitos).

### Por qué el marcador no colisiona con datos reales

`realDecimalMarker = "\x01real"` arranca con **SOH (`\x01`)**, un byte de control que **ningún decimal legítimo puede tener como prefijo** (los decimales arrancan con dígito, signo o punto). Es un centinela interno reservado de la capa de captura, no dato de usuario válido en una columna `DECIMAL`. Sobrevive el pipeline completo (verificado end-to-end): trigger SQL → `json_object` (JSON-escapea `\x01` como ``) → columna TEXT del journal → scan Go → `json.Unmarshal` (decodifica `` → `\x01`) → `strings.HasPrefix` → strip → `normalizeFloat`.

### Cómo satisface los tres requisitos innegociables

- **(a) TEXT-stored decimal arbitrario intacto byte a byte:** `typeof='text'` → `CAST` verbatim sin marcador → `canonicalValue` no es `float64`, no lleva marcador → **verbatim**. El trigger nunca toca el texto. Demostrado end-to-end (test `TestApplyChangesPreservesArbitraryPrecisionDecimalEndToEnd`) con los 4 inputs del PM más `"007"`, `"0.10"` y el de 38 dígitos.
- **(b) REAL-stored decimal reconcilia sin `ConflictError` espurio:** `typeof='real'` → `MARKER || printf('%!.17g')` → `canonicalValue` strip+`normalizeFloat` → forma más corta; el driver del destino lee `float64` → misma `normalizeFloat` → misma forma. Convergen. `TestApplyChangesDecimalRealStorageNoSpuriousConflict` (escenario NUMERIC-affinity del AUDIT2-B) sigue verde.
- **(c) Sin regresión en `FloatType`:** la rama `FloatType` no se toca en su rol de normalización (solo se le agrega el gate Inf/NaN de BAJA-2). `TestApplyChangesReplicatesFloat*` y `TestApplyChangesPreservesHighPrecisionFloat` siguen verdes.

### Trade-offs

- **Marker en el valor vs. campo separado:** se prefirió prefijo en el valor antes que cambiar el esquema del journal (`after_row` es JSON `columna→string`); eso habría requerido reescribir `decodeCapturedRow` y romper a los devs paralelos y los tests de captura. El prefijo es local a la rama `real` y no cambia la estructura JSON.
- **Colisión teórica:** un usuario que inserte literalmente `"\x01real0.1"` en una columna `DECIMAL` text sería mal interpretado como REAL y normalizado. Es dato **fuera de contrato** (no es un decimal); el marcador es un centinela reservado. No afecta a los inputs legítimos del ALTA.
- **`Inf`/`NaN` REAL-stored en DECIMAL:** `printf('%!.17g', Inf)` produce `"Inf"`; `normalizeFloat` lo parsea a `+Inf`. Queda fuera del alcance de este fix (es el caso BAJA-2 para `FloatType`; DECIMAL-REAL-Inf es un borde no requerido por el ALTA).

---

## BAJA-1 — Comentarios `%.17g` vs `%!.17g`

Al inspeccionar el árbol actual, **todos** los comentarios ya citan `%!.17g` (grep de `%.17g` sin `!` no encuentra nada); el hallazgo del AUDIT3-B estaba sobre line numbers de un estado previo. Reafirmado: las zonas de comentarios que el fix del ALTA reescribió (`capture.go` rama `DecimalType`, `store.go` rama `DecimalType`) ahora documentan fielmente `printf('%!.17g')` y el rol del marcador.

---

## BAJA-2 — `FloatType` rechaza `Inf`/`NaN`

La rama `FloatType` antes llamaba `normalizeFloat` incondicionalmente, produciendo `"+Inf"`/`"NaN"` sin rechazarlos (asimetría con `DecimalType`, cuyo gate excluye no-finitos). Ahora parsea y **rechaza con error claro**:

```go
parsed, err := strconv.ParseFloat(text, 64)
if err != nil { return ..., fmt.Errorf("invalid float %q: %w", text, err) }
if math.IsInf(parsed, 0) || math.IsNaN(parsed) {
    return ..., fmt.Errorf("invalid float %q: Inf/NaN are not supported", text)
}
return Value{Kind: FloatValue, Value: strconv.FormatFloat(parsed, 'g', -1, 64)}, nil
```

Paridad con cómo el resto del código rechaza valores imposibles (p. ej. `canonicalVectorValue`, `decodeCapturedRow`). Cubre texto (`"inf"`, `"NaN"`, variantes de caso/signo) y `float64` no-finitos del driver. Test: `TestCanonicalFloatRejectsInfNaN`. Los floats finitos siguen canonicalizando igual (`"1.5"` → `"1.5"`).

---

## BAJA-3 — INSERT duplicado en modo estricto → `ConflictError`

`applyChange` rama `Insert` (modo no-tolerant) antes hacía `return insertRow(...)`, devolviendo la violación de unicidad cruda del driver. Ahora envuelve el caso "PK ya existe" en un `*ConflictError` con contexto (tabla, PK, y filas divergentes cuando el conflicto es observable), preservando la semántica estricta (sigue siendo error, la fila no se modifica, la transacción rollbackea):

```go
if err := insertRow(ctx, tx, engine, table, change.After); err != nil {
    actual, found, lookupErr := loadRow(ctx, tx, engine, table, change.PrimaryKey)
    if lookupErr == nil && found {
        return &ConflictError{Table: table.Name, PrimaryKey: change.PrimaryKey, Expected: change.After, Actual: actual}
    }
    return err
}
return nil
```

Otros fallos de INSERT (no-colisión de PK) se devuelven crudos sin enmascarar. Test: `TestApplyChangesInsertDuplicateRaisesConflictError` (verifica `errors.As(err, &conflict)`, tabla, PK, y que la fila existente queda intacta). El camino `Tolerant` no se modifica.

---

## Tests cambiados como consecuencia directa del diseño

Dos asserts existentes cambiaron **exclusivamente** porque el diseño mueve la desambiguación del texto al tipo de almacenamiento (marcador). Sin el cambio, la nueva semántica los haría contradictorios.

### 1. `TestCompileCaptureTriggersUsesRoundTripFloatEncodingOnSQLite` (`compat/capture_test.go`)

- **Antes:** `strings.Contains(sqliteJoined, "WHEN 'real' THEN printf('%!.17g', ")`
- **Después:** `strings.Contains(sqliteJoined, "WHEN 'real' THEN " + sqlString(realDecimalMarker) + " || printf('%!.17g', ")`
- **Razón:** el trigger ahora emite `MARKER || printf('%!.17g')` en la rama `real`; el assert de la SQL del trigger debe reflejarlo. Se mantienen los asserts de `CASE typeof(`, `printf('%!.17g', ` (FloatType), y Postgres sin `printf` / con `CAST`.

### 2. `TestCanonicalDecimalReconcilesFloatStorage` (`compat/store_test.go`)

- **Antes:** `fromText := canonicalValue(DecimalType, "123456789012345.67")` (texto plano) y `fromRounded := canonicalValue(DecimalType, "99999999999999.984")` (texto plano), ambos esperando converger con el `float64` del driver.
- **Después:** `fromText := canonicalValue(DecimalType, realDecimalMarker+"123456789012345.67")` y `fromRounded := canonicalValue(DecimalType, realDecimalMarker+"99999999999999.984")`.
- **Razón:** el texto plano `"123456789012345.67"` ahora es **TEXT-stored** → verbatim (no reconcilia). Para representar el texto REAL-journaleado que el trigger emite (el caso que este test protege), hay que pasar el marcador. El comentario del test se actualizó para explicarlo. Las aserciones de valor (`"1.2345678901234567e+14"`, integer verbatim, 38 dígitos verbatim) se mantienen idénticas.

Los asserts de `TestCompileCaptureTriggersUsesTransactionLocalGUCOnPostgres`, `TestDecodeCapturedRowRejectsMissingColumn`, `TestApplyChangesDecimalRealStorageNoSpuriousConflict`, `TestApplyChangesPreservesArbitraryPrecisionDecimal` y demás **no cambiaron**.

---

## Tests nuevos

| Test | Archivo | Qué cubre |
|---|---|---|
| `TestCanonicalDecimalPreservesArbitraryPrecisionText` | `store_test.go` | Unit table-driven de `canonicalValue(DecimalType)` verbatim: los 4 inputs del PM + `"007"`, `"0.10"`, 38 dígitos, y `"0.1"` (round-trip). |
| `TestApplyChangesPreservesArbitraryPrecisionDecimalEndToEnd` | `replicate_test.go` | **Definition of done ALTA:** SQLite `:memory:` con captura instalada → INSERT del texto exacto → `ReadCapturedChanges` → `ApplyChanges` → leer destino y comparar byte a byte, + `VerifySnapshots` equivalent. Los 4 inputs del PM intactos. |
| `TestCanonicalFloatRejectsInfNaN` | `store_test.go` | BAJA-2: `inf`/`+inf`/`-inf`/`Inf`/`nan`/`NaN`/`float64(±Inf)`/`math.NaN()` rechazados; `"1.5"` finito sigue canonicalizando. |
| `TestApplyChangesInsertDuplicateRaisesConflictError` | `replicate_test.go` | BAJA-3: INSERT de PK ya presente → `*ConflictError` con tabla/PK; la fila existente queda intacta (semántica estricta). |

---

## Evidencia de ejecución (salida real)

```
$ go build ./... && go vet ./... && go test ./compat/ -count=1
ok  	example.com/sqlite-postgres-compat/compat	2.069s

$ go test ./... -count=1
?  	example.com/sqlite-postgres-compat/cmd/compat-audit	[no test files]
?  	example.com/sqlite-postgres-compat/cmd/compat-copy	[no test files]
?  	example.com/sqlite-postgres-compat/cmd/compat-cutover	[no test files]
ok  	example.com/sqlite-postgres-compat/cmd/internal/cliout	2.438s
ok  	example.com/sqlite-postgres-compat/compat	2.261s

$ go test ./compat/ -count=1 -run 'Decimal|FloatRejects|InsertDuplicate|CaptureTriggersUsesRoundTrip|Reconciles' -v
=== RUN   TestCompileCaptureTriggersUsesRoundTripFloatEncodingOnSQLite
--- PASS: TestCompileCaptureTriggersUsesRoundTripFloatEncodingOnSQLite (0.00s)
=== RUN   TestSQLiteDecimalUsesLosslessTextStorage
--- PASS: TestSQLiteDecimalUsesLosslessTextStorage (0.00s)
=== RUN   TestApplyChangesDecimalRealStorageNoSpuriousConflict
--- PASS: TestApplyChangesDecimalRealStorageNoSpuriousConflict (0.00s)
=== RUN   TestApplyChangesPreservesArbitraryPrecisionDecimal
--- PASS: TestApplyChangesPreservesArbitraryPrecisionDecimal (0.00s)
=== RUN   TestApplyChangesPreservesArbitraryPrecisionDecimalEndToEnd
--- PASS: TestApplyChangesPreservesArbitraryPrecisionDecimalEndToEnd (0.00s)
=== RUN   TestApplyChangesInsertDuplicateRaisesConflictError
--- PASS: TestApplyChangesInsertDuplicateRaisesConflictError (0.00s)
=== RUN   TestCanonicalFloatRejectsInfNaN
--- PASS: TestCanonicalFloatRejectsInfNaN (0.00s)
=== RUN   TestCanonicalDecimalReconcilesFloatStorage
--- PASS: TestCanonicalDecimalReconcilesFloatStorage (0.00s)
=== RUN   TestCanonicalDecimalPreservesArbitraryPrecisionText
=== RUN   TestCanonicalDecimalPreservesArbitraryPrecisionText/seventeen-digit_non-round-trip
=== RUN   TestCanonicalDecimalPreservesArbitraryPrecisionText/scale-bearing
=== RUN   TestCanonicalDecimalPreservesArbitraryPrecisionText/seventeen-digit_magnitude
=== RUN   TestCanonicalDecimalPreservesArbitraryPrecisionText/seventeen-nines_integer
=== RUN   TestCanonicalDecimalPreservesArbitraryPrecisionText/leading_zeros
=== RUN   TestCanonicalDecimalPreservesArbitraryPrecisionText/trailing_zero_scale
=== RUN   TestCanonicalDecimalPreservesArbitraryPrecisionText/thirty-eight-digit
=== RUN   TestCanonicalDecimalPreservesArbitraryPrecisionText/round-trip_float_text_stays_equal
--- PASS: TestCanonicalDecimalPreservesArbitraryPrecisionText (0.00s)
    --- PASS: .../seventeen-digit_non-round-trip (0.00s)
    --- PASS: .../scale-bearing (0.00s)
    --- PASS: .../seventeen-digit_magnitude (0.00s)
    --- PASS: .../seventeen-nines_integer (0.00s)
    --- PASS: .../leading_zeros (0.00s)
    --- PASS: .../trailing_zero_scale (0.00s)
    --- PASS: .../thirty-eight-digit (0.00s)
    --- PASS: .../round-trip_float_text_stays_equal (0.00s)
PASS
ok  	example.com/sqlite-postgres-compat/compat	2.403s

$ gofmt -l <archivos tocados>   → exit 0 (sin archivos a formatear)
```

`go build ./...`: limpio. `go vet ./...`: limpio.