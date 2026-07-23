# FIX-A5-BAJA — Reparación de los 4 hallazgos BAJA de AUDIT5 (§§4.3–4.6)

Fecha: 2026-07-22
Ámbito: binario unificado `compat` (subcomandos `audit`/`copy`/`cutover`), dispatch, parseo de flags y patrón stderr de los paths not-exact/diverged.
Entorno: Windows 11, Go, repo `sqlite-postgres-compat` en `main`. Árbol limpio al inicio.
PostgreSQL: **real**, PostgreSQL 17.10 (Alpine) vía `COMPAT_POSTGRES_DSN` (password **enmascarado como `***`** en todo este reporte). DSN usado: `postgres://postgres:***@31.220.22.176:5434/postgres?sslmode=disable`.
Binario de demo: `go build -o /tmp/fixb5demo/compat.exe ./cmd/compat` (nunca `go run` para asserts de exit code — colapsa códigos en Windows). Tempfiles y tablas efímeras `fixb5_*` borrados al terminar (ver §6).

## Resumen

Los 4 hallazgos BAJA de AUDIT5 quedan reparados. Código, docs y e2e se movieron juntos: ningún claim documentado quedó sin su reflejo en el binario.

| AUDIT5 | Hallazgo | Fix | Archivos |
|---|---|---|---|
| §4.3 | Doble `--dry-run` aceptado en silencio | flag reconocido repetido → `ERR_USAGE` exit 2 con `duplicate flag` | `cliout.go` (`SplitArgs`/`ParseArgsStrict`), `audit.go`/`copy.go`/`cutover.go`, `cliout_test.go` |
| §4.4 | `--` tratado como flag desconocido | `--` = separador end-of-flags estándar (subcomando y dispatch) | `cliout.go` (`SplitArgs`), `main.go`, `cliout_test.go` |
| §4.5 | flag antes del subcomando → mensaje genérico | mensaje orientador `flags must follow the subcommand` (exit 2) | `cliout.go` (`DispatchUsageMessage`), `main.go`, `cliout_test.go` |
| §4.6 | `err` impreso DOS veces en stderr (copy/cutover not-exact/diverged) | `err` aparece UNA vez en stderr; payload (`[]Finding`/`VerificationReport`) y envelope conservados | `copy.go`, `cutover.go`, e2e |

## Detalle por fix

### §4.3 — Flag duplicado → `ERR_USAGE` exit 2

`SplitArgs` ahora detecta un flag reconocido repetido y retorna `duplicate` (el token ofensor) con `ok=false`. `ParseArgsStrict` toma un nuevo parámetro `duplicateMsg` y emite `compat <sub>: duplicate flag %q` vía `Die(ErrUsage, …)` (exit 2). El mensaje orientativo guía al usuario; antes el `map[string]bool` deduplicaba en silencio.

- Unit: `TestSplitArgs/duplicate recognized flag rejected` y `TestParseArgsStrict_DuplicateFlagExits2` (exit 2, envelope `duplicate flag "--dry-run"`, hint en stderr).
- Demo real: `compat cutover --dry-run --dry-run <cfg>` → exit 2, stdout `{"status":"error","code":"ERR_USAGE","message":"compat cutover: duplicate flag \"--dry-run\""}`.

### §4.4 — Soporte de separador `--`

`SplitArgs`: al ver `--`, se activa `endOfFlags`; todo token posterior es posicional aunque empiece con `-`, y `--` se descarta. A nivel dispatch (`main.go`), un `--` inicial se consume y el token siguiente es el subcomando (`compat -- audit x.json` despacha a `audit`). Decisión tomada (lo más coherente con la semántica de `--` en el subcomando): `--` a nivel dispatch = "lo que sigue es el subcomando, no un flag".

- Unit: `TestSplitArgs/dash-dash ends flags`, `dash-dash itself is discarded`, `dash-dash with no following token`; `TestParseArgsStrict_Happy` (caso `--raro.json` posicional).
- Demo real: `compat cutover --dry-run -- <cfg>` → exit 0 con `plan` JSON (audit exact, 2 filas, `destination_has_tables:false`); `compat audit -- --raro.json` → exit 1 `ERR_CONFIG` `open --raro.json: …` (trata `--raro.json` como ruta, no como flag inesperado).

### §4.5 — Mensaje orientador para flag antes del subcomando

Nuevo `cliout.DispatchUsageMessage(firstArg)`: si el primer token empieza con `-` y **no** es help-ish (`--help`/`-h`/`-help`), retorna `compat: flags must follow the subcommand (e.g. compat cutover --dry-run <config>)`; si no, el genérico `compat: missing or unknown subcommand`. `main.go` lo usa en `usageFail(firstArg)`. Sigue siendo `ERR_USAGE` exit 2.

- Unit: `TestDispatchUsageMessage` (6 casos: leading flag, `-x`, unknown subcommand, vacío, `--help`, `-h`).
- Demo real: `compat --dry-run cutover x` → exit 2, stdout `{"…","code":"ERR_USAGE","message":"compat: flags must follow the subcommand (e.g. compat cutover --dry-run <config>)"}`, stderr = hint de subcomandos + el mensaje orientador.

### §4.6 — `err` una sola vez en stderr

En `copy.go` (paths not-exact y diverged) y `cutover.go` (path not-exact) se eliminó el `fmt.Fprintln(os.Stderr, err)` explícito previo al `Die`. El `err` queda impreso una sola vez por `cliout.Die` (que hace `Fprintln(os.Stderr, err)` + envelope a stdout). El payload estructurado (`[]Finding` en `ERR_AUDIT_NOT_EXACT`, `VerificationReport` en `ERR_VERIFY_DIVERGED`) se conserva en stderr; el envelope tipado se conserva en stdout. Exit codes sin cambio (1).

- E2e (nuevos, top-level):
  - `TestCopyCLINotExactStderrOnce` — sin PG (falla en audit): exit 1, envelope `ERR_AUDIT_NOT_EXACT`, stderr con `[]Finding` una vez y la línea de error una vez.
  - `TestCopyCLIDivergedStderrOnce` — **PG real**: divergencia genuina y reproducible (columna `NUMERIC(38,18)` rellena `0.10` a escala 18 → `0.100000000000000000`, digest destino ≠ origen). exit 1, envelope `ERR_VERIFY_DIVERGED`, stderr con `VerificationReport` una vez (`"equivalent":false` ×1) y `snapshot mismatch: source` ×1.
  - `TestCutoverCLINotExactStderrOnce` — sin PG: mismo patrón stderr-una-vez para cutover.
- Demo real (diverged, PG): ver §5. La línea `snapshot mismatch: source …` aparece **una vez** en stderr (antes aparecía dos).

> Nota sobre el fixture diverged: la divergencia usada es real y reproducible contra el PG dado, **no** un bug introducido ni un claim sobre la corrección de la migración decimal. Es el vehículo para ejercitar el contrato stderr del path `ERR_VERIFY_DIVERGED`. La escala de `NUMERIC(38,18)` rellena `0.10` a 18 decimales; el texto canónico del destino difiere del `TEXT` del origen → digests distintos → `ERR_VERIFY_DIVERGED` es la respuesta correcta y honesta del binario. La corrección (o no) de ese relleno de escala es una pregunta de diseño fuera del alcance de estos 4 fixes (no se tocó `compat/`).

## Trade-offs

- **`duplicate flag` es estricto para flags reconocidos.** Cualquier flag booleano repetido ahora detiene la ejecución. Esto cambia una leniency previa (deduplicación silenciosa). Un agente/script que pasara `--dry-run` dos veces por error ahora recibe exit 2 en vez de un plan; es el comportamiento pedido (§4.3). No afecta flags desconocidos (siguen siendo `unexpected flag`).
- **`--` consume el primer `--` nada más.** `compat cutover --dry-run -- --raro.json` deja a `--raro.json` como posicional (vía un único `--`). `compat -- -- audit` (dos `--` antes del sub) cae al default con mensaje orientador (edge case inocuo, exit 2). Convención estándar de un solo `--`.
- **`--` a nivel dispatch despacha al siguiente token como subcomando.** Es lo coherente con la semántica de subcomando. Alternativa considerada: tratar `--` como subcomando desconocido → `ERR_USAGE`; se descartó porque rompería la simetría con el `--` del subcomando y sorprendería a usuarios de shell.
- **Mensaje orientador sólo para tokens que empiezan con `-` y no son help-ish.** `--help`/`-h`/`-help` siguen dando el mensaje genérico (no hay help real implementado; era `ERR_USAGE` antes y lo sigue siendo). Un flag raro como `--version` recibiría el orientador (razonable: es un flag antes del subcomando).
- **stderr-una-vez cambia el orden relativo en stderr.** Antes: `err`, `payload`, `err(Die)`. Ahora: `payload`, `err(Die)`. El payload (JSON machine-readable) queda primero y la línea de error al final. stdout (envelope) es invariante. Ningún e2e previo dependía del orden stderr err×2; los nuevos e2e assert "una vez".
- **Fixture diverged depende del relleno de escala de `NUMERIC`.** Si una ronda futura normalizara `0.10`↔`0.100…000` (cambiando `compat/`), la divergencia desaparecería y `TestCopyCLIDivergedStderrOnce` rompería — lo que sería la señal correcta de que ese contrato cambió. No se introduce dependencia frágil más allá del comportamiento real actual del PG dado.

## Asserts / claims cambiados (docs y e2e)

### Docs

- **AGENTS.md §8 (dispatch)**: agregado el mensaje orientador para flag antes del subcomando y la semántica de `--` a nivel dispatch (`compat -- audit x.json` → `audit`).
- **AGENTS.md §8 (flags)**: agregado flag duplicado → `ERR_USAGE` con `duplicate flag`, y `--` como separador end-of-flags (todo lo posterior es posicional aunque empiece con `-`). Lista de exit-2 ampliada a "unexpected or duplicate flag".
- **AGENTS.md §8.1**: el bullet de `compat copy` not-exact/diverged ahora dice "la línea de error se imprime a stderr **exactly once**"; se aclara que cutover not-exact sigue el mismo patrón.
- **AGENTS.md §8.1 tabla `ERR_USAGE`**: "wrong argument count, an unexpected flag, a duplicated recognized flag, or a missing/unknown subcommand".
- **AGENTS.md `compat copy` / `compat cutover`**: exit-2 reescrito a "wrong argument count, an unexpected flag, or a duplicated recognized flag"; not-exact/diverged marcan "error line … exactly once (not duplicated)".
- **docs/USAGE.md**: §copy y §cutover actualizados (exit-2 incluye flag inesperado y duplicado; stderr-una-vez); §códigos — bullet copy y fila `ERR_USAGE`/`ERR_AUDIT_NOT_EXACT`/`ERR_VERIFY_DIVERGED`; nueva sección **"Convenciones de parseo de flags"** (flag antes del subcomando, duplicado, `--`).
- **README.md / docs/TESTING.md**: conteo e2e **36 = 35+1 → 39 = 38+1** (3 tests top-level nuevos).

### E2e (nuevos, top-level; todos verdes contra PG real)

| Test | Path cubierto | PG |
|---|---|---|
| `TestCopyCLINotExactStderrOnce` | copy `ERR_AUDIT_NOT_EXACT` stderr-una-vez | no |
| `TestCopyCLIDivergedStderrOnce` | copy `ERR_VERIFY_DIVERGED` stderr-una-vez | **sí** |
| `TestCutoverCLINotExactStderrOnce` | cutover `ERR_AUDIT_NOT_EXACT` stderr-una-vez | no |

### Unit (cliout)

`TestSplitArgs` (casos nuevos: duplicate, `--` ×3), `TestParseArgsStrict_Happy` (caso `--`), `TestParseArgsStrict_DuplicateFlagExits2`, `TestDispatchUsageMessage`. Actualizados los calls existentes a la nueva firma de `SplitArgs`/`ParseArgsStrict`.

## Verificación

- `pwsh scripts/check.ps1`: gofmt OK / go vet OK / `go test ./...` OK (exit 0).
- `go vet -tags=e2e ./e2e`: OK (exit 0).
- `go test -tags=e2e ./e2e -count=1 -timeout 180s` contra PG real: **38 PASS + 1 FAIL intencional** (`TestSystemClaimsExactCoverageForRequiredFeatureFamilies`, baseline conocido — las familias genéricas `foreign_keys`/`check_constraints`/`indexes`/`views`/`triggers`/`stored_routines`/`full_text` son `unknown` por contrato §9). Los 3 tests nuevos pasan; `TestCLIDispatchUsage`/`TestCLIRejectsUnknownFlagAsUsage`/`TestCLIUsageCountMessageConsistent` siguen en verde (sin regresión).
- Demos con binario buildeado (§5): las 6 salidas reales arriba, exit codes reales, password enmascarado. El diverged corre contra el PG dado y muestra `err` una vez + `VerificationReport`.

## Limpieza

- Tablas efímeras `fixb5_div_demo`, `fixb5_probe`, `fixb5_demo_items` dropeadas del PG real (verificado `fixb5_* tables remaining: 0`).
- Binario de demo `/tmp/fixb5demo/compat.exe` y dir `/tmp/fixb5demo` borrados; helpers Go temporales (`fixb5demo_setup*.go`, `fixb5demo_cleanup.go`, `fixb5demo_verify.go`) y sondas (`/tmp/probe*`) borrados.
- La suite e2e usa su DB efímera `compat_e2e_<ts>` (creada y dropeada por `TestMain`); sin bases `fixb5_*` persistentes.
- `git status`: sólo los archivos dentro del set permitido (cmd/compat/**, cmd/internal/cliout/**, e2e/*.go, AGENTS.md, docs/USAGE.md, docs/TESTING.md, README.md) + este reporte. No se tocó `compat/**` ni `docs/reports/**` existentes. No se hizo `git add`/`commit`.

## Archivos modificados

- `cmd/internal/cliout/cliout.go` — `SplitArgs` (+`--`/duplicate), `ParseArgsStrict` (+`duplicateMsg`), `DispatchUsageMessage`/`isHelpIsh`.
- `cmd/internal/cliout/cliout_test.go` — tests unitarios 1–3.
- `cmd/compat/main.go` — dispatch `--` + mensaje orientador.
- `cmd/compat/audit.go`, `cmd/compat/copy.go`, `cmd/compat/cutover.go` — `duplicateMsg` y fix §4.6 (err una vez).
- `e2e/system_test.go` — `TestCopyCLINotExactStderrOnce`, `TestCopyCLIDivergedStderrOnce`.
- `e2e/cutover_test.go` — `TestCutoverCLINotExactStderrOnce`.
- `AGENTS.md`, `docs/USAGE.md`, `docs/TESTING.md`, `README.md` — contrato y conteo.