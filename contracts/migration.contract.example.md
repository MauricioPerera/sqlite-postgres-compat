# Migration contract (example)

A hybrid, human-plus-machine migration contract. The YAML frontmatter is the machine-readable claim; the prose sections are for the operator; the **Verdicts** section is the executable sequence an agent or CI runs, with the exact expected result of each command (exit code and JSON fields).

This example is a zero-window SQLite → PostgreSQL cutover of a single-table schema with a `vector` column. It is a template: replace engines, versions, DSNs, schema and features for your case. The grammar the schema must obey is specified in [`../AGENTS.md`](../AGENTS.md).

```yaml
---
source:
  engine: sqlite
  version: { major: 3, minor: 45, patch: 0 }
destination:
  engine: postgres
  version: { major: 17, minor: 10, patch: 0 }
# Point schema_ref at a JSON file holding the canonical `compat.Schema`, or
# inline the schema under `schema` below. The referenced file is the example
# shipped with the repo; replace it with your own. The path is resolved relative
# to the cutover.json config file, not the cwd. Exactly one of schema or
# schema_ref is required; both or neither is rejected with ERR_CONFIG.
schema_ref: examples/schema.example.json
# schema: inline        # alternative to schema_ref
required_features:
  - tables
  - primary_keys
  - canonical_foreign_keys
  - transactions
  - canonical_vectors
---
```

## Intent

Move an application's data from a SQLite source to a PostgreSQL 17.10 destination with proven exact equivalence, using a zero-window cutover so the application keeps serving reads/writes from SQLite until the moment of the switch. The canonical schema declares a `vector(N)` column, so the migration explicitly requires `canonical_vectors` and the destination must have the pgvector extension installed.

## Scope

- **In scope**: the tables, constraints, indexes, views, triggers and routines described by the referenced canonical schema; their rows; and the change stream captured during the cutover overlap window.
- **Out of scope**: DDL not expressible in the canonical grammar (see `../AGENTS.md` §7), arbitrary native SQL, ANN indexes, native vector distance functions, and full-text search ranking.
- **Preconditions**:
  - Destination PostgreSQL 17.10 is reachable and the connection can create/drop a temporary database (for verification) or the target database is empty (for `compat-copy`).
  - pgvector is installed on the destination (`CREATE EXTENSION IF NOT EXISTS vector`).
  - The application is the sole writer to the SQLite file during the cutover.

## Risks / Rollback (human)

- **Risk — overlap double-apply**: a row mutated after capture-install travels both inside the snapshot and in the journal. Mitigation: `compat-cutover` drains with `ApplyChangesTolerant`, which treats a change whose final state already matches the destination as already applied. Genuine divergence still raises a strict `ConflictError`.
- **Risk — echo during catch-up**: replicated rows being re-journalized by the destination's capture triggers. Mitigation: anti-echo suppression is transaction-local (Postgres GUC `compat.suppress`); it does not leak to other transactions under MVCC.
- **Risk — version collision**: `Version{0,0,0}` is invalid and rejected; two distinct zero-version sources would collide in the dedup table. Always set a real source version.
- **Rollback**: keep the SQLite file intact and do not cut the application DSN over until `compat-cutover` prints `status=ready`. If it prints `status=diverged` (exit 1), do not switch — investigate the digest mismatch, restore the destination from the pre-migration backup, and re-run. Cutting the DSN is manual and irreversible from the tool's perspective.

## Verdicts (executable)

Run these commands from the repo root. Each must produce the stated result; any deviation stops the migration.

### 1. Audit the contract

```bash
go run ./cmd/compat-audit ./examples/contract.example.json
```

- **Expected stdout**: a JSON array with one object per required feature, every `status` equal to `exact`:
  ```json
  [{"feature":"tables","status":"exact"},
   {"feature":"canonical_foreign_keys","status":"exact"},
   {"feature":"transactions","status":"exact"}]
  ```
- **Expected exit code**: `0`.
- **Failure**: exit `1` with a reason on stderr (some feature is not `exact`) or exit `2` (wrong argument count). Do not proceed.

### 2. Cutover (zero-window move)

Prepare a `cutover.json` with `source_dsn`, `destination_dsn`, `contract`, `schema` (or `schema_ref`) and optional `options`, then:

```bash
go run ./cmd/compat-cutover ./cutover.json
```

- **Expected stdout**: a `cutoverReport` JSON object:
  ```json
  {"status":"ready","source_digest":"<sha256>","destination_digest":"<sha256>","changes_applied":<N>}
  ```
  with `source_digest` equal to `destination_digest`.
- **Expected stderr**: progress lines, each prefixed with `compat-cutover: `: `compat-cutover: audit: ...`, `compat-cutover: capture: ...`, `compat-cutover: snapshot: ...`, `compat-cutover: catch-up: ...`.
- **Expected exit code**: `0`.
- **Failure**: `{"status":"diverged",...}` with exit `1` (digests differ), exit `1` with findings on stderr (a required feature not exact), or exit `2` (wrong argument count). Do not cut the DSN over.

### 3. Verify (digest equivalence)

`compat-cutover` verifies internally before printing `status=ready`. To re-verify independently, export both stores and compare:

- **Expected**: `compat.VerifySnapshots(source, destination)` returns `VerificationReport{Equivalent: true}` with equal `SourceDigest` and `DestinationDigest`.
- **Expected exit code**: `0` (the cutover command already encodes this).

### 4. Cut over the application DSN (manual, irreversible)

After `status=ready`, point the application at the PostgreSQL destination DSN. This step is not performed by any CLI in this repo.