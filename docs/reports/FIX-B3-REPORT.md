# FIX-B3 — Reporte de corrección

Tres hallazgos de auditoría corregidos. Archivos tocados (únicos): `compat/inspect.go`, `docs/TESTING.md`, `VALIDATION_REPORT.md`, `docs/USAGE.md`.

## Cambios

1. `compat/inspect.go`: eliminado el `defer objects.Close()` redundante (línea 457). Se mantiene el `objects.Close()` explícito con manejo de error (patrón del resto del archivo). Cero cambio de comportamiento: el cierre sigue ocurriendo exactamente una vez por camino.
2. `docs/TESTING.md:58`: removido "conflictos" de la lista de lo que el e2e valida "extremo a extremo". La detección de conflictos no tiene test e2e (`grep -ci conflict e2e/system_test.go` = 0); su cobertura es unitaria y ya figura en `docs/TESTING.md:10` ("snapshots, journals, conflictos y runtime común").
3. `VALIDATION_REPORT.md:43`: aclarado que la detección de conflictos es cobertura de la suite unitaria, no de la batería E2E.
4. `docs/USAGE.md`: documentado el exit code 2 (argumentos ≠ 1) para ambos CLIs (`compat-audit` y `compat-copy`), además del exit 1 ya documentado. Coincide con `cmd/compat-audit/main.go:14` y `cmd/compat-copy/main.go:24` (`os.Exit(2)`).

## Definición de hecho — salida real

### 1. `grep -n "conflicto" docs/TESTING.md VALIDATION_REPORT.md`

```
docs/TESTING.md:10:Estas pruebas deben terminar correctamente. Cubren AST, compilación DDL, parsers de catálogo, snapshots, journals, conflictos y runtime común.
VALIDATION_REPORT.md:43:- Detección de conflictos antes de sobrescribir cambios divergentes (cubierta por la suite unitaria, no por la batería E2E).
```

La mención en `docs/TESTING.md:10` corresponde a la suite unitaria (sección "Pruebas unitarias y análisis estático"). La mención en `VALIDATION_REPORT.md:43` ahora aclara explícitamente que es cobertura unitaria, no de la batería E2E. La lista e2e (`docs/TESTING.md:58`) ya no menciona conflictos.

### 2. `grep -n "código 2\|code 2\|exit 2\|salida 2" docs/USAGE.md`

```
9:La salida es JSON. El proceso termina con código `1` si cualquier capacidad requerida no es exacta, y con código de salida 2 si el número de argumentos no es exactamente uno.
52:El flujo audita las capacidades inferidas, exporta el origen, importa el destino y vuelve a exportarlo para verificar su hash canónico. El destino debe estar vacío para los objetos descritos. El proceso termina con código `1` ante cualquier error o falta de equivalencia exacta, y con código de salida 2 si el número de argumentos no es exactamente uno.
```

### 3. `grep -n "objects.Close" compat/inspect.go`

```
470:	if err := objects.Close(); err != nil {
```

Queda un único cierre de `objects` (el explícito con manejo de error). El `defer` redundante fue eliminado.

### 4. `go build ./... && go vet ./... && go test ./...`

```
?   	example.com/sqlite-postgres-compat/cmd/compat-audit	[no test files]
?   	example.com/sqlite-postgres-compat/cmd/compat-copy	[no test files]
ok  	example.com/sqlite-postgres-compat/compat	(cached)
EXIT=0
```

Todo verde. `go build ./...`, `go vet ./...` y `go test ./...` finalizan con código 0.

## Nota sobre la ejecución

La cache de build de Go por defecto (`GOCACHE` del usuario) estaba corrupta ("too many levels of symbolic links" en `go-build/`), lo que producía errores espurios de `go vet` (`undefined: stripTrailingNot`) y de parseo en `spec_test.go`. La corrección del Hallazgo 1 en `inspect.go` es aislada y no toca esos archivos. Se ejecutó la batería con una cache limpia y temporal (`GOCACHE=/tmp/gocache-b3`) para confirmar el estado real: build, vet y test en verde.

El árbol de trabajo contenía además modificaciones concurrentes de otros devs en `compat/spec.go`, `compat/spec_test.go`, `compat/replicate.go` y `compat/replicate_test.go` (no tocadas por esta tarea, conforme a la restricción de ARCHIVOS). Esas modificaciones compilan y pasan pruebas correctamente una vez descartado el ruido de la cache corrupta.