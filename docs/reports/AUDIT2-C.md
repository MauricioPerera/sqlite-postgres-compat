# AUDIT2-C — cmd/**, AGENTS.md, contracts/, README/USAGE (read-only)

Scope per assignment: `cmd/**` (3 CLIs + `cliout`), `AGENTS.md`, `README.md`, `docs/USAGE.md`, `contracts/`, `examples/*.json`. `compat/` touched only via targeted grep to confirm cited symbols/behavior exist (not audited). `e2e/*.go` read only for coherence, not audited line by line.

Baseline verified green before any analysis:
- `go build ./...` → no output, exit 0.
- `go vet ./...` → no output, exit 0.
- `go test ./...` → `ok example.com/sqlite-postgres-compat/compat 2.345s` (cmd packages have no test files).

No repo files were modified. All CLI executions below used temp files outside the repo (`%TEMP%/audit_test*`).

## Findings

### ALTA

1. **`ERR_USAGE` does not actually cover "unexpected flag"; the doc claims it does, twice.**
   `AGENTS.md:191` and `docs/USAGE.md:229` both state: `ERR_USAGE | Wrong argument count (or an unexpected flag). | 2`. The real implementation (`cmd/compat-cutover/main.go:82-91`) only special-cases the literal token `--dry-run`; any other flag is not recognized as a flag at all — it falls straight through the arg-count check (which only compares `len(args) != 1`) and is treated as the **positional JSON config path**. Reproduced:
   ```
   $ go run ./cmd/compat-cutover --bogus-flag
   open --bogus-flag: El sistema no puede encontrar el archivo especificado.
   {"status":"error","code":"ERR_CONFIG","message":"open --bogus-flag: ..."}
   ```
   Exit is 1 with `ERR_CONFIG`, not 2 with `ERR_USAGE` as documented. `cmd/internal/cliout/cliout.go:27-28`'s own comment ("incorrect CLI invocation (wrong argument count)") does *not* make the flag claim — only the two markdown docs do, and both are wrong. An agent branching on `code=="ERR_USAGE"` to detect a bad flag will misclassify this as a config/file error.

2. **`schema_ref` is documented as a valid alternative to inline `schema` in `cutover.json`, but no CLI struct parses it — the field is silently dropped, not rejected.**
   `contracts/migration.contract.example.md:15-19,70` and its own frontmatter (`schema_ref: examples/migration.example.json`) present `schema_ref` as first-class, and prose step 2 says "Prepare a `cutover.json` with ... `schema` (or `schema_ref`)". Neither `cutoverConfig` (`cmd/compat-cutover/main.go:28-34`) nor `migrationConfig` (`cmd/compat-copy/main.go:15-20`) has a `schema_ref` field or any file-resolution logic for it — `encoding/json` silently ignores the unknown key, leaving `Schema` at its zero value. Reproduced: a `cutover.json` with only `schema_ref` (no `schema`) passes `Schema.Validate()` (empty schema validates trivially — 0 tables) and proceeds straight to the connect phase, silently running with **no schema at all**, instead of failing fast with `ERR_CONFIG`:
   ```
   $ go run ./cmd/compat-cutover --dry-run cutover_schemaref.json
   compat-cutover: audit: exact coverage for 2 required features
   unable to open database file: out of memory (14)
   {"status":"error","code":"ERR_CONNECT_SOURCE", ...}
   ```
   This is the one place in the CLI surface that silently degrades rather than rejecting explicitly — the opposite of the design principle stated in `AGENTS.md:3` ("If you generate something outside this grammar, the layer rejects it... rather than silently degrading") and `README.md:7`. Either implement `schema_ref` resolution or remove it from the contract template/doc.

### MEDIA

3. **`compat-copy`'s `ERR_AUDIT_NOT_EXACT` path emits no findings anywhere (stdout or stderr) — inconsistent with the other two CLIs.**
   `cmd/compat-copy/main.go:39-45`: on `RequireExact` failure it calls `fail(cliout.ErrAuditNotExact, err)`, which only prints the single-feature error message — the full `[]Finding` array is never written. Contrast with `compat-audit` (`cmd/compat-audit/main.go:35-42`, findings on stdout before the error line) and `compat-cutover` (`cmd/compat-cutover/main.go:112-117`, findings JSON on **stderr** before the error line). `AGENTS.md`/`docs/USAGE.md` don't explicitly claim compat-copy prints findings, so this isn't a doc contradiction, but it is a real behavioral inconsistency across "the same phase heuristic" the taxonomy claims to share (`AGENTS.md:203`), and it means an agent debugging a non-exact audit via `compat-copy` gets only the first-failing feature, never the full picture the other two CLIs give.

4. **Unsupported/typo'd column type families are not caught by `Schema.Validate()`; they surface much later, past both DB connections, misclassified as `ERR_SNAPSHOT`.**
   `compat/schema.go:185-219` (`Schema.Validate`) only checks `column.Type.Family == ""` (empty) and validates `vector`'s dimension argument — it never checks the family against the known set. The actual rejection ("`type family %q is not supported by %s`") lives in `compat/ddl.go:182` (`compileType`), reached only from `CompileDDL`, which in the CLI flow is invoked indirectly through `ImportSnapshot` — after both `OpenStore`/`Ping` calls have already succeeded against real source and destination stores. Reproduced with `family: "nope"` on a `compat-copy` config: `Schema.Validate()` passed silently; the run proceeded to (and failed at) the destination connect step, before ever reaching the type-check. In a real environment where both DBs are reachable, this same input would fail at `ERR_SNAPSHOT`, not `ERR_SCHEMA` as an agent reading `AGENTS.md:196`/`docs/USAGE.md:234` ("`ERR_SCHEMA` | `Schema.Validate` or `ApplySchema` fails") might reasonably expect for a schema-shaped mistake. Not a doc/code mismatch (the taxonomy is phase-based and technically self-consistent), but a real UX/fail-fast gap: a one-character typo in a `type.family` value is not caught until credentials for both live databases have already been exercised.

5. **The general error-envelope contract in `AGENTS.md` §8.1 ("prints ... in addition to any result JSON it already emits") does not hold for `compat-cutover`'s diverged case or `compat-copy`'s diverged case; only the more specific, correct per-CLI sections describe the real single-line behavior.**
   For `compat-cutover` diverged: `cmd/compat-cutover/main.go:182-198` emits exactly **one** JSON line — the `cutoverReport` with `code` embedded — never a second `{"status":"error",...}` envelope; `os.Exit(1)` is called directly, bypassing `cliout.EmitError`/`EmitErrorFrom` entirely. `AGENTS.md:200` and `docs/USAGE.md:238` describe this specific case correctly ("keeps its `diverged` result JSON and adds this code"), but that directly conflicts with the general rule stated a few paragraphs earlier (`AGENTS.md:181-187`, `docs/USAGE.md:219`) that every failure prints an envelope "in addition to" any result JSON. For `compat-copy` diverged: the opposite problem — only the bare error envelope is printed (`cmd/compat-copy/main.go:80-82`, `fail(cliout.ErrVerifyDiverged, err)`), no `VerificationReport` JSON at all (the digests are recoverable only as free text inside the `message` field, via `compat/verify.go:55-59`'s `fmt.Errorf("snapshot mismatch: source %s destination %s", ...)`). An agent that trusts the general §8.1 statement literally (expects two JSON lines on every failure, or expects the result JSON to always survive) will be wrong for both CLIs on this specific path, in opposite directions.

### BAJA

6. **No credential leakage found in the paths tested** (documented here as a negative result, not a defect, since the audit scope explicitly asked to check for it). `pgx`'s connection/parse errors redact the password (`postgres://user:xxxxxx@...`) even on malformed-DSN parse failures, and none of the three CLIs' `logf`/stderr/stdout paths print the raw DSN. Verified with a deliberately-leaked-looking password in both a live-connection-refused case and a malformed-URL parse-error case; neither surfaced the plaintext password.

7. `e2e/cutover_test.go:531-534`: the test `TestCutoverAuditCLIErrorCodeOnNotExact` (declared in `cutover_test.go`) actually drives `./cmd/compat-audit`, not `compat-cutover` — a stale/misleading name and comment inside the e2e reference file. Out of primary scope (e2e not audited line-by-line) but noted since it directly affects "which CLI does the doc's ERR_AUDIT_NOT_EXACT/findings claim describe" when cross-referencing tests against `AGENTS.md`.

## Doc ↔ code coherence table

| AGENTS.md / USAGE.md claim | Verified against | Result |
|---|---|---|
| Type family table (§1) | `compat/schema.go`, `compat/ddl.go` (spot-checked) | Matches |
| `vector` requires exactly one positive dimension | `compat/schema.go:207-213` | Matches |
| Function allowlist (§3) | `compat/sqlparse.go:100-134` | Matches exactly, including rejection message `unsupported catalog function %q` |
| `LIKE`→`ILIKE` on Postgres | `compat/ddl.go:287-294`, `compat/runtime.go:159` | Matches |
| Negative `LIMIT`/`OFFSET` rejected with exact message | `compat/sqlselect.go:89,102` | Matches |
| Non-`public` trigger schema rejected with exact message | `compat/sqltrigger.go:63` | Matches |
| `Version{0,0,0}` invalid | `compat/spec.go:23-33` | Matches |
| Contract requires source ≠ destination engine | `compat/spec.go:63-74` | Matches (undocumented in AGENTS.md but consistent, not contradictory) |
| `compat-audit` exit codes / findings-then-error order | `cmd/compat-audit/main.go` | Matches exactly (verified live, see below) |
| `compat-copy` exit codes / `VerificationReport` shape | `cmd/compat-copy/main.go` | Matches on success path; no findings ever surfacing on `ERR_AUDIT_NOT_EXACT` (finding 3); no report on `ERR_VERIFY_DIVERGED` (finding 5) |
| `compat-cutover` `--dry-run` plan shape (`status`,`audit`,`source_tables`,`destination_has_tables`,`phases`) | `cmd/compat-cutover/main.go:245-251` | Matches field-for-field |
| `ERR_USAGE` covers "unexpected flag" | `cmd/compat-cutover/main.go:82-91` | **False** (finding 1) |
| `contracts/migration.contract.example.md` step 1 (`compat-audit ./examples/contract.example.json` → 3 exact findings, exit 0) | Live run | Matches exactly |
| `schema_ref` as alternative to `schema` | `cmd/compat-cutover/main.go:28-34`, `cmd/compat-copy/main.go:15-20` | **Not implemented** (finding 2) |
| `examples/cutover.example.json`, `examples/migration.example.json` shapes vs `cutoverConfig`/`migrationConfig` | Live `--dry-run`/run against local Postgres (auth-only failure, i.e. correctly reached the driver) | Structurally executable as documented |
| `examples/contract.example.json` vs `compat.Contract` | Live run | Matches exactly |
| README/USAGE CLI table (exit codes, JSON shapes) | Live runs of all 3 CLIs, usage/config/audit-not-exact paths | Matches, except finding 1 |
| Credential handling in error output | Live runs with password-bearing DSNs (unreachable host, malformed URL) | No leak observed (finding 6, informational) |

## Trade-offs / design notes (not defects)

- The taxonomy is explicitly phase-based, not content-based (`AGENTS.md:203`): a schema-shaped mistake (finding 4) legitimately surfaces as `ERR_SNAPSHOT` rather than `ERR_SCHEMA` given that design, and the docs don't promise otherwise — flagged only because it means the "never silently degrade" ethos is honored at the `compat/` grammar layer but not at the CLI's own early-validation layer.
- `compat-copy` silently losing structured divergence data (finding 5) vs `compat-cutover` preserving it is a real product asymmetry, not merely cosmetic — worth a deliberate decision (either both preserve the report, or both drop it) rather than leaving it as an artifact of which code path happened to call `fail()` first.
- `--dry-run`'s read-only guarantee held under direct testing (traced: no `InstallChangeCapture`/`ImportSnapshot`/write call in `runDryRun`, `cmd/compat-cutover/main.go:205-255`); this claim is solid.

## Severity counts

- ALTA: 2
- MEDIA: 3
- BAJA: 2
