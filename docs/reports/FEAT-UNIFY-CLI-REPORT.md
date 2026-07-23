# FEAT-UNIFY-CLI — Unificación de los tres CLIs en un binario `compat`

Fecha: 2026-07-22
Alcance: `cmd/**`, `e2e/*.go`, las 6 docs vivas y `contracts/migration.contract.example.md`. No se tocó `compat/**` ni `docs/reports/**`.

## Resumen

Se reemplazaron los tres binarios separados —`cmd/compat-audit`, `cmd/compat-copy`, `cmd/compat-cutover`— por un único binario `cmd/compat` con subcomandos:

```
compat audit <contract.json>
compat copy <migration.json>
compat cutover [--dry-run] <cutover.json>
```

Cada subcomando conserva **exactamente** el comportamiento observable de su binario original (mismos envelopes JSON, mismos exit codes —uso=2, error=1, ok=0—, mismos streams stdout/stderr, mismo orden de líneas, mismos findings/reports/dry-run plan) con **una única excepción deliberada**: los prefijos de mensajes cambiaron de `compat-audit:` / `compat-copy:` / `compat-cutover:` a `compat audit:` / `compat copy:` / `compat cutover:`.

## Arquitectura

- `cmd/compat/main.go` — dispatcher. Lee `os.Args[1]`; si es `audit`/`copy`/`cutover` despacha a `runAudit`/`runCopy`/`runCutover` con `os.Args[1:]`. Cualquier otro token (incluido ausencia de subcomando, nombre desconocido, o `--help`-ish) → `usageFail()`: hint de uso a stderr + envelope `ERR_USAGE` a stdout, exit 2.
- `cmd/compat/audit.go` — `runAudit` (antes `cmd/compat-audit/main.go`).
- `cmd/compat/copy.go` — `runCopy` + `migrationConfig` (antes `cmd/compat-copy/main.go`).
- `cmd/compat/cutover.go` — `runCutover` + `runDryRun` + tipos/helpers (`cutoverConfig`, `cutoverOptions`, `cutoverReport`, `planReport`, `planTable`, `cutoverPhases`, `countRows`, `tableExists`, `quoteIdent`, `drainChanges`, `logf`) (antes `cmd/compat-cutover/main.go`).

Los tres comparten `cmd/internal/cliout` sin cambios de lógica (solo un comentario de ejemplo actualizado). `cliout` no gana lógica nueva, por lo que no se agregaron tests de dispatch a `cliout_test.go` (la cobertura del dispatch vive en e2e y en la demo manual).

## Cambios de prefijo (la única diferencia observable)

| Antes | Después | Dónde |
|---|---|---|
| `uso: compat-audit <contract.json>` | `uso: compat audit <contract.json>` | hint de audit |
| `compat-audit: unexpected flag %q` | `compat audit: unexpected flag %q` | envelope unexpected-flag de audit |
| `compat-audit requires exactly one contract JSON argument` | `compat audit requires exactly one contract JSON argument` | envelope wrong-count de audit |
| `uso: compat-copy <migration.json>` | `uso: compat copy <migration.json>` | hint de copy |
| `compat-copy: unexpected flag %q` | `compat copy: unexpected flag %q` | envelope unexpected-flag de copy |
| `compat-copy requires exactly one migration JSON argument` | `compat copy requires exactly one migration JSON argument` | envelope wrong-count de copy |
| `uso: compat-cutover [--dry-run] <cutover.json>\n...` | `uso: compat cutover [--dry-run] <cutover.json>\n...` | hint de cutover |
| `compat-cutover: unexpected flag %q` | `compat cutover: unexpected flag %q` | envelope unexpected-flag de cutover |
| `usage: compat-cutover [--dry-run] <cutover.json>` | `usage: compat cutover [--dry-run] <cutover.json>` | envelope wrong-count de cutover |
| `compat-cutover: ` (progreso stderr) | `compat cutover: ` (progreso stderr) | `logf` de cutover |

Nota: los `countMsg` originales de audit/copy **no** llevaban dos puntos (`compat-audit requires...`). Se preservó esa estructura exacta pasando el guion a espacio sin agregar dos puntos (`compat audit requires...`), manteniendo la inconsistencia original en lugar de introducir una nueva.

## e2e: asserts cambiados

**No se cambió qué se asserta.** Los e2e nunca afirmaron sobre el texto de mensajes ni prefijos (solo sobre `code`, exit codes y estructura JSON), por lo que ningún assert de contenido cambió. Solo cambió **cómo se invoca** el CLI y los strings no-assertados (mensajes de `t.Fatalf` y comentarios). Detalle exacto:

### `e2e/cutover_test.go`

- `TestCutoverCLIEndToEnd`: `exec.Command("go","run","./cmd/compat-cutover", configPath)` → `exec.Command("go","run","./cmd/compat","cutover", configPath)`. Strings de `Fatalf` `"compat-cutover failed"` → `"compat cutover failed"`, `"invalid compat-cutover output"` → `"invalid compat cutover output"`. Asserts intactos: `status=ready`, digests iguales no-vacíos, 2 filas migradas.
- `TestDryRunCLISuccessPlan`: `runCLI(t,"./cmd/compat-cutover","--dry-run",configPath)` → `runCLI(t,"./cmd/compat","cutover","--dry-run",configPath)`. Asserts intactos: plan JSON, `source_tables=[{name,rows=2}]`, `destination_has_tables=false`, `phases` exactas, no escritura en origen/destino.
- `TestDryRunCLIInvalidConfigError`: misma invocación. Assert intacto: exit 1 + `ERR_CONFIG`.
- `TestDryRunCLIUnreachableDestinationError`: misma invocación. Assert intacto: exit 1 + `ERR_CONNECT_DESTINATION`, origen intacto.
- `TestAuditCLIErrorCodeOnNotExact`: `runCLI(t,"./cmd/compat-audit",contractPath)` → `runCLI(t,"./cmd/compat","audit",contractPath)`. Asserts intactos: findings array + `ERR_AUDIT_NOT_EXACT`, exit 1.
- `TestCLIRejectsUnknownFlagAsUsage`: reestructurado — el `pkg` se unificó a `"cmd/compat"` y los `args` ahora prefijan el subcomando (`["cutover","--bogus","x.json"]`, `["copy",...]`, `["audit",...]`). Asserts intactos: exit 2 + `ERR_USAGE` para cada subcomando.
- `TestCLIDispatchUsage` **(NUEVO, aditivo)**: cubre el contrato de dispatch del binario unificado — sin subcomando, subcomando desconocido (`bogus`) y flag `--help`-ish → exit 2 + `ERR_USAGE` + stderr que lista `subcomandos`. No reemplaza ningún assert existente.
- `TestCutoverRejectsUnknownConfigKey`, `TestCutoverRejectsSchemaAndSchemaRef`, `TestCutoverRejectsMissingSchemaAndSchemaRef`, `TestCutoverWithSchemaRefSucceeds`: `runBuiltCLI(t,"cmd/compat-cutover",configPath)` → `runBuiltCLI(t,"cmd/compat","cutover",configPath)`. Asserts intactos (exit 1 `ERR_CONFIG` para los tres primeros; `status=ready` + digests + 2 filas para el último).

### `e2e/system_test.go`

- `TestSystemCompatCopyCLIEndToEnd`: `exec.Command("go","run","./cmd/compat-copy",configPath)` → `exec.Command("go","run","./cmd/compat","copy",configPath)`. Strings de `Fatalf` actualizados (`compat-copy failed` → `compat copy failed`, etc.). Asserts intactos: `VerificationReport.Equivalent == true`, 2 filas migradas.

### Helpers e2e

- `builtCLI`/`runBuiltCLI`/`runCLI`: sin cambios de código — solo cambiaron los call sites (pkg `"cmd/compat"` y subcomando como primer arg). El cacheo por `pkg` ahora colapsa a un único `"cmd/compat"`.

## Docs vivas actualizadas

- `AGENTS.md` §8 (renombrado `## 8. CLIs` → `## 8. CLI`): intro reescrita al binario unificado + dispatch; sección `--help`-ish/unknown-subcommand; todas las menciones `compat-X` → `compat X` en headers de subsección (`### compat audit <contract.json>`, etc.), tabla de códigos y §9 (flujo de migración). Queda una mención justificada en la línea de cabecera que documenta la unificación misma (viejos nombres → nuevos).
- `docs/USAGE.md`: todas las invocaciones `go run ./cmd/compat-X` → `go run ./cmd/compat X`; referencias en prosa `compat-cutover`/`compat-copy` → `compat cutover`/`compat copy`; header `### Códigos de error tipados (los 3 CLIs)` → `(los 3 subcomandos)`.
- `docs/TESTING.md`: `CLI real compat-copy` → `CLI real compat copy`; `CLI compat-cutover` → `CLI compat cutover`.
- `README.md`: quickstart, tabla de comandos (renombrada `## Los CLIs` → `## El CLI`), descripción de cutover, árbol del repo (`cmd/ # CLIs: compat-audit...` → `# CLI unificado: compat`).
- `docs/OPERATIONS.md`: `compat-copy` → `compat copy`.
- `contracts/migration.contract.example.md`: preconditions, risks/rollback, comandos `go run`, prefijos de stderr esperados (`compat-cutover: audit: ...` → `compat cutover: audit: ...`), sección Verify, y failure exit-2 extendido a "wrong argument count or an unexpected flag".

## Scripts

- `scripts/test-system.ps1` y `scripts/check.ps1`: **sin cambios**. Solo ejecutan `go test`/`go vet`; no invocan los binarios viejos por nombre.

## Definición de hecho (verificado)

```
$ go build ./...            # BUILD:0
$ go vet ./...              # VET:0
$ go vet -tags=e2e ./e2e    # E2EVET:0  (compila; no se corre la suite, necesita PostgreSQL)
$ go test ./... -count=1   # TEST:0  (cliout + compat verdes; cmd/compat sin tests)
$ pwsh scripts/check.ps1   # gofmt: OK / go vet: OK / go test: OK  (CHECK_EXIT:0)
```

Demo manual con binario buildeado (`go build -o`, no `go run` — `go run` colapsa exit codes en Windows):

1. `compat` (sin args) → exit 2 + envelope `ERR_USAGE` + uso en stderr listando subcomandos.
2. `compat bogus` → exit 2 + `ERR_USAGE`.
3. `compat audit --bogus x` → exit 2 + `ERR_USAGE` con prefijo `compat audit: unexpected flag "--bogus"`.
4. `compat audit examples/contract.example.json` → exit 0 con el array de findings de hoy (`tables`/`canonical_foreign_keys`/`transactions`, todos `exact`).
5. `compat cutover --dry-run <config mínimo en tempdir>` (destino inalcanzable `127.0.0.1:1`) → línea de progreso `compat cutover: audit: exact coverage for 3 required features` en stderr y envelope `ERR_CONNECT_DESTINATION` exit 1 — mismo comportamiento que hoy (audita, conecta origen, falla al conectar destino).

Grep final `grep -rn "compat-audit\|compat-copy\|compat-cutover" AGENTS.md docs/USAGE.md docs/TESTING.md README.md docs/OPERATIONS.md contracts/ e2e/ cmd/ scripts/` → únicamente hits **justificados** que documentan la unificación:
- `AGENTS.md:178` — cabecera que explica que los subcomandos antes eran tres binarios.
- `cmd/compat/{main,audit,copy,cutover}.go` — comentarios que documentan el cambio de prefijo.
- `cmd/internal/cliout/cliout.go` ya limpiado (queda referenciando `audit/copy subcommands`, no los nombres viejos).

## Trade-offs

1. **Un solo `package main` con archivos por subcomando vs. subpaquetes.** Se eligió un solo paquete con `runAudit`/`runCopy`/`runCutover` y los tipos de cada subcomando en su archivo. Ventaja: mínima, sin nuevas rutas de import, preserva el comportamiento exacto sin capa extra. Costo: todos los tipos (`migrationConfig`, `cutoverConfig`, etc.) comparten paquete; no hay colisión hoy, pero hay menos aislamiento que con subpaquetes. Aceptable porque los tres flujos no comparten tipos entre sí y el namespace está limpio.
2. **Dispatch en `main.go` (no en `cliout`).** `cliout` no gana lógica nueva — el dispatch usa el `cliout.Die(ErrUsage, ...)` existente. Costo: la lógica de dispatch vive en `package main`, que no es unit-testeable in-process. Se cubre con `e2e/TestCLIDispatchUsage` (nueva) y la demo manual. Esto cumple la regla "agregá tests del dispatch si cliout gana lógica nueva": no la ganó, así que no se agregaron tests a `cliout_test.go`.
3. **Preservar la inconsistencia original de los `countMsg`.** Los `countMsg` de audit/copy no llevaban dos puntos en el original (`compat-audit requires...`); se mapeó a `compat audit requires...` (guion→espacio, sin dos puntos) en vez de "corregirlo" a `compat audit: requires...`. Ventaja: cambio mínimo, sin introducir una nueva inconsistencia. Costo: la forma con dos puntos (`compat audit: unexpected flag`) y sin dos (`compat audit requires`) conviven, igual que en el original.
4. **Menciones justificadas restantes.** Se conservan las referencias a los nombres viejos en la cabecera de `AGENTS.md` y en los comentarios de `cmd/compat/*.go` como documentación del cambio. Es información útil para quien migra scripts que aún invoque los viejos binarios, y es el hit justificado que permite el grep de cero.

## Abortar

No se abortó: preservar el comportamiento observable exacto fue posible en todos los paths. El único cambio observable es el prefijo, documentado arriba.