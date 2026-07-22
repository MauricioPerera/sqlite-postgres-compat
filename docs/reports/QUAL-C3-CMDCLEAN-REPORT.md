# QUAL-C3 — Limpieza de boilerplate en los 3 `main.go` + tests de `cliout`

**Estado:** LISTO · `go build ./...` ✅ · `go vet ./...` ✅ · `go test ./cmd/... -cover` ✅ (96.8%) · `go test ./...` ✅
**Archivos tocados (y solo estos):** `cmd/internal/cliout/cliout.go`, `cmd/internal/cliout/cliout_test.go` (nuevo), `cmd/compat-audit/main.go`, `cmd/compat-copy/main.go`, `cmd/compat-cutover/main.go`.
**No se tocó:** `compat/**`, `e2e/**`, `AGENTS.md`, `docs/**`.

---

## 1. Qué se deduplicó

Dos patrones repetidos se centralizaron en `cmd/internal/cliout` como helpers públicos (contrato de `cliout` ya documentado en AGENTS.md §8; no se agrega superficie nueva incompatible):

### 1.1 `cliout.Die(code, err)`
Centraliza `fmt.Fprintln(os.Stderr, err)` → `EmitErrorFrom(code, err)` (sobre stdout) → `os.Exit(ExitCode(code))`. Reemplaza:
- el patrón inline de `compat-audit` (`fmt.Fprintln(os.Stderr, err); os.Exit(cliout.EmitError(cliout.ErrX, err.Error()))`, 4 sitios), y
- las dos funciones locales `fail()` idénticas de `compat-copy` y `compat-cutover` (eliminadas).

`Die` usa `EmitErrorFrom` (no `EmitError` directo) igual que los `fail` originales; con `err != nil` el comportamiento es byte-idéntico al inline con `err.Error()`.

### 1.2 `cliout.ParseArgsStrict(knownFlags, args, wantN, hint, unexpectedMsg, countMsg)`
Centraliza el front-end de los 3 CLIs: `SplitArgs` → chequeo de flag desconocido → chequeo de cantidad posicional → en cada violación `fmt.Fprintln(os.Stderr, hint)` + `Die(ErrUsage, …)` + `os.Exit(2)`. Reemplaza el bloque de ~10 líneas repetido en cada `main`.

**Decisión clave (preservación observable):** los mensajes de uso (stderr) y los mensajes del envelope (stdout) **son provistos por cada CLI** como parámetros. `ParseArgsStrict` no los hardcodea. Así cada CLI conserva sus strings documentados byte-for-byte (incluida la inconsistencia preexistente de `compat-cutover`, donde el hint dice `uso:` en español pero el envelope del error de cantidad dice `usage:` en inglés, y el hint de cutover son dos líneas). El helper sólo elimina la *plomería* (`SplitArgs` + `fmt.Fprintln` + `os.Exit`), nunca el texto observable ni los exit codes.

---

## 2. Comportamiento observable — sin cambios

La definición de hecho exige idéntico: exit code, JSON, stream (stdout/stderr), orden de líneas. Verificado por demo manual (binarios construidos, no `git stash`):

### `compat-audit --bogus` (caso exigido)
```
EXIT=2
--- stdout ---
{"status":"error","code":"ERR_USAGE","message":"compat-audit: unexpected flag \"--bogus\""}
--- stderr ---
uso: compat-audit <contract.json>
compat-audit: unexpected flag "--bogus"
```
Coincide con AGENTS.md §8.1: `ERR_USAGE` → exit `2`; envelope de una línea en **stdout**; diagnóstico free-text en **stderr**. Orden stderr→stdout y la forma del envelope idénticos al flujo pre-cambio (que usaba `SplitArgs` + `Fprintln(stderr)` + `EmitError`).

### Verificación adicional de los 3 front-ends
- `compat-copy --bogus` → exit 2, envelope `ERR_USAGE` con `compat-copy: unexpected flag "--bogus"`, hint `uso: compat-copy <migration.json>`.
- `compat-copy` (sin args) → exit 2, envelope con `compat-copy requires exactly one migration JSON argument`.
- `compat-cutover --bogus` → exit 2, hint de **dos líneas** en stderr + envelope con `compat-cutover: unexpected flag "--bogus"`.
- `compat-cutover` (sin args) → exit 2, envelope con `usage: compat-cutover [--dry-run] <cutover.json>` (renderizado por `json.Marshal` como `<cutover.json>`, **idéntico al original** porque `Die`→`EmitErrorFrom`→`EmitError` usa el mismo `json.Marshal` que escapaba `<` ya antes).

### Path diverged de `compat-cutover` (intocable por diseño)
Las líneas que emiten el `cutoverReport` diverged con `code` embebido y el `os.Exit(1)` suelto (~199) **no se modificaron en su contrato**: sólo se reemplazó el `fail(cliout.ErrInternal, err)` de guarda de `EmitJSON` por `cliout.Die(...)`. El `EmitJSON(out)` + `os.Exit(1)` del path diverged queda exactamente como estaba: una sola línea JSON en stdout con el code embebido, sin envelope `{"status":"error",...}` separado (AGENTS.md §8.1, path "compat-cutover diverged").

### Doble-stderr en paths no-exactos/diverged de `compat-copy`/`compat-cutover`
Se conserva intencionalmente el comportamiento preexistente: esos paths emiten a stderr el err, luego el JSON estructurado (`[]Finding` o `VerificationReport`), y luego `Die` vuelve a imprimir el err a stderr (igual que el `fail` original) antes del envelope en stdout. No se “corrigió” el doble print: hacerlo sería un cambio observable que rompería la suite e2e.

---

## 3. Tests — `cmd/internal/cliout/cliout_test.go` (nuevo)

Cobertura **96.8%** de `cliout`. Cubre happy/edge/error de:

- `ExitCode`: `ErrUsage`→2, resto→1.
- `EmitError`: envelope parseable en una línea; newline final; **mensaje con `\n` embebido** queda escapado → stdout sigue siendo una sola línea (contrato one-line).
- `EmitErrorFrom`: nil → mensaje = el code; non-nil → `err.Error()`.
- `EmitJSON`: éxito (marshal + newline); fallo (`chan` no-marshallable → error).
- `SplitArgs`: flags conocidos + posicionales; **flag desconocido** (`--bogus`) → `ok=false` + `unexpected`; flags vacíos; flag sin valor **no consume el siguiente token** (`--dry-run --bogus` → `--bogus` sigue siendo desconocido).
- `DecodeFileStrict`: happy; **key desconocida** rechazada; archivo inexistente.
- `ResolveSchema`: inline solo; ref solo con **ruta relativa** resuelta junto al config; **schema+schema_ref juntos** (error "not both"); **ninguno** (error "exactly one"); ref ilegible (error con prefijo `schema_ref`).
- `ReplicationCode`: `*compat.ConflictError`→`ErrReplicationConflict`; otro→`ErrInternal`.

### Tests de los helpers nuevos (que llaman `os.Exit`)
`Die` y los paths de error de `ParseArgsStrict` llaman a `os.Exit`, así que no se pueden testear en-proceso. Se testean con **subprocess re-exec** (patrón estándar de `os/exec`): el test binario se re-ejecuta a sí mismo con un marker de env, el hijo invoca el helper, y el padre aserta exit code, stdout (envelope decodificado a `errorEnvelope` para comparar campos, no texto escapado) y stderr:
- `ParseArgsStrict` flag inesperado → exit 2 + envelope `ERR_USAGE` con mensaje `prog: unexpected flag "--bogus"` + hint en stderr.
- `ParseArgsStrict` cantidad incorrecta → exit 2 + envelope con mensaje de count + hint en stderr.
- `Die(ErrConfig,…)` → exit 1 + envelope `ERR_CONFIG` + err en stderr.
- `Die(ErrUsage,…)` → exit 2 + envelope `ERR_USAGE`.

---

## 4. Salida real de tests

```
$ go build ./... && go vet ./... && echo "BUILD+VET OK"
BUILD+VET OK

$ go test ./cmd/internal/cliout -count=1 -cover
ok  	example.com/sqlite-postgres-compat/cmd/internal/cliout	2.315s	coverage: 96.8% of statements

$ go test ./... -count=1
?   	example.com/sqlite-postgres-compat/cmd/compat-audit	[no test files]
?   	example.com/sqlite-postgres-compat/cmd/compat-copy	[no test files]
?   	example.com/sqlite-postgres-compat/cmd/compat-cutover	[no test files]
ok  	example.com/sqlite-postgres-compat/cmd/internal/cliout	2.221s
ok  	example.com/sqlite-postgres-compat/compat	2.280s
```
Los 3 `main` siguen con 0% de cobertura (no hay tests para `main` — fuera de alcance, y los `main` son ahora plumbing mínimo sobre `cliout` ya cubierto). Sin fallos causados por mis archivos.

---

## 5. Trade-offs

- **`Die` no retorna (llama `os.Exit`):** no se puede testear en-proceso; se cubre vía subprocess re-exec. Costo: los tests subprocess son ~0.7–1s más lentos y re-ejecutan el binario de test. Beneficio: el helper reproduce el comportamiento real (`os.Stdout`/`os.Stderr` reales + `os.Exit` real), más fiel que inyectar writers/exit mockables (lo cual habría agregado indirección de producción sólo para test).
- **Mensajes como parámetros en `ParseArgsStrict`:** firma de 6 args es más verbosa que un `Usage` struct tipado. Se eligió parámetros planos para no introducir un tipo nuevo en el paquete `cliout` y mantener el cambio mínimo; la verbosidad vive una vez en cada `main`, no en cada sitio de error.
- **No se unificaron los mensajes entre CLIs** (p.ej. `uso:` vs `usage:`, hints de 1 vs 2 líneas): unificarlos sería un cambio observable y rompería el contrato de AGENTS.md §8. Se conservan las inconsistencias preexistentes a propósito.
- **No se eliminó el doble-stderr** de los paths no-exactos/diverged: comportamiento preexistente, preservado por la regla “sin cambios observables”.
- **`compat-audit` perdió el import `fmt`** (ya no lo usa); `os` se retiene por `os.Args`. Importes más limpios, sin cambio de comportamiento.