# AGENTS.md — Canonical grammar for portable SQLite ↔ PostgreSQL schemas

This document is the **machine-facing specification** an LLM/agent must respect when generating schemas or SQL that has to round-trip through `sqlite-postgres-compat`. Everything here is transcribed from the real parser/compilers in `compat/` (the code is the source of truth). If you generate something outside this grammar, the layer rejects it with an explicit error rather than silently degrading.

A worked, executable migration contract lives at [`contracts/migration.contract.example.md`](contracts/migration.contract.example.md).

## 1. Type families

Canonical types are `Type{Family, Arguments}` (`compat/schema.go`). Families:

| Family | SQLite physical | PostgreSQL physical | Arguments |
|---|---|---|---|
| `boolean` | `INTEGER` | `BOOLEAN` | — |
| `integer` | `INTEGER` | `BIGINT` | — |
| `decimal` | `TEXT` (lossless) | `NUMERIC(p,s)` or `NUMERIC` | up to 2: `(p,s)` |
| `float` | `REAL` | `DOUBLE PRECISION` | — |
| `text` | `TEXT` | `TEXT` | — |
| `binary` | `BLOB` | `BYTEA` | — |
| `date` | `TEXT` | `DATE` | — |
| `timestamp` | `TEXT` (RFC3339Nano) | `TEXT` (RFC3339Nano) | — |
| `json` | `TEXT` | `TEXT` | — |
| `uuid` | `TEXT` | `TEXT` | — |
| `vector` | `TEXT` (carrier `'[1,2,3]'`) | `vector(N)` (requires pgvector) | exactly 1, positive: `(N)` |

Rules (from `Schema.Validate` and `compileType`):

- `decimal` accepts at most two arguments `(precision, scale)`.
- `vector` requires **exactly one positive argument** (the dimension); otherwise validation fails. PostgreSQL emits `vector(N)` and requires the pgvector extension in the destination; if absent, `CREATE TABLE` fails with an engine error (accepted: the capability is declared explicitly, not silently degraded to text).
- Any other family is rejected by `compileType` with `type family %q is not supported by %s`.

A vector column infers the `canonical_vectors` feature (see `InferFeatures`).

## 2. Constraint forms

`Constraint{Kind, Columns, References, Expression}` (`compat/schema.go`, compiled in `compat/ddl.go`):

- `primary_key` — `PRIMARY KEY (col, ...)`. Simple and composite.
- `unique` — `UNIQUE (col, ...)`. Simple and composite.
- `foreign_key` — `FOREIGN KEY (cols) REFERENCES table (cols) [ON UPDATE action] [ON DELETE action]`. `References` is required. Composite keys supported. Referential actions: `no_action` (default, omitted), `restrict`, `cascade`, `set_null`, `set_default`.
- `check` — `CHECK (expression)`. `Expression` is required and must be in the expression grammar below.

A constraint needs columns unless it is `check`.

## 3. Expression grammar

Parser: `compat/sqlparse.go` (`parseCatalogExpression`). Operator precedence, **loosest to tightest** (transcribed from the `levels` table):

1. `OR`
2. `AND`
3. `NOT` — handled between `AND` and the `IS NULL`/comparison levels, so `NOT a = b` parses as `not(eq(a, b))`, not `eq(not(a), b)`.
4. `IS NULL` / `IS NOT NULL`
5. comparisons: `<=`, `>=`, `<>`, `!=`, `=`, `<`, `>`, `LIKE`
6. `||` (concatenation)
7. `+`, `-`
8. `*`, `/` (tightest)

Same-precedence binary operators associate to the left. The infix form `a NOT LIKE b` is folded into `not(like(a, b))`, matching the prefix form `NOT a LIKE b`.

### Literals

- Strings: `'...'` with `''` as the embedded-quote escape.
- Booleans: `TRUE`, `FALSE`.
- `NULL`.
- `CURRENT_TIMESTAMP`.
- Numbers: integer (`123`) or decimal (`1.5`, `1e3`) — a token containing `.`, `e` or `E` is decimal.
- Hexadecimal: SQLite `0x10` / `0XABCDEF`, recognized and converted to its decimal value (so it compiles to a plain integer, not a quoted identifier). Out-of-64-bit-range hex is rejected.
- Identifiers: dotted (`a.b`) and double-quoted (`"weird name"`, with `""` as the embedded-quote escape).

### Function allowlist (exact)

Recognized by `parseCatalogExpression` and compiled by `compileExpression`:

| Function | Arity | Notes |
|---|---|---|
| `count`, `sum`, `avg`, `min`, `max` | `*` or one expression | aggregates; `count(*)` accepted |
| `lower`, `upper` | one expression | |
| `length`, `abs`, `trim` | one expression | |
| `coalesce` | at least one argument | variadic |
| `replace` | exactly three arguments | |

Every other function name is rejected with `unsupported catalog function %q`.

### LIKE → ILIKE

SQLite's `LIKE` is case-insensitive (ASCII) by default; PostgreSQL's `LIKE` is case-sensitive. To preserve SQLite semantics, `like` compiles to `LIKE` on SQLite and **`ILIKE` on PostgreSQL**. This is the standard pragmatic mapping and is accepted as a known trade-off (`ILIKE` folds the full Unicode range; SQLite only folds ASCII).

## 4. SELECT grammar

Parser: `compat/sqlselect.go` (`parseCatalogSelect`). A view definition is `CREATE VIEW name AS <select>`; the parser strips the `CREATE ... AS SELECT ` prefix and requires a `SELECT`.

```
SELECT [DISTINCT] projection, ... 
FROM table[[ AS] alias]
[ {LEFT [OUTER] | INNER | CROSS | } JOIN table[[ AS] alias] [ON expr] ]...
[WHERE expr]
[GROUP BY expr, ...]
[HAVING expr]
[ORDER BY expr [ASC|DESC], ...]
[LIMIT n]
[OFFSET n]
```

- **Projections**: comma-separated. Each is `expr` or `expr AS alias`; the alias is a simple identifier (no dot). `SELECT *` / a bare `*` projection is **rejected** (`unsupported catalog expression`).
- **FROM**: a single table source `table`, `table alias`, or `table AS alias`. The table name must not be schema-qualified (no `.`). Compound sources (comma, parentheses) are rejected with `compound table source is outside the canonical grammar`.
- **JOINs**: `LEFT [OUTER]`, `INNER`, `CROSS`, and bare `JOIN` (treated as `INNER`). `LEFT`/`INNER` require `ON <expr>`; `CROSS` takes no `ON`. Multiple joins chain.
- **WHERE / HAVING**: a catalog expression (Section 3).
- **GROUP BY**: comma-separated expressions.
- **ORDER BY**: comma-separated; each item is `expr [ASC|DESC]`.
- **LIMIT / OFFSET**: non-negative integers. A **negative** value is rejected at parse time with `unsupported negative LIMIT %d` / `unsupported negative OFFSET %d`.

Always rejected in SELECT: subqueries, set operations (`UNION`/`INTERSECT`/`EXCEPT`), CTEs (`WITH`), window functions, `SELECT *`, compound table sources, and schema-qualified table names.

## 5. Triggers (canonical forms)

Parser: `compat/sqltrigger.go`.

**SQLite**:
```
CREATE TRIGGER name (BEFORE|AFTER) (INSERT|UPDATE|DELETE) ON table
  [FOR EACH ROW] [WHEN expr]
  BEGIN <action>; <action>; ... END
```

**PostgreSQL** (inspected form):
```
CREATE TRIGGER name (BEFORE|AFTER) (INSERT|UPDATE|DELETE) ON [schema.]table
  FOR EACH ROW [WHEN (expr)] EXECUTE FUNCTION fn(...)
```
The trigger function body must contain `RETURN`; the text before `RETURN` is parsed as the action list. The optional schema qualifier must be empty or `public` — **any other schema is rejected** with `unsupported trigger schema %q: only "public" is allowed`.

**Actions** (statements separated by `;`, at least one required):

- `INSERT INTO table (col, ...) VALUES (val, ...)` — column and value counts must match; assignment columns are simple (no `.`).
- `UPDATE table SET col = val, ... WHERE expr` — `WHERE` is required.
- `DELETE FROM table WHERE expr` — `WHERE` is required.

`WHEN` and action `WHERE`/values use the expression grammar (Section 3). `NEW`/`OLD` are recognized as column qualifiers. Any action outside these three forms is rejected with `trigger action %q is outside the canonical grammar`.

## 6. Routines (canonical forms)

Parser: `compat/sqlroutine.go`; runtime: `compat/runtime.go` (`CallRoutine`).

External PostgreSQL routines are translatable only when:

- `kind = 'p'` (procedure) **or** the result type is `void`. Functions with a non-void return are placed in `Inspection.Unresolved` and block `Inspection.Exact`.
- `language` is `plpgsql` or `sql`; anything else is rejected.
- Parameters are `name type` or `IN name type`, with **no `DEFAULT` and no `=`**. Parameter types map via `canonicalPostgresRoutineType`: `boolean`; `smallint`/`integer`/`bigint`→`integer`; `numeric`/`decimal`→`decimal`; `real`/`double precision`→`float`; `text`/`character varying`/`character`→`text`; `bytea`→`binary`; `date`; `timestamp with/without time zone`→`timestamp`; `json`/`jsonb`→`json`; `uuid`.

The body is `BEGIN ... END` (optional wrapper) parsed as the same action list as triggers, then rewritten for the runtime:

- **INSERT** assignment values: a bare identifier must name a declared parameter, else `routine expression %q is not a declared parameter`.
- **UPDATE**: requires assignments **and** a `WHERE`.
- **DELETE**: requires a `WHERE` and **no** assignments.
- **WHERE** grammar is restricted to comparisons (`=`, `<>`, `<`, `<=`, `>`, `>=`, `LIKE`) composed with `AND`/`OR`/`NOT`, plus `IS NULL`/`IS NOT NULL`, against columns, parameters and literals. A bare identifier naming a parameter becomes a `parameter` node; any other bare identifier stays a `column` node. Qualified `table.column` identifiers in a routine `WHERE` are rejected. Any construct outside this subset is rejected with `routine WHERE expression %q is outside the canonical grammar`.

`LIKE` in a routine `WHERE` compiles to `ILIKE` on PostgreSQL (same mapping as Section 3).

## 7. Always rejected (explicit errors)

The layer never silently degrades. These are rejected with explicit errors:

- Negative `LIMIT` / `OFFSET`.
- Non-`public` schema qualifier on a PostgreSQL trigger.
- `SELECT *` or a bare `*` projection.
- Compound or schema-qualified table sources in `FROM`.
- Subqueries, set operations, CTEs, window functions.
- Any function not in the allowlist (Section 3).
- Any expression outside the grammar (Section 3).
- Unsupported PostgreSQL `DEFAULT` casts (only a known set is accepted: `text`, `character varying`, `character`, `boolean`, `smallint`, `integer`, `bigint`, `numeric`, `real`, `double precision`, `date`, `timestamp without time zone`, `timestamp with time zone`, `uuid`, `json`, `jsonb`).
- Routines with a non-void return, languages other than `plpgsql`/`sql`, or parameters with `DEFAULT`/`=`.
- `vector` without exactly one positive dimension argument.
- `Version{0,0,0}` as a source/destination version (invalid dedup key).
- Trigger/routine actions outside the three canonical forms.

## 8. CLIs

All CLIs take exactly one JSON config argument. Exit codes: `0` success; `1` any error or non-exact/non-equivalent result; `2` wrong argument count.

### `compat-audit <contract.json>`

Audits a `Contract` (`{source, destination, required_features}`) and prints one `Finding` per required feature as a JSON array on stdout.

```json
[{"feature":"tables","status":"exact"},
 {"feature":"canonical_foreign_keys","status":"exact"},
 {"feature":"transactions","status":"exact"}]
```

- Exit `0`: every required feature is `exact`.
- Exit `1`: any feature is not `exact` (the failing reason is also printed to stderr), or any read/parse/audit error.
- Exit `2`: argument count is not 1.

### `compat-copy <migration.json>`

Config: `{source_dsn, destination_dsn, contract, schema}`. Infers features from the schema (`InferFeatures`), audits, requires exact, then exports the source snapshot, imports it into the destination, re-exports the destination, and verifies digests. Prints a `VerificationReport` on stdout:

```json
{"source_digest":"...","destination_digest":"...","equivalent":true}
```

- Exit `0`: `equivalent == true`.
- Exit `1`: any error or `equivalent == false`.
- Exit `2`: wrong argument count. The destination must be empty for the described objects (import is additive).

### `compat-cutover <cutover.json>`

Config: `{source_dsn, destination_dsn, contract, schema, options}`. `options` is optional with defaults `poll_interval_ms=1000`, `drain_polls=3`, `batch_limit=500`. Orchestrates a zero-window SQLite → PostgreSQL cutover: audit → install change capture on source → export+import snapshot → drain the journal with `ApplyChangesTolerant` until `drain_polls` consecutive empty reads → verify digests. Prints a `cutoverReport` on stdout and progress lines on stderr:

```json
{"status":"ready","source_digest":"...","destination_digest":"...","changes_applied":N}
```

- Exit `0`: digests match → `status=ready`.
- Exit `1`: `status=diverged` (digests differ), a required feature is not exact (findings also printed to stderr), or any error.
- Exit `2`: wrong argument count.
- Cutting the application's DSN over to the destination is **manual** and is not this tool's responsibility; do it after `status=ready`.

## 9. Migration flow (phases and verdicts)

A migration is a sequence of auditable verdicts. Each phase produces a machine-checkable result.

1. **Audit** — `compat-audit contract.json` (or the audit step inside `compat-copy`/`compat-cutover`, which appends `InferFeatures(schema)` to `required_features`).
   - Verdict: every finding `status=exact` (exit `0`). Any `unknown`/`unsupported`/`transformed`/`emulated` stops the migration (exit `1`).
2. **Move** — choose one:
   - Offline snapshot copy: `compat-copy migration.json`.
   - Zero-window cutover: `compat-cutover cutover.json`.
   - Verdict (copy): `VerificationReport.equivalent == true` (exit `0`). Verdict (cutover): `cutoverReport.status == "ready"` (exit `0`).
3. **Verify** — `compat-copy`/`compat-cutover` verify internally (digest comparison via `VerifySnapshots`). Programmatic callers can re-verify at any time with `compat.VerifySnapshots(source, destination)`, expecting `Equivalent == true` and equal `SourceDigest`/`DestinationDigest`.

A canonical schema that contains a `vector` column infers `canonical_vectors`; one with views/triggers/routines/indexes infers the corresponding `canonical_*` features. Generic families (`foreign_keys`, `check_constraints`, `indexes`, `views`, `triggers`, `stored_routines`, `full_text`) remain `unknown` because they represent arbitrary native SQL and are not audited as exact.

See [`contracts/migration.contract.example.md`](contracts/migration.contract.example.md) for a full executable example.