# FIX-Q3 — ERR_USAGE covers unknown flags + real `schema_ref` + reject unknown config keys

Closes the two ALTA findings from `docs/reports/AUDIT2-C.md`:

- **BUG 1** (`AUDIT2-C.md` §1): `ERR_USAGE` was documented as covering "unexpected flag" but an unknown flag (`--bogus`) fell through to the positional config path and failed `ERR_CONFIG`/exit 1.
- **BUG 2** (`AUDIT2-C.md` §2): `schema_ref` was documented as a first-class alternative to inline `schema`, but no CLI parsed it — the key was silently dropped and the process ran with an empty schema.

Both fixed, plus a root-cause guard: every config JSON is now decoded with `json.Decoder.DisallowUnknownFields()`, so an unknown key is an explicit `ERR_CONFIG` instead of a silent drop (the "never silently degrade" promise, applied at the CLI's own validation layer).

Scope touched: `cmd/**` (3 CLIs + `cmd/internal/cliout`), `e2e/cutover_test.go` (tests added only), `AGENTS.md`, `docs/USAGE.md`, `contracts/migration.contract.example.md`, `examples/schema.example.json` (new). `compat/` untouched.

## 1. Implementation

### 1.1 Unknown flag → `ERR_USAGE` (exit 2), consistently across the 3 CLIs

New helper `cliout.SplitArgs(knownFlags, args)` (`cmd/internal/cliout/cliout.go`) partitions the arg list into recognized boolean flags and positionals. Any token beginning with `-` that is not in `knownFlags` returns `ok=false` with the offending token; the caller emits `ERR_USAGE` and exits 2. This makes the flag claim real instead of letting an unknown flag masquerade as the config path.

- `compat-audit` / `compat-copy`: `SplitArgs(nil, …)` — no recognized flags, so any `-…` is `ERR_USAGE`.
- `compat-cutover`: `SplitArgs([]string{"--dry-run"}, …)` — `--dry-run` is recognized; everything else starting with `-` is `ERR_USAGE`.

### 1.2 Real `schema_ref` support + reject unknown config keys

New helpers in `cmd/internal/cliout/cliout.go`:

- `DecodeFileStrict(path, v)` — reads + decodes with `DisallowUnknownFields`. Used by all three CLIs for their config (and `compat-audit` for the contract). An unknown key → explicit error → `ERR_CONFIG`.
- `ResolveSchema(configPath, ref, inline)` — enforces exactly one of inline `schema` (populated = at least one table) or `schema_ref` (non-empty). Both → `ERR_CONFIG`; neither → `ERR_CONFIG` (this is what stops the old "run with no schema" silent degradation); `schema_ref` set → `decodeSchemaRef`, which resolves the path **relative to the config file** (`filepath.Join(filepath.Dir(configPath), ref)` unless absolute) and decodes the referenced bare `compat.Schema` with `DisallowUnknownFields`. Unreadable / invalid-JSON `schema_ref` → `ERR_CONFIG`.

Both `cutoverConfig` (`cmd/compat-cutover/main.go`) and `migrationConfig` (`cmd/compat-copy/main.go`) gained a `SchemaRef string json:"schema_ref,omitempty"` field and call `ResolveSchema` right after decode. The resolved schema replaces `config.Schema` before `Schema.Validate`.

`compat-audit` has no schema, so it only gains strict decoding (no `schema_ref`).

### 1.3 Docs + contract example

- `AGENTS.md` §8 / §8.1: §8 intro now states any unrecognized `-…` flag is `ERR_USAGE` (exit 2); `ERR_CONFIG` row documents `DisallowUnknownFields` + the `schema`/`schema_ref` constraint; the `compat-copy` and `compat-cutover` config lines now read `{…, schema | schema_ref, …}` with the exactly-one rule and relative-to-config resolution.
- `docs/USAGE.md`: same `ERR_USAGE`/`ERR_CONFIG` table updates (Spanish); a new `schema_ref` block in the cutover section with a JSON example and the exactly-one rule; a one-line `schema_ref` mention in the compat-copy section.
- `contracts/migration.contract.example.md`: `schema_ref` now points at `examples/schema.example.json` (a real bare-schema file) instead of `examples/migration.example.json` (a full config, which would have failed `DisallowUnknownFields` when decoded as a bare `Schema`); comment explains relative-to-config resolution + the exactly-one rule.
- `examples/schema.example.json` (new): a bare `compat.Schema` object (`entries` table, `id`+`title`, PK) — the file the contract example references.

## 2. Definition of done — evidence

### 2.1 New e2e tests (all PASS)

Added to `e2e/cutover_test.go`:

| Test | Asserts |
|---|---|
| `TestCLIRejectsUnknownFlagAsUsage` | (a) `compat-cutover --bogus x.json`, `compat-copy --bogus x.json`, `compat-audit --bogus x.json` → stdout JSON `code=ERR_USAGE`, **exit 2** (subtests `cutover`/`copy`/`audit`). |
| `TestCutoverRejectsUnknownConfigKey` | (b) config with an unknown top-level key (`bogus_key`) → `ERR_CONFIG`, exit 1 (decode fails before any store is opened). |
| `TestCutoverRejectsSchemaAndSchemaRef` | (c) config with both `schema` and `schema_ref` → `ERR_CONFIG`, exit 1. |
| `TestCutoverRejectsMissingSchemaAndSchemaRef` | neither `schema` nor `schema_ref` → `ERR_CONFIG`, exit 1 (the old silent-empty-schema path, now explicit). |
| `TestCutoverWithSchemaRefSucceeds` | (d) full cutover with `schema_ref` (path relative to the config file) against real PostgreSQL → `status=ready`, matching non-empty digests, 2 migrated rows verified in PG. |

Note on exit-code testing: the existing `runCLI` helper drives `go run`, which on Windows collapses every non-zero subprocess exit to 1 and so cannot reveal the exit-2 path. The new `runBuiltCLI` helper (`e2e/cutover_test.go`) builds each CLI once into an OS temp dir and execs the binary directly, yielding the real process exit code. The 3 binaries are cached across tests via `sync.Map`.

### 2.2 Full e2e suite (`go test -tags=e2e ./e2e -count=1 -timeout 600s`, `COMPAT_POSTGRES_DSN` set)

```
--- FAIL: TestSystemClaimsExactCoverageForRequiredFeatureFamilies (0.00s)
FAIL    example.com/sqlite-postgres-compat/e2e    55.339s
FAIL
```

Top-level tally (`-v`, `--- PASS/FAIL: Test…` lines):

```
      1 FAIL:
     32 PASS:
```

**32 PASS + 1 FAIL.** The single FAIL is the documented intentional one (`TestSystemClaimsExactCoverageForRequiredFeatureFamilies` — generic families `foreign_keys`/`check_constraints`/`indexes`/`triggers`/`views`/`stored_routines`/`full_text` remain `unknown`, as established in `docs/reports/VALIDATION_REPORT.md:65` and prior fix reports). Baseline was 27 PASS + 1 intentional FAIL (28 top-level); the 5 new top-level tests added here take it to 32 PASS + 1 intentional FAIL (33 top-level). No new FAIL.

### 2.3 `go build ./... && go vet ./... && go test ./...`

```
$ go build ./...   → BUILD OK (exit 0)
$ go vet ./...     → VET OK (exit 0)
$ go test ./...
?   example.com/sqlite-postgres-compat/cmd/compat-audit        [no test files]
?   example.com/sqlite-postgres-compat/cmd/compat-copy         [no test files]
?   example.com/sqlite-postgres-compat/cmd/compat-cutover     [no test files]
?   example.com/sqlite-postgres-compat/cmd/internal/cliout    [no test files]
ok  example.com/sqlite-postgres-compat/compat (cached)
```

All green.

### 2.4 `go run ./cmd/compat-audit ./examples/contract.example.json` still exit 0

Protects against `DisallowUnknownFields` breaking the shipped examples:

```
$ go run ./cmd/compat-audit ./examples/contract.example.json
[{"feature":"tables","status":"exact"},{"feature":"canonical_foreign_keys","status":"exact"},{"feature":"transactions","status":"exact"}]
exit=0
```

Also smoke-checked live (real subprocess stdout, captured before this report):

```
$ go run ./cmd/compat-cutover --bogus ./examples/cutover.example.json
{"status":"error","code":"ERR_USAGE","message":"compat-cutover: unexpected flag \"--bogus\""}
(exit 2 via the built binary — `go run` itself reports "exit status 2" but returns 1 to the shell on Windows; the e2e test uses the built binary to assert the true exit 2.)
```

### 2.5 `grep -n "schema_ref" docs/USAGE.md AGENTS.md contracts/migration.contract.example.md cmd/`

Doc and code aligned (full output):

```
docs/USAGE.md:52:... podés usar `schema_ref` (ruta a un JSON con el `compat.Schema` canónico, resuelta relativa al archivo de config); debe haber exactamente uno de `schema` o `schema_ref`, si no `ERR_CONFIG` ...
docs/USAGE.md:193:En lugar de inlinear el `schema`, podés apuntar `schema_ref` a un archivo JSON que contenga el objeto `compat.Schema` canónico ... resuelta **relativa al archivo de config**, no al cwd:
docs/USAGE.md:200:  "schema_ref": "schema.json",
docs/USAGE.md:205:Debe haber **exactamente uno** de `schema` o `schema_ref`: ambos o ninguno es `ERR_CONFIG`. Un archivo `schema_ref` ilegible o con JSON inválido también es `ERR_CONFIG`. `compat-copy` soporta `schema_ref` igual que `compat-cutover`.
docs/USAGE.md:244:| `ERR_CONFIG` | ... `json.Decoder.DisallowUnknownFields`, así que una key desconocida es un error explícito ... una violación de `schema`/`schema_ref` ... también es `ERR_CONFIG`. | `1` |
AGENTS.md:192:| `ERR_CONFIG` | ... `json.Decoder.DisallowUnknownFields`, so an unknown key is an explicit error ...; a `schema`/`schema_ref` violation (both, neither, or an unreadable/invalid `schema_ref` file) is also `ERR_CONFIG`. | `1` |
AGENTS.md:221:Config: `{source_dsn, destination_dsn, contract, schema | schema_ref}`. Exactly one of `schema` ... or `schema_ref` (path to a JSON file holding a bare `compat.Schema` object, resolved **relative to the config file**, not the cwd) is required; both or neither is `ERR_CONFIG`, and an unreadable or JSON-invalid `schema_ref` file is `ERR_CONFIG`. ...
AGENTS.md:233:Config: `{source_dsn, destination_dsn, contract, schema | schema_ref, options}`. Exactly one of `schema` ... or `schema_ref` ... is required; both or neither is `ERR_CONFIG`, ...
contracts/migration.contract.example.md:15:# Point schema_ref at a JSON file holding the canonical `compat.Schema`, or
contracts/migration.contract.example.md:19:# schema_ref is required; both or neither is rejected with ERR_CONFIG.
contracts/migration.contract.example.md:20:schema_ref: examples/schema.example.json
contracts/migration.contract.example.md:21:# schema: inline        # alternative to schema_ref
contracts/migration.contract.example.md:72:Prepare a `cutover.json` with `source_dsn`, `destination_dsn`, `contract`, `schema` (or `schema_ref`) and optional `options`, then:
cmd/compat-copy/main.go:19:	SchemaRef      string          `json:"schema_ref,omitempty"`
cmd/compat-cutover/main.go:33:	SchemaRef      string          `json:"schema_ref,omitempty"`
cmd/internal/cliout/cliout.go:156:// file's location, not the process cwd, so a config and its schema_ref travel
cmd/internal/cliout/cliout.go:165:		return compat.Schema{}, fmt.Errorf("schema_ref %q: %w", ref, err)
cmd/internal/cliout/cliout.go:171:// exactly one of the inline `schema` field or a `schema_ref` path. ...
cmd/internal/cliout/cliout.go:186:		return compat.Schema{}, errors.New("config must specify exactly one of schema or schema_ref, not both")
cmd/internal/cliout/cliout.go:188:		return compat.Schema{}, errors.New("config must specify exactly one of schema or schema_ref")
```

## 3. Notes / trade-offs

- `--dry-run` is now matched anywhere in the arg list (previously only as `args[0]`). This is a superset of the prior "antes del JSON posicional" behavior; all existing tests pass `--dry-run` before the config, so no behavior relied on positional-only matching. Documented as "an optional `--dry-run` flag".
- `ResolveSchema` treats an inline schema with zero tables as "absent" — this is deliberate: it is what makes a config that omits both `schema` and `schema_ref` fail fast with `ERR_CONFIG` instead of running with an empty schema (the exact silent-degradation bug from AUDIT2-C §2).
- The DSN responded persistently across the run (TestMain creates/drops the e2e database; all PG-touching tests passed). No password logged.
- No process was left running; no files outside the repo were touched.