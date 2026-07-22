# AUDIT3-C — Ronda 3, auditor C (CLIs / superficie AI-first / docs)

Auditor efímero sin memoria previa. HEAD `c09ee8f`. Scope: CLIs
(`compat-audit`, `compat-copy`, `compat-cutover`), contrato machine-facing
documentado en `AGENTS.md` §8/§8.1 y `docs/USAGE.md`, y coherencia
docs↔código de README/TESTING/examples/contracts. El núcleo SQL y el runtime
los cubren los auditores A y B — no se duplica.

## Resumen ejecutivo

**El contrato documentado se cumple al pie de la letra en HEAD para toda la
superficie AI-first verificable sin PostgreSQL.** Los 3 CLIs emiten
exactamente los exit codes (0/1/2), envelopes JSON
`{"status":"error","code":"<CODE>","message":"..."}` en una sola línea en
stdout (mensaje JSON-encodeado, newlines embebidos no rompen la línea),
streams (stdout vs stderr) y orden de líneas que documentan §8/§8.1 y
USAGE.md. La paridad `compat-copy` ↔ `compat-cutover` prometida por el fix M3
en el path `ERR_AUDIT_NOT_EXACT` (findings JSON a stderr antes del envelope)
es estructuralmente idéntica. `schema_ref` (resolución relativa al config,
exactly-one-of, `DisallowUnknownFields`) y todos los códigos de error
alcanzables sin PG dan el código correcto.

**1 hallazgo MEDIA** (conteo de tests e2e documentado stale: 28 vs 33 real) y
**2 BAJA** (cosmético en el ejemplo de stderr del contrato; `scripts/check.ps1`
nuevo sin documentar). Áreas de comportamiento del contrato: limpias.

Los caminos que requieren PostgreSQL (`ERR_VERIFY_DIVERGED` en ambos CLIs,
`cutoverReport` ready/diverged, `VerificationReport` de copy exitoso, y el
plan JSON de `--dry-run` exitoso) quedan **NO VERIFICADOS** (sin PG en este
entorno), conforme a la consigna.

## Metodología

- Binarios buildeados con `go build -o <tmp>/compat-{audit,copy,cutover}.exe`
  (no `go run`): en Windows `go run` colapsa exit≠0 a 1, así que los exit codes
  se midieron sobre el binario real. Verificado el colapso: `go run
  ./cmd/compat-audit` (sin args) devolvió exit 1, mientras el binario midió
  exit 2.
- Cada path de error ejecutado, capturando stdout y stderr por separado y el
  exit code real. Configs de prueba en un tempdir fuera del repo; los caminos
  que abren SQLite se corrieron desde un cwd limpio (`<tmp>/cutcwd`) para no
  ensuciar el repo.
- `git status --short` final esperado: solo `docs/reports/AUDIT3-C.md`.

## Tabla claim → verificación → resultado

| Claim (doc) | Verificación (comando real + salida + exit) | Resultado |
|---|---|---|
| §8: arg count ≠ 1 → `ERR_USAGE` exit 2 (los 3 CLIs) | `compat-audit` (0 args) exit 2, envelope `{"status":"error","code":"ERR_USAGE","message":"compat-audit requires exactly one contract JSON argument"}`. `compat-audit a b` exit 2 ídem. `compat-copy` (0 args) exit 2 ídem (mensaje "migration"). `compat-cutover` (0 args) exit 2 ídem. | ✅ |
| §8: flag `-` desconocido → `ERR_USAGE` exit 2, jamás tratado como config posicional | `compat-audit --bogus cfg` exit 2 `unexpected flag "--bogus"`; `compat-audit -x foo` exit 2 `unexpected flag "-x"`. `compat-copy --bogus x` exit 2 ídem. `compat-cutover --bogus c` y `compat-cutover --dry-run --bogus c` exit 2 ídem. | ✅ |
| §8: `--dry-run` sólo `compat-cutover`; `compat-audit`/`compat-copy` sin flags | `--dry-run` rechazado en audit/copy como flag desconocido (exit 2); aceptado en cutover (`E4-E6`). | ✅ |
| §8.1: envelope simple en stdout, una línea, mensaje JSON-encodeado | Todo error emite exactamente una línea JSON en stdout con `status/code/message`. Mensaje multilinea (p.ej. connect dest) queda como una sola línea (`\n` escapado a `\\n`). | ✅ |
| §8.1: `compat-audit` findings a **stdout** luego envelope en `ERR_AUDIT_NOT_EXACT` | `required_features=["tables","views"]`: stdout = `[{"feature":"tables","status":"exact"},{"feature":"views","status":"unknown",...}]` **+** `{"status":"error","code":"ERR_AUDIT_NOT_EXACT",...}`; stderr = razón. exit 1. (B7/B8) | ✅ |
| §8.1: `compat-copy` y `compat-cutover` findings a **stderr** luego envelope en `ERR_AUDIT_NOT_EXACT` (M3) | copy (D6) y cutover (E7, E8): stdout = solo envelope `ERR_AUDIT_NOT_EXACT`; stderr = `razón` + arreglo `[]Finding` JSON + `razón` (la doble impresión de `err` es intencional y coincide con cutover, ver FIX-M3-AUDIT2C-REPORT.md). exit 1. | ✅ |
| M3: formato findings-stderr de copy **idéntico** a cutover en `ERR_AUDIT_NOT_EXACT` | D6 (copy) y E7 (cutover) producen la misma estructura de stderr: `<err>\n<findings json>\n<err>\n` y envelope en stdout. (Orden interno de findings no determinístico por `InferFeatures` sobre map — pero no determinístico en **ambos** por igual, paridad preservada.) | ✅ |
| §8.1: `ERR_CONFIG` = archivo ilegible / decode / `DisallowUnknownFields` / `Contract.Validate` | missing file exit 1 `ERR_CONFIG`; JSON roto exit 1 `ERR_CONFIG` "unexpected EOF"; key desconocida exit 1 `ERR_CONFIG` "json: unknown field"; same engine exit 1 `ERR_CONFIG` "source and destination must be different engines"; `version 0.0.0` exit 1 `ERR_CONFIG` "source: invalid sqlite version 0.0.0"; engine `mysql` exit 1 `ERR_CONFIG` "source: unsupported engine". (B1-B6, C4-C6) | ✅ |
| §8.1: `schema`/`schema_ref` both/neither → `ERR_CONFIG` | both exit 1 `ERR_CONFIG` "config must specify exactly one of schema or schema_ref, not both"; neither exit 1 `ERR_CONFIG` "...exactly one of schema or schema_ref". (C7/C8) | ✅ |
| §8.1: `schema_ref` resuelto **relativo al config**, no al cwd | config en `<tmp>/sub/cfg_refok.json` con `schema_ref:"s.json"` corrido desde la raíz del repo (cwd ≠ dir del config): resuelve `s.json` junto al config, pasa audit+validate, llega a `ERR_CONNECT_DESTINATION`. (D1) | ✅ |
| §8.1: `schema_ref` ilegible / JSON inválido / key desconocida → `ERR_CONFIG` | unreadable exit 1 `ERR_CONFIG` `schema_ref "missing.json": open ...`; JSON inválido exit 1 `ERR_CONFIG` `schema_ref "brokenref.json": invalid character...`; key desconocida en el ref exit 1 `ERR_CONFIG` `schema_ref "refunknown.json": json: unknown field "X"`. (D2-D4) | ✅ |
| §8.1: `ERR_SCHEMA` = `Schema.Validate` falla | schema con tabla duplicada exit 1 `ERR_SCHEMA` `duplicate table "t"`. (D5) | ✅ |
| §8.1: `ERR_CONNECT_SOURCE` / `ERR_CONNECT_DESTINATION` | source SQLite en dir inexistente → exit 1 `ERR_CONNECT_SOURCE` "unable to open database file (14)" (cutover, dry-run, copy). source SQLite válido + dest PG inalcanzable → exit 1 `ERR_CONNECT_DESTINATION` (cutover, dry-run, copy). (F1-F6) | ✅ |
| §8 audit: ejemplo ejecutable `examples/contract.example.json` → arreglo + exit 0 | `compat-audit examples/contract.example.json` exit 0, stdout = `[{"feature":"tables","status":"exact"},{"feature":"canonical_foreign_keys","status":"exact"},{"feature":"transactions","status":"exact"}]` (idéntico byte-a-byte al ejemplo de §8). (A5) | ✅ |
| `examples/schema.example.json` = `compat.Schema` bare decodable (DisallowUnknownFields) + válido | config con `schema_ref:"schema.example.json"`: decodifica, valida, audit exact, llega a connect. (G2) | ✅ |
| BAJA #7 (M3): rename `TestAuditCLIErrorCodeOnNotExact` presente, viejo `TestCutoverAuditCLIErrorCodeOnNotExact` ausente | `grep` en `e2e/`: solo `TestAuditCLIErrorCodeOnNotExact` existe; el viejo nombre no aparece. (G5) | ✅ |
| c09ee8f: HEAD gofmt-clean | `gofmt -l` sobre archivos `.go` tracked = vacío (limpio). `go vet ./...` exit 0. (G3/G4) | ✅ |

## Hallazgos

### MEDIA #1 — Conteo de tests e2E documentado stale (28 vs 33)

**Docs:** `README.md:57`, `docs/TESTING.md:44,48`, `docs/reports/VALIDATION_REPORT.md:65`
afirman «28 pruebas de nivel superior (27 superadas + 1 fallida,
`TestSystemClaimsExactCoverageForRequiredFeatureFamilies`)».

**Código (HEAD):** `grep -rhE "^func Test[A-Z]" e2e/*.go | grep -v TestMain | wc -l`
= **33** funciones de test top-level en `e2e/` (34 incluyendo `TestMain`):
- `e2e/cutover_test.go`: 14
- `e2e/suppress_test.go`: 3
- `e2e/system_test.go`: 16

**Causa:** `VALIDATION_REPORT.md` se escribió en `af83514` (14:35) con 28. El
commit **`aba9f15`** ("feat(cli): ERR_USAGE for unknown flags, real schema_ref,
reject unknown keys") agregó **5** tests top-level a `e2e/cutover_test.go`
(verbatim del `git show`):
`TestCLIRejectsUnknownFlagAsUsage`, `TestCutoverRejectsUnknownConfigKey`,
`TestCutoverRejectsSchemaAndSchemaRef`,
`TestCutoverRejectsMissingSchemaAndSchemaRef`, `TestCutoverWithSchemaRefSucceeds`.
28 + 5 = 33. (`c51b49a` sólo renombró, net 0 al conteo.)

**Impacto:** un agente que lea «28/27/1» modela mal la batería (cree que hay 5
tests menos de los reales). El sub-claim «27 superadas» no es verificable sin
PG, pero el total «28» es concretamente falso.

**Severidad:** MEDIA (hueco/staleness de docs; no es una violación de contrato
de comportamiento que induzca a un agente a una acción incorrecta — por eso no
ALTA).

### BAJA #1 — Prefijo `compat-cutover:` omitido en el ejemplo de stderr del contrato

**Doc:** `contracts/migration.contract.example.md` §2 (Verdicts, paso 2):
«Expected stderr: progress lines `audit: ...`, `capture: ...`, `snapshot:
...`, `catch-up: ...`.»

**Código:** `logf` (`cmd/compat-cutover/main.go:320-322`) prefija cada línea con
`compat-cutover: `. Salida real observada (F1):
`compat-cutover: audit: exact coverage for 3 required features`.

**Severidad:** BAJA (cosmético: el ejemplo omite el prefijo; no afecta parsing
machine-facing, que vive en stdout).

### BAJA #2 — `scripts/check.ps1` nuevo sin documentar

**Código:** `scripts/check.ps1` se agregó en `c09ee8f` (gate de calidad:
`gofmt -l .` + `go vet ./...` + `go test ./... -count=1`).

**Docs:** `README.md:71` describe `scripts/` como «script de validación integral
(`test-system.ps1`)» (singular, sólo test-system). `docs/TESTING.md` no
menciona `check.ps1`. Ningún comando documentado quedó roto por el script nuevo;
simplemente el nuevo gate no está referenciado en ningún doc.

**Severidad:** BAJA (gap de doc; nada roto).

## Áreas limpias (explícito)

Cumplen el contrato byte-a-comportamiento en HEAD, sin desviación:
- **Exit codes 0/1/2** y **envelope simple** (stdout, una línea, mensaje
  JSON-escapeado) en los 3 CLIs.
- **`ERR_USAGE`** por conteo incorrecto **y** flag desconocido (3 CLIs).
- **Split stdout/stderr de `ERR_AUDIT_NOT_EXACT`**: audit → stdout, copy/cutover
  → stderr, exactamente como §8.1.
- **Paridad M3** copy↔cutover en findings-stderr de `ERR_AUDIT_NOT_EXACT`.
- **`ERR_CONFIG`**: ilegible, decode, `DisallowUnknownFields` (key desconocida
  en config y en `schema_ref`), `Contract.Validate` (mismo engine, versión
  `0.0.0`, engine no soportado).
- **`schema_ref`**: resolución relativa al config (no al cwd), exactly-one-of,
  ilegible/JSON-inválido/key-desconocida → `ERR_CONFIG`.
- **`ERR_SCHEMA`** (`Schema.Validate`), **`ERR_CONNECT_SOURCE/DESTINATION`**.
- **`--dry-run`** sólo en cutover; sus caminos de error (audit-not-exact,
  connect) verificables dan el código correcto.
- **Ejemplos ejecutables**: `examples/contract.example.json` (audit → arreglo
  idéntico al ejemplo de §8, exit 0) y `examples/schema.example.json` (bare
  `Schema` válido).
- **Rename e2e** (BAJA #7 de la ronda 2): `TestAuditCLIErrorCodeOnNotExact`
  presente, viejo nombre ausente. HEAD gofmt-clean, `go vet` limpio.

## NO VERIFICADOS (requieren PostgreSQL)

Sin PG en este entorno, conforme a la consigna:
- **`ERR_VERIFY_DIVERGED`** — `compat-cutover` (una línea `cutoverReport` con
  `code` embebido, sin envelope) y `compat-copy` (`VerificationReport` a stderr
  + envelope). Requiere llegar a la fase de verify (ambos stores conectados).
- **`cutoverReport` exitoso** (`status=ready`, exit 0) y `status=diverged`
  (exit 1).
- **`VerificationReport` exitoso** de `compat-copy`
  (`{"source_digest","destination_digest","equivalent":true}`, exit 0).
- **Plan JSON de `--dry-run` exitoso** (`{"status":"plan","audit":...,
  "source_tables":...,"destination_has_tables":...,"phases":...}`, exit 0):
  source SQLite conecta, pero dest PG inalcanzable.

Nota: las **estructuras** de estos reportes (campos JSON, `code,omitempty`,
`phases` fijo `["install_capture","snapshot","catch_up","verify"]`) son legibles
en el código (`cmd/compat-cutover/main.go:56-80`, `planReport`) y coinciden con
lo documentado; lo no verificado es la **emisión real** de esos reportes en
runtime, que necesita PG.

## Observación (no es hallazgo — fuera de scope / ambiental)

Durante la sesión aparecieron en el árbol dos archivos `.go` no trackeados en
`compat/` (`zz_audit3_repro_test.go`, `zz_audit3a_sqlcore_test.go`) — WIP de los
auditores A/B concurrentes (no míos; `compat/` es READ-ONLY para mí y nunca
escribí ahí). Están sin formatear (`gofmt -l .` los lista), de modo que
`scripts/check.ps1` fallaría el gate `gofmt` sobre el **árbol de trabajo
actual** — pero **HEAD es gofmt-clean**, así que no es un defecto del repo ni del
contrato. También apareció un `s.db` scratch de 0 bytes en la raíz (artefacto
runtime, no fuente); lo removí. No toqué los `.go` ajenos.

## Conteo final

- **ALTA: 0**
- **MEDIA: 1** (conteo de tests e2e stale 28 vs 33)
- **BAJA: 2** (prefijo stderr en ejemplo del contrato; `check.ps1` sin documentar)