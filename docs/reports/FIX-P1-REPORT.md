# FIX-P1-REPORT — Race de supresión anti-eco en Postgres

## Resumen

Sí/no: **LISTO.** La supresión anti-eco en la rama Postgres pasa de bandera
global (fila única en `__compat_capture_state`) a **GUC transaction-local**
`compat.suppress`, eliminando la race de MVCC. La rama SQLite queda exactamente
como estaba. Cero cambio de API pública.

## Bug confirmado

`setCaptureSuppressed` hacía `UPDATE __compat_capture_state SET suppress=1` dentro
de la tx de `ApplyChanges`. Bajo MVCC de Postgres, una segunda `ApplyChanges`
concurrente no veía el `suppress=1` no-commiteado de la primera → sus triggers
journalizaban el eco → ruptura de idempotencia. SQLite estaba protegido por
`SetMaxOpenConns(1)` (`store.go:48`).

## Diseño aplicado (cerrado por el PM, no modificado)

- **Postgres, armado:** `setCaptureSuppressed(true)` ejecuta
  `SELECT set_config('compat.suppress', '1', true)` (tercer arg `true` =
  transaction-local: se resetea solo en COMMIT/ROLLBACK, invisible para otras tx).
- **Postgres, condición de trigger:** los triggers de captura PG cambian su
  guarda a `current_setting('compat.suppress', true) IS DISTINCT FROM '1'`
  (segundo arg `true` = `missing_ok`, no error si la GUC no existe → devuelve
  NULL → `NULL IS DISTINCT FROM '1'` es true → journaliza escrituras normales).
- **Postgres, desarmado:** `setCaptureSuppressed(false)` es **no-op**. El reset
  es automático al final de la tx, y no hay escrituras entre el "clear" y el
  `Commit`, así que dejar la GUC seteada hasta el commit es inofensivo. Ver
  comentario doc en `compat/replicate.go` (`setCaptureSuppressed`).
- **SQLite:** sin cambios. Sigue usando `__compat_capture_state` (tabla + fila
  id=1 + `UPDATE`). Seguro por conexión única.

## Decisiones de diff (documentadas)

1. **`__compat_capture_state` se sigue creando en ambos engines.**
   `createAppliedChangesTable` la crea y siembra la fila id=1 en SQLite y
   Postgres. En Postgres queda **creada y sin uso** por los triggers (lo
   prohibido era que los triggers PG dependan de la tabla, no que la tabla
   exista). Esto mantiene el helper engine-agnóstico y el diff mínimo. Los
   filtros de inspección (`compat/inspect.go`, `compat/schema.go`) ya excluyen
   esta tabla por nombre, así que no aparece en el catálogo canónico.
2. **`setCaptureSuppressed` ahora recibe `engine Engine`.** Firma:
   `setCaptureSuppressed(ctx, tx, engine, suppressed)`. Únicos dos callers están
   en `ApplyChanges` y pasan `store.Target.Engine`. No es API pública (función
   no exportada), así que no rompe contrato.
3. **No-op documentado para `false` en Postgres** (ver punto arriba y doc-comment).

## Archivos tocados

- `compat/replicate.go` — `setCaptureSuppressed` engine-aware (GUC en PG, tabla
  en SQLite); callers pasan `store.Target.Engine`; doc-comment con la decisión
  no-op y la justificación MVCC.
- `compat/capture.go` — condición del trigger PG → `current_setting(...)`;
  comentario explicando la guarda y el aislamiento bajo MVCC. Rama SQLite intacta.
- `compat/capture_test.go` — **nuevo** `TestCompileCaptureTriggersUsesTransactionLocalGUCOnPostgres`
  (guarda el SQL generado: PG usa la GUC y no toca `__compat_capture_state`;
  SQLite conserva la condición sobre la tabla). No modifica tests existentes.
- `compat/replicate_test.go` — **nuevo** `TestSQLiteSetCaptureSuppressedUpdatesStateTable`
  (regresión SQLite: la firma engine-aware no rompió el toggle 0↔1 de la tabla).
  No modifica tests existentes.
- `e2e/suppress_test.go` — **nuevo** (`package e2e_test`, `//go:build e2e`).

No se tocaron `e2e/system_test.go`, `cmd/`, `docs/`, ni tests preexistentes.

## Hecho — definición 1: e2e/suppress_test.go

Tres pruebas (`-run TestSuppress`):

- **(a) Aislamiento** `TestSuppressIsolationDoesNotLeakAcrossConnections`: la tx A
  arma la supresión con el SQL exacto del path productivo
  (`set_config('compat.suppress','1',true)`) y la mantiene abierta. Una conexión
  B (pool separado → backend distinto) inserta y commitea antes que A. El cambio
  de B **SÍ** queda journalizado (la supresión de A no se filtró); el propio
  cambio de A dentro de su tx armada **NO** se journaliza. Tras el commit de A,
  el journal sigue teniendo sólo la entrada de B.
  - Nota de fidelidad: `ApplyChanges` commitea internamente, así que la única
    forma de mantener una tx armada abierta es replicar el mecanismo (la misma
    sentencia `set_config`) directamente. Es el mismo SQL que ahora ejecuta el
    path público.
- **(b) Anti-eco real** `TestSuppressAntiEchoOnReplicatedWrites`: `ApplyChanges`
  contra destino PG con captura instalada no genera journal por las filas
  replicadas (0 entradas); una escritura manual posterior **SÍ** journaliza
  (control positivo: los triggers siguen vivos, sólo se suprime la replicación).
- **(c) Idempotencia** `TestSuppressReapplicationIsIdempotent`: re-aplicar el
  mismo stream sigue siendo no-op (0 eco, exactamente una fila).

Trade-off / decisión de test: la DB temporal es compartida por toda la suite e2e
y `__compat_applied_changes` dedup por `(source_engine, source_version,
sequence)`. Para evitar colisiones entre tests (y con los tests existentes que
usan `SQLite v3 seq=1/2`), cada test de supresión usa una **versión de source
única** (`{3,9001}` y `{3,9002}`). Documentado en el helper `suppressInsertChange`.

## Hecho — definición 2: `go test -tags=e2e ./e2e -run TestSuppress -v -count=1 -timeout 600s`

DSN (password enmascarado): `postgres://postgres:****@31.220.22.176:5434/postgres?sslmode=disable`

Salida real:

```
=== RUN   TestSuppressIsolationDoesNotLeakAcrossConnections
--- PASS: TestSuppressIsolationDoesNotLeakAcrossConnections (3.23s)
=== RUN   TestSuppressAntiEchoOnReplicatedWrites
--- PASS: TestSuppressAntiEchoOnReplicatedWrites (2.60s)
=== RUN   TestSuppressReapplicationIsIdempotent
--- PASS: TestSuppressReapplicationIsIdempotent (2.82s)
PASS
ok  	example.com/sqlite-postgres-compat/e2e	11.806s
```

## Hecho — definición 3: `go build ./... && go vet ./... && go test ./...`

Salida real:

```
=== BUILD ===
build-ok
=== VET ===
vet-ok
=== UNIT TESTS ===
?   	example.com/sqlite-postgres-compat/cmd/compat-audit	[no test files]
?   	example.com/sqlite-postgres-compat/cmd/compat-copy	[no test files]
ok  	example.com/sqlite-postgres-compat/compat	2.355s
```

(`go vet ./...` y `go vet -tags=e2e ./e2e` ambos limpios.) Tests preexistentes
no modificados.

## Hecho — definición 4: `go test -tags=e2e ./e2e -count=1 -timeout 600s` completo

Mismos resultados que la baseline + 3 PASS nuevos. Resumen real (top-level):

```
--- PASS: TestDatabaseDSNPreservesConnectionParameters (0.00s)
--- PASS: TestSuppressAntiEchoOnReplicatedWrites (2.50s)
--- PASS: TestSuppressIsolationDoesNotLeakAcrossConnections (2.88s)
--- PASS: TestSuppressReapplicationIsIdempotent (3.34s)
--- PASS: TestSystemAutomaticallyCapturesAndReplicatesBothDirections (3.67s)
--- PASS: TestSystemCanonicalFullTextReturnsEquivalentResults (1.02s)
--- PASS: TestSystemCanonicalRoutineExecutesEqually (1.09s)
--- PASS: TestSystemCanonicalTriggerProducesEquivalentEffects (1.05s)
--- PASS: TestSystemCanonicalViewProducesEquivalentResults (1.40s)
--- PASS: TestSystemCompatCopyCLIEndToEnd (2.61s)
--- PASS: TestSystemEnforcesCanonicalChecksAndIndexesEqually (1.30s)
--- PASS: TestSystemEnforcesForeignKeysEqually (1.32s)
--- PASS: TestSystemInspectsNativeSchemaObjectsWithoutMetadata (5.14s)
--- PASS: TestSystemPortableCoreRoundTripSQLitePostgresSQLite (0.97s)
--- PASS: TestSystemPreservesArbitraryPrecisionDecimals (0.88s)
--- PASS: TestSystemPreservesJSONUUIDAndTimestampSemantics (0.91s)
--- PASS: TestSystemReconstructsExactCanonicalSchemaFromBothEngines (0.81s)
--- PASS: TestSystemReplicatesIncrementalChangesBothDirections (3.00s)
--- FAIL: TestSystemClaimsExactCoverageForRequiredFeatureFamilies (0.00s)
FAIL	example.com/sqlite-postgres-compat/e2e	36.956s
```

**18 PASS (15 baseline + 3 nuevos) + 1 FAIL intencional**
(`TestSystemClaimsExactCoverageForRequiredFeatureFamilies`). Ningún FAIL nuevo;
el FAIL intencional documentado es el único que falla, igual que la baseline.

## Trade-offs

- **GUC de sesión no declarada:** `compat.suppress` no se registra con
  `CREATE VARIABLE`/`PGOPTIONS`; se setea ad-hoc con `set_config(...,true)`.
  `current_setting(name, true)` con `missing_ok=true` la lee sin error cuando no
  existe (devuelve NULL). No requiere DDL ni permisos especiales; funciona en PG
  12+ (la suite valida contra PG 17 real).
- **Tabla `__compat_capture_state` zombie en PG:** creada y sembrada pero sin
  uso funcional en la rama PG. Preferí esto a bifurcar `createAppliedChangesTable`
  por engine para minimizar el diff y el riesgo; el costo es una tabla vacía
  inerte en destinos PG. Aceptable y documentado.
- **No-op asimétrico en `setCaptureSuppressed(false)` para PG:** a diferencia de
  SQLite (que resetea la fila a 0), PG no hace nada en el "clear" porque el reset
  es automático al fin de la tx. La asimetría está documentada en el doc-comment.
- **Test de aislamiento replica el mecanismo en SQL directo** (no vía
  `ApplyChanges`) porque la API pública commitea y no permite mantener una tx
  armada abierta. Es la misma sentencia `set_config` que ejecuta el path
  productivo, así que ejercita el mismo mecanismo de supresión.

## Verificación de no-regresión

- La prueba e2e preexistente `TestSystemAutomaticallyCapturesAndReplicatesBothDirections`
  (que valida anti-eco bidireccional real con captura instalada en ambos engines)
  sigue en PASS — confirma que el cambio de mecanismo no rompió el anti-eco ni la
  captura normal.
- `TestSystemReplicatesIncrementalChangesBothDirections` sigue en PASS —
  idempotencia bidireccional preservada.