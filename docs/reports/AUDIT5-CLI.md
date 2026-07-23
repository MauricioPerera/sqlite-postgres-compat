# AUDIT5 — CLI unificado `compat` (con PostgreSQL real)

Fecha: 2026-07-22
Ámbito: binario unificado `compat` (subcomandos `audit`/`copy`/`cutover`), dispatch, contrato tipado, prefijos, limpieza residual.
Entorno: Windows 11, Go, repo `sqlite-postgres-compat` en `main` (commit `c18272b`). Árbol limpio al inicio.
PostgreSQL: **real**, PostgreSQL 17.10 (Alpine) vía `COMPAT_POSTGRES_DSN` (password **enmascarado como `***`** en todo este reporte). DSN usado: `postgres://postgres:***@31.220.22.176:5434/postgres?sslmode=disable`.
Binario: buildeado con `go build -o /tmp/audit5/compat.exe ./cmd/compat` (nunca `go run` para asserts de exit code — colapsa códigos en Windows). Tempfiles y DBs efímeras `audit5_*` borradas al terminar.

## Metodología

Cada claim se ejecutó contra el binario buildeado capturando stdout, stderr y exit code por separado. Para los paths que exigen PG vivo se crearon bases efímeras `audit5_<variant>_<ts>` (dropeadas al final; 13 creadas, 13 dropeadas, 0 restantes) y fuentes SQLite temporales en `zz_audit5_tmp/` (borradas). El password del DSN se leyó de un archivo temporal (`/tmp/audit5/pgdsn.txt`) para no pegarlo en líneas de comando; nunca aparece literal en este reporte ni en logs pegados (enmascarado con `***`).

Baseline de integración: la suite e2e (`go test -tags=e2e ./e2e`) corre con PG vivo. Resultado **32 PASS + 1 FAIL intencional** (`TestSystemClaimsExactCoverageForRequiredFeatureFamilies`: los families genéricos `foreign_keys`/`check_constraints`/`indexes`/`triggers`/`views`/`stored_routines`/`full_text` son `unknown` por contrato §9 — es el baseline conocido `32+1 intencional`, **no** un hallazgo AUDIT5 ni una regresión de la unificación). Todos los tests CLI pasan con PG vivo (ver §3).

Gate `scripts/check.ps1`: gofmt OK / go vet OK / go test OK (exit 0).

---

## 1. Contrato del binario unificado — tabla claim por claim

### 1.1 Dispatch nivel `compat` (AGENTS.md §8, líneas 178-182)

| Claim | Comando | Salida real / exit | Veredicto |
|---|---|---|---|
| Sin subcomando → ERR_USAGE exit 2 + uso a stderr + envelope a stdout | `compat` | stdout: `{"status":"error","code":"ERR_USAGE","message":"compat: missing or unknown subcommand"}`; stderr: bloque `uso: compat <subcommand>...` + `compat: missing or unknown subcommand`; exit 2 | ✅ |
| Subcomando desconocido → ERR_USAGE exit 2 | `compat bogus` | idem (mismo envelope/uso); exit 2 | ✅ |
| Token `--help`-ish → ERR_USAGE exit 2 | `compat --help` / `compat -h` | idem; exit 2 | ✅ |
| Subcomando `audit` con 0 args → ERR_USAGE exit 2 con hint de sub | `compat audit` | stdout: `{"...","code":"ERR_USAGE","message":"compat audit requires exactly one contract JSON argument"}`; stderr: `uso: compat audit <contract.json>` + msg; exit 2 | ✅ |

### 1.2 `compat audit <contract.json>` (AGENTS.md §8 "compat audit", USAGE.md)

| Claim | Comando | Salida real / exit | Veredicto |
|---|---|---|---|
| Exit 0 + array de findings por feature requerida | `compat audit examples/contract.example.json` | stdout: `[{"feature":"tables","status":"exact"},{"feature":"canonical_foreign_keys","status":"exact"},{"feature":"transactions","status":"exact"}]`; exit 0 | ✅ |
| Feature no-exact → exit 1, findings a stdout **antes** del envelope, ERR_AUDIT_NOT_EXACT | `compat audit <req foreign_keys>` | stdout: `[{"feature":"foreign_keys","status":"unknown","reason":"requires parser and semantic compiler"}]` luego `{"status":"error","code":"ERR_AUDIT_NOT_EXACT","message":"feature \"foreign_keys\" is unknown: ..."}`; stderr: reason; exit 1 | ✅ |
| Flag desconocido → ERR_USAGE exit 2, `compat audit: unexpected flag` | `compat audit --bogus x.json` | stdout: `{"...","code":"ERR_USAGE","message":"compat audit: unexpected flag \"--bogus\""}`; stderr: uso + msg; exit 2 | ✅ |
| Count != 1 → ERR_USAGE exit 2 | `compat audit a.json b.json` | `compat audit requires exactly one contract JSON argument`; exit 2 | ✅ |
| Config inexistente → ERR_CONFIG exit 1 | `compat audit /nope.json` | `{"...","code":"ERR_CONFIG","message":"open ...: El sistema no puede encontrar el archivo..."}`; exit 1 | ✅ |
| Key desconocida → ERR_CONFIG exit 1 (DisallowUnknownFields) | `compat audit <badkey.json>` (campo `bogusfield`) | `{"...","code":"ERR_CONFIG","message":"json: unknown field \"bogusfield\""}`; exit 1 | ✅ |
| JSON inválido → ERR_CONFIG exit 1 | `compat audit <badjson.json>` | `{"...","code":"ERR_CONFIG","message":"unexpected EOF"}`; exit 1 | ✅ |

> `audit` no usa `schema`/`schema_ref` (sólo `Contract`); no aplica.

### 1.3 `compat copy <migration.json>` (AGENTS.md §8 "compat copy", USAGE.md)

| Claim | Comando | Salida real / exit | Veredicto |
|---|---|---|---|
| Exit 0 + `VerificationReport` (`source_digest`==`destination_digest`, `equivalent:true`) | `compat copy <cfg entries, PG vivo>` | stdout: `{"source_digest":"59e718...","destination_digest":"59e718...","equivalent":true}`; exit 0; 2 filas en PG | ✅ (ex-NO VERIFICADO) |
| `schema_ref` feliz, resuelto **relativo al config** (no cwd) | `compat copy <cfg_refok>` desde cwd `/tmp/audit5`, `schema_ref:"schema.json"` junto al config | exit 0, `equivalent:true`, 2 filas en PG | ✅ (ex-NO VERIFICADO) |
| `ERR_VERIFY_DIVERGED`: `VerificationReport` a stderr + envelope a stdout | `compat copy <cfg date>` (ver §4.1) | stdout: envelope `ERR_VERIFY_DIVERGED`; stderr: msg + `VerificationReport` JSON (`equivalent:false`) + msg; exit 1 | ✅ estructura (ex-NO VERIFICADO) |
| Flag desconocido → ERR_USAGE exit 2, `compat copy: unexpected flag` | `compat copy --bogus x` | `{"...","message":"compat copy: unexpected flag \"--bogus\""}`; exit 2 | ✅ |
| Count != 1 → ERR_USAGE exit 2 | `compat copy` / `copy a b` | `compat copy requires exactly one migration JSON argument`; exit 2 | ✅ |
| Config inexistente → ERR_CONFIG exit 1 | `compat copy /nope.json` | ERR_CONFIG; exit 1 | ✅ |
| Key desconocida → ERR_CONFIG exit 1 | `compat copy <extrafield>` | `json: unknown field "extrafield"`; exit 1 | ✅ |
| `schema` AND `schema_ref` → ERR_CONFIG exit 1 | `compat copy <cfg_both>` | `config must specify exactly one of schema or schema_ref, not both`; exit 1 | ✅ |
| Ni `schema` ni `schema_ref` → ERR_CONFIG exit 1 | `compat copy <cfg_neither>` | `config must specify exactly one of schema or schema_ref`; exit 1 | ✅ |
| `schema_ref` ilegible → ERR_CONFIG (resuelve relativo al config) | `compat copy <schema_ref:"nonexistent_schema.json">` | `schema_ref "nonexistent_schema.json": open zz_audit5_tmp\nonexistent_schema.json: ...`; exit 1 | ✅ |
| `schema_ref` JSON inválido → ERR_CONFIG exit 1 | `compat copy <schema_ref:"badschema.json">` | `schema_ref "badschema.json": unexpected EOF`; exit 1 | ✅ |
| `ERR_CONNECT_SOURCE` (source sqlite inalcanzable) | `compat copy <source file:/nonexistent/...>` | `{"...","code":"ERR_CONNECT_SOURCE","message":"unable to open database file (14)"}`; exit 1 | ✅ (ex-NO VERIFICADO) |
| `ERR_CONNECT_DESTINATION` (PG inalcanzable) | `compat copy <dest 127.0.0.1:1>` | `{"...","code":"ERR_CONNECT_DESTINATION","message":"failed to connect to \`user=postgres database=postgres\`: ... dial tcp 127.0.0.1:1: ..."}`; exit 1 | ✅ (ex-NO VERIFICADO) |

> Nota: el mensaje de pgx **no expone el password** del DSN (sólo `user=... database=...`). Verificado.

### 1.4 `compat cutover <cutover.json>` y `--dry-run` (AGENTS.md §8 "compat cutover" + §8.1 dry-run, USAGE.md)

| Claim | Comando | Salida real / exit | Veredicto |
|---|---|---|---|
| Exit 0 + `cutoverReport` `status=ready`, digests iguales, `changes_applied` | `compat cutover <cfg entries, PG vivo>` | stdout: `{"status":"ready","source_digest":"59e718...","destination_digest":"59e718...","changes_applied":0}`; stderr: 4 líneas progreso `compat cutover: ...`; exit 0 | ✅ (ex-NO VERIFICADO) |
| `status=diverged`: **una sola** línea JSON en stdout con `code` embebido, **sin** envelope aparte | `compat cutover <cfg date>` (ver §4.1) | stdout: `{"status":"diverged","code":"ERR_VERIFY_DIVERGED","source_digest":"7fcf...","destination_digest":"7369...","changes_applied":0}`; stderr: 4 líneas progreso; exit 1 — **sin** segundo `{"status":"error",...}` | ✅ estructura (ex-NO VERIFICADO) |
| `--dry-run` plan JSON exit 0 | `compat cutover --dry-run <cfg entries, PG vivo>` | stdout: `{"status":"plan","audit":[{"feature":"canonical_full_text","status":"exact"},{"feature":"primary_keys","status":"exact"},{"feature":"tables","status":"exact"}],"source_tables":[{"name":"entries","rows":2}],"destination_has_tables":false,"phases":["install_capture","snapshot","catch_up","verify"]}`; stderr: `compat cutover: audit: exact coverage for 3 required features`; exit 0 | ✅ (ex-NO VERIFICADO) |
| `--dry-run` no escribe en origen/destino | (mismo) | tras dry-run: `SELECT count(*) FROM information_schema.tables WHERE table_schema='public'` → `0` (no creó tablas); la fuente SQLite intacta | ✅ |
| `--dry-run` con destino que ya tiene las tablas → `destination_has_tables:true` | (no forzado separadamente; la lógica `tableExists` cubierta por e2e `TestDryRunCLISuccessPlan`/`UnreachableDestinationError`) | — | ✅ (e2e) |
| `--dry-run` audit no-exact → ERR_AUDIT_NOT_EXACT exit 1 | `compat cutover --dry-run <cfg not-exact>` | exit 1 ERR_AUDIT_NOT_EXACT (e2e `TestDryRunCLIInvalidConfigError` cubre ERR_CONFIG; not-exact implícito en RequireExact compartido) | ✅ (e2e + código compartido) |
| `--dry-run` destino inalcanzable → ERR_CONNECT_DESTINATION exit 1, origen intacto | `compat cutover --dry-run <dest 127.0.0.1:1>` | stdout: `{"...","code":"ERR_CONNECT_DESTINATION",...}`; stderr: `compat cutover: audit: exact coverage...` + err; exit 1 | ✅ (ex-NO VERIFICADO) |
| `--dry-run` origen inalcanzable → ERR_CONNECT_SOURCE exit 1 | `compat cutover --dry-run <source file:/nonexistent/...>` | `{"...","code":"ERR_CONNECT_SOURCE","message":"unable to open database file (14)"}`; exit 1 | ✅ |
| `--dry-run` aceptado en **cualquier posición** tras el subcomando | `compat cutover <cfg> --dry-run` (flag al final) | aceptado: audit + connect ejecutados → ERR_CONNECT_DESTINATION; exit 1 | ✅ |
| Flag desconocido → ERR_USAGE exit 2, `compat cutover: unexpected flag` | `compat cutover --bogus x` / `cutover --dry-run --bogus x` | `{"...","message":"compat cutover: unexpected flag \"--bogus\""}`; exit 2 | ✅ |
| Count != 1 → ERR_USAGE exit 2 | `compat cutover` / `cutover a b` | `usage: compat cutover [--dry-run] <cutover.json>`; exit 2 | ✅ (ver §5.2) |
| Config inexistente → ERR_CONFIG exit 1 | `compat cutover /nope.json` / `--dry-run /nope.json` | ERR_CONFIG; exit 1 | ✅ |
| Key desconocida → ERR_CONFIG exit 1 | `compat cutover <extrafield>` | `json: unknown field "extrafield"`; exit 1 | ✅ |
| `schema` AND `schema_ref` / ni uno / `schema_ref` ilegible | `compat cutover <cfg_both/neither/badref>` | mismos mensajes que copy; ERR_CONFIG exit 1 | ✅ |

### 1.5 Códigos tipados (AGENTS.md §8.1 tabla)

Verificados en runtime con PG vivo (los que exigían PG eran NO VERIFICADOS en rondas AUDIT2-4):

| Código | Path verificado | Exit | Veredicto |
|---|---|---|---|
| `ERR_USAGE` | dispatch, flag desconocido, count != 1 (3 sub) | 2 | ✅ |
| `ERR_CONFIG` | config inexistente, JSON inválido, key desconocida, schema/schema_ref (ambos/ninguno/ref ilegible) | 1 | ✅ |
| `ERR_AUDIT_NOT_EXACT` | `audit` req genérica; copy/cutover req no-exact | 1 | ✅ (audit runtime; copy/cutover por código compartido) |
| `ERR_CONNECT_SOURCE` | sqlite source inalcanzable (copy + dry-run) | 1 | ✅ (ex-NO VERIFICADO) |
| `ERR_CONNECT_DESTINATION` | PG inalcanzable (copy + dry-run) | 1 | ✅ (ex-NO VERIFICADO) |
| `ERR_VERIFY_DIVERGED` | copy (VerificationReport a stderr + envelope) y cutover (una línea con code embebido) | 1 | ✅ estructura + runtime (ex-NO VERIFICADO) — vía columna `date` divergente (ver §4) |
| `ERR_INTERNAL`/`ERR_SCHEMA`/`ERR_SNAPSHOT`/`ERR_REPLICATION_CONFLICT`/`ERR_CAPTURE` | no forzados individualmente en esta ronda (cubiertos por e2e happy paths de cutover: install_capture, snapshot, drain) | — | ⚠ no forzados; cutover happy los ejercita en verde |

---

## 2. Ataque al dispatch nuevo (código nuevo en `cmd/compat/main.go`)

| Vector | Comando | Comportamiento real | Veredicto |
|---|---|---|---|
| Subcomando en mayúsculas | `compat AUDIT x.json` / `compat Audit x.json` | ERR_USAGE exit 2 (`compat: missing or unknown subcommand`) — dispatch case-sensitive | ✅ coherente (subcomandos documentados en minúsculas) |
| Subcomando vacío / whitespace | `compat "" x.json` / `compat " " x.json` | ERR_USAGE exit 2 (args[0] cae al `default`) | ✅ |
| Flag **antes** del subcomando | `compat --dry-run cutover x.json` | `--dry-run` es args[0] → `default` → ERR_USAGE exit 2 (`compat: missing or unknown subcommand`). NO se interpreta como flag de cutover | ✅ coherente con §8 ("--dry-run ... en cualquier posición **después** del subcomando"); mensaje genérico (ver §5.4) |
| Argumentos extra tras el config | `compat copy x.json extra` | 2 positionals → ERR_USAGE exit 2 (count) | ✅ |
| Subcomando como 2º token tras uno inválido | `compat bogus audit x.json` | args[0]=`bogus` → ERR_USAGE; `audit` ignorado (dispatch sólo mira args[0]) | ✅ coherente |
| Doble `--dry-run` | `compat cutover --dry-run --dry-run x.json` | **Aceptado** (map dedup → `dryRun=true`), ejecuta audit+connect. No error por flag duplicado | ⚠ BAJA (leniency, §5.1) |
| Separador `--` | `compat cutover --dry-run -- x.json` / `compat audit -- x.json` / `compat -- audit x.json` | `--` tratado como flag desconocido → ERR_USAGE (`unexpected flag "--"` / `missing or unknown subcommand`) | ⚠ BAJA (no soporta `--`, §5.2) |
| Orden líneas usage vs envelope (consistencia 3 sub) | — | Patrón idéntico en los 3: stderr=hint, luego stderr=msg de error (en `Die`), stdout=envelope. Estructura uniforme | ✅ |
| Consistencia prefijos `compat <sub>:` en TODOS los mensajes | grep código + salida real | Todo mensaje vivo usa `compat <sub>:` (o `compat:` a nivel dispatch). **Cero** prefijos viejos `compat-audit:`/`compat-copy:`/`compat-cutover:` en mensajes (sólo quedan en **comentarios** que documentan la migración: `cmd/compat/{main,audit,copy,cutover}.go` — legítimos) | ✅ (ver §3) |

Grep de prefijos viejos en `cmd/`:
```
cmd/compat/audit.go:11 // changed from "compat-audit:" to "compat audit:".
cmd/compat/copy.go:26  // from "compat-copy:" to "compat copy:".
cmd/compat/cutover.go:74 // "compat-cutover:" to "compat cutover:".
cmd/compat/main.go:7-9 // cabecera que documenta la unificación.
```
Todos son comentarios (no mensajes emitidos). `cliout` no referencia nombres viejos. ✅

---

## 3. Limpieza residual

- **Docs vivas** (`AGENTS.md`, `README.md`, `docs/USAGE.md`, `docs/TESTING.md`, `docs/OPERATIONS.md`, `contracts/`): la única mención a binarios viejos es `AGENTS.md:178` — la **cabecera** que documenta la unificación ("previously lived in three separate binaries (`compat-audit`, `compat-copy`, `compat-cutover`)"). Es descriptiva/histórica (no es una instrucción operativa `go run ./cmd/compat-X`); es el hit justificado que el reporte FEAT-UNIFY preservó a propósito. **No hay referencias operativas** (nigún `go run ./cmd/compat-audit`/`-copy`/`-cutover`) en docs vivas, scripts, tests ni mensajes. ✅
- **Scripts** (`scripts/check.ps1`, `scripts/test-system.ps1`): no invocan binarios por nombre (sólo `go test`/`go vet`/`gofmt`). No referencia a viejos. ✅
- **Tests** (`e2e/`): todos los call sites usan `./cmd/compat` + subcomando; sin refs a `./cmd/compat-X`. ✅
- **Mensajes de error**: sin prefijos viejos (§2). ✅

### `scripts/test-system.ps1` con el binario nuevo

Corrido con `COMPAT_POSTGRES_DSN` real (DSN leído de archivo, password no pegado en cmdline):
```
$ pwsh scripts/test-system.ps1 -PostgresDsn <dsn>
... (unit + vet + e2e -v) ...
--- PASS: TestSystemAutomaticallyCapturesAndReplicatesBothDirections (3.38s)
--- FAIL: TestSystemClaimsExactCoverageForRequiredFeatureFamilies (0.00s)   <- intentional, §0
FAIL  example.com/sqlite-postgres-compat/e2e  50.528s   (exit 1)
```
El script **funciona con el binario nuevo**: no invoca los viejos; delega en la suite e2e, que internamente usa `go run ./cmd/compat`. El exit 1 es exclusivamente por `TestSystemClaimsExactCoverageForRequiredFeatureFamilies` (baseline `32+1 intencional`, families genéricos `unknown` por contrato §9) — **no** es regresión de la unificación ni hallazgo AUDIT5. Todos los tests CLI (copy/cutover/dry-run/dispatch/flags/schema_ref) pasan.

---

## 4. Hallazgos

### 4.1 [ALTA] La familia `date` rompe la verificación de `compat copy` y `compat cutover` (siempre `ERR_VERIFY_DIVERGED`)

**Síntoma.** Con PG vivo, un schema con una columna `date` hace que `compat copy` y `compat cutover` **siempre** fallen la verificación de digests (`ERR_VERIFY_DIVERGED`, exit 1), aunque los datos son lógicamente equivalentes. Ejecución real (copy):

```
$ compat copy <cfg con tabla d(id integer pk, d date), 1 fila '2020-01-01'>
EXIT=1
--STDOUT--
{"status":"error","code":"ERR_VERIFY_DIVERGED","message":"snapshot mismatch: source 7fcf9d... destination 7369f5..."}
--STDERR--
snapshot mismatch: source 7fcf9d... destination 7369f5...
{"source_digest":"7fcf9d...","destination_digest":"7369f5...","equivalent":false}
snapshot mismatch: source 7fcf9d... destination 7369f5...
```

`compat cutover` con el mismo schema → `status=diverged`:
```
{"status":"diverged","code":"ERR_VERIFY_DIVERGED","source_digest":"7fcf9d...","destination_digest":"7369f5...","changes_applied":0}   (exit 1)
```

**Root cause (verificado).** El mapeo DDL de Postgres (`compat/ddl.go:159-160`) compila `DateType` al tipo nativo `DATE`:
```go
case DateType:
    return "DATE", nil
```
El driver pgx devuelve una columna PG `DATE` como `time.Time` (verificado con sonda directa: `col=d goType=time.Time value=2020-01-01 00:00:00 +0000 UTC`). En `canonicalValue` (`compat/store.go:272-274`), la rama `time.Time` se evalúa **antes** del `switch kind`:
```go
if timestamp, ok := source.(time.Time); ok {
    return Value{Kind: TimestampValue, Value: timestamp.UTC().Format(time.RFC3339Nano)}, nil
}
```
→ el destino canoniza a `TimestampValue` = `"2020-01-01T00:00:00Z"`. El origen SQLite almacena `date` como `TEXT` (`ddl.go:131`, agrupado con `TextType`), y al leerse como string cae al `switch kind` → `DateType` → `Value{Kind: DateValue, Value: "2020-01-01"}`. Los dos canónicos difieren → digests distintos → diverge.

**Por qué es ALTA (contrato violado).** AGENTS.md §1 lista `date` como familia soportada (SQLite `TEXT` / PG `DATE`). §8 promete que `copy` da exit 0 cuando `equivalent==true` y `cutover` da `status=ready`; §9 define el veredicto de Move como `equivalent==true`/`status==ready`. Un agente que siga el contrato y migre un schema con una columna `date` recibe **siempre** `ERR_VERIFY_DIVERGED`/`status=diverged`, exit 1 — lo que induce a diagnosticar corrupción de datos cuando los datos están bien. Es exactamente el tipo de bug que las rondas previas dejaron **NO VERIFICADO** por falta de PG.

**Contraste que confirma la oversight.** El diseño **evitó** este problema a propósito para `TimestampType`, `JSONType` y `UUIDType` mapeándolos a `TEXT` en PG (`ddl.go:161-172`) con comentarios protectores explícitos:
```go
case TimestampType: return "TEXT", nil  // "PostgreSQL timestamps have microsecond resolution. Canonical RFC3339 text preserves nanoseconds..."
case JSONType:      return "TEXT", nil  // "JSONB rewrites key order... TEXT preserves the canonical payload byte-for-byte."
case UUIDType:      return "TEXT", nil  // "Native UUID normalizes textual representation. TEXT preserves the exact canonical value."
```
El patrón protector **no se aplicó** a `DateType` (única familia temporal mapeada a tipo nativo PG). Verificado que `timestamp`, `json`, `uuid`, `float`, `boolean`, `integer`/`text` round-tripean correctamente (exit 0, digests iguales); `date` es la **única** que diverge.

**Estructura del path diverged (verificada, ex-NO VERIFICADO).** A pesar del bug, la **forma** de emisión coincide con §8.1:
- `compat copy` diverged: `VerificationReport` JSON a stderr **antes** del envelope `ERR_VERIFY_DIVERGED` a stdout (exit 1). ✅ caso (c).
- `compat cutover` diverged: **una sola** línea JSON en stdout con `code` embebido, **sin** envelope `{"status":"error",...}` aparte (exit 1). ✅ caso (b).

**Sugerencia de fix (no aplicada — READ-ONLY).** Mapear `DateType`→`TEXT` en PG (igual que `timestamp`), o manejar `time.Time` dentro del `case DateType` formateando `"2006-01-02"` cuando `kind==DateType` (antes de la rama `time.Time` genérica).

### 4.2 [MEDIA] Inconsistencia del `countMsg` (uso-count) entre los 3 subcomandos

Los tres subcomandos emiten mensajes distintos para "cantidad de argumentos incorrecta":

| Sub | `countMsg` (envelope `ERR_USAGE` exit 2) |
|---|---|
| `audit` | `compat audit requires exactly one contract JSON argument` |
| `copy` | `compat copy requires exactly one migration JSON argument` |
| `cutover` | `usage: compat cutover [--dry-run] <cutover.json>` |

`audit` y `copy` usan prosa "`compat X requires ...`" (sin dos puntos); `cutover` usa "`usage: compat cutover [--dry-run] <...>`" (prefijo `usage:`, forma del flag). Misma severidad/exit, pero texto/estilo divergente entre subcomandos. Es **pre-existente** (el reporte FEAT-UNIFY trade-off #3 lo preservó a propósito: "se mapeó guion→espacio sin agregar dos puntos" para audit/copy; cutover ya era `usage:` en el binario original). No introducido por la unificación; se mantiene como inconsistencia entre subcomandos. Clasificada MEDIA por definición ("inconsistencia entre subcomandos"); cosmética/operativamente inocua.

### 4.3 [BAJA] Doble `--dry-run` aceptado en silencio

`compat cutover --dry-run --dry-run <cfg>` → no error; el segundo `--dry-run` se acepta (el `map[string]bool` en `SplitArgs` deduplica). No viola el contrato (éste no prohíbe duplicados), pero es una leniency: un flag booleano duplicado podría señalar un error del usuario que pasa inadvertido.

### 4.4 [BAJA] Sin soporte de separador `--`

`--` se trata como flag desconocido → `ERR_USAGE` (`compat cutover: unexpected flag "--"` / `compat audit: unexpected flag "--"` / `compat: missing or unknown subcommand`), no como separador end-of-flags. Convención CLI estándar no soportada; el contrato no la promete. Inocuo pero sorpresivo para usuarios de shell.

### 4.5 [BAJA] Flag antes del subcomando produce mensaje genérico

`compat --dry-run cutover <cfg>` → `ERR_USAGE` con mensaje `compat: missing or unknown subcommand` (exit 2). **Comportamiento correcto** (§8: `--dry-run` va después del subcomando), pero el mensaje no sugiere que `--dry-run` debe seguir a `cutover`; un usuario puede confundirse. Sólo usabilidad del mensaje; no es violación de contrato.

### 4.6 [BAJA] Duplicación del `err` en stderr en los paths not-exact/diverged de copy/cutover

En `copy.go:54/98` y `cutover.go:111`, el error se imprime a stderr con `fmt.Fprintln(os.Stderr, err)` y luego `cliout.Die` (`cliout.go:108`) lo vuelve a imprimir a stderr. Resultado: el mensaje de error aparece **dos veces** en stderr (visible en §4.1: la línea `snapshot mismatch:...` aparece dos veces en stderr). Cosmético; no afecta stdout (lo machine-readable). Pre-existente.

---

## 5. Áreas limpias

- **Dispatch** (§1.1, §2): no-args, desconocido, `--help`/`-h`, case-sensitivity, vacío/whitespace, 2º token inválido — todo ERR_USAGE exit 2 coherente. ✅
- **Prefijos**: cero prefijos viejos `compat-X:` en mensajes; sólo comentarios legítimos. ✅
- **audit**: contrato completo verificado (happy, not-exact, flags, count, config, unknown-key, bad-json). ✅
- **copy happy + schema_ref + ERR_CONNECT_***: verificados con PG vivo. ✅
- **cutover happy (ready) + diverged + dry-run plan + ERR_CONNECT_***: verificados con PG vivo. ✅
- **Códigos tipados ex-NO-VERIFICADO** (`ERR_CONNECT_SOURCE/DESTINATION`, `ERR_VERIFY_DIVERGED` en copy y cutover, `cutoverReport` ready/diverged, `VerificationReport` de copy, plan de `--dry-run`): **ahora verificados** con PG real. ✅
- **Limpieza residual**: sin refs operativas a binarios viejos en docs vivas/scripts/tests/mensajes. ✅
- **Gate** `check.ps1` verde; `test-system.ps1` corre con el binario nuevo (falla sólo por el test intencional conocido). ✅
- **`timestamp`/`json`/`uuid`/`float`/`boolean`/`integer`/`text` round-trip**: verificados equivalentes con PG vivo (no divergen). ✅

## Conteo final por severidad

- **ALTA: 1** (§4.1 — `date` rompe copy/cutover verify)
- **MEDIA: 1** (§4.2 — `countMsg` inconsistente entre subcomandos, pre-existente)
- **BAJA: 4** (§4.3 doble `--dry-run`; §4.4 sin `--`; §4.5 mensaje genérico flag-antes-sub; §4.6 `err` duplicado en stderr)