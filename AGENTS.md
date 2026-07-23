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
| `date` | `TEXT` | `TEXT` | — |
| `timestamp` | `TEXT` (RFC3339Nano) | `TEXT` (RFC3339Nano) | — |
| `json` | `TEXT` | `TEXT` | — |
| `uuid` | `TEXT` | `TEXT` | — |
| `vector` | `TEXT` (carrier `'[1,2,3]'`) | `vector(N)` (requires pgvector) | exactly 1, positive: `(N)` |

Rules (from `Schema.Validate` and `compileType`):

- `decimal` accepts at most two arguments `(precision, scale)`.
- `vector` requires **exactly one positive argument** (the dimension); otherwise validation fails. PostgreSQL emits `vector(N)` and requires the pgvector extension in the destination; if absent, `CREATE TABLE` fails with an engine error (accepted: the capability is declared explicitly, not silently degraded to text).
- Any other family is rejected by `compileType` with `type family %q is not supported by %s`.

A vector column infers the `canonical_vectors` feature (see `InferFeatures`).

### Generated columns (STORED)

A `Column` may carry an additive `Generated` field (`*GeneratedColumn{Expression, Stored}`, JSON `generated` with `omitempty`), compiled by `compileTable` (`compat/ddl.go`) to `col TYPE GENERATED ALWAYS AS (<expr>) STORED` — byte-identical syntax in SQLite (≥ 3.31) and PostgreSQL (≥ 12). `Expression` is a catalog expression (Section 3). The destination recomputes the value deterministically, which is the equivalence proof; the data chain never writes a generated column (it is excluded from every `INSERT`/`UPDATE` column list in snapshot import and replication).

Rules (from `Schema.Validate`; both engines enforce them):

- `Stored` must be `true`. **`VIRTUAL` is rejected** — PostgreSQL cannot express it — so a `Stored=false` generated column fails validation and is never emitted.
- A generated column must not also have a `Default`.
- A generated column must not be part of a `primary_key`.

Native inspection reconstructs a STORED generated column as `exact` (SQLite `pragma_table_xinfo` hidden=3, expression recovered from the `CREATE TABLE` text; PostgreSQL `is_generated='ALWAYS'`, expression from `generation_expression`). A `VIRTUAL` (SQLite hidden=2), identity, or out-of-grammar generation expression column is placed in `Inspection.Unresolved` and blocks `Inspection.Exact`.

### Domains (asymmetric)

A `Schema` may carry an additive `omitempty` `Domains []Domain` (`compat/schema.go`). A `Domain{Name, Type /* base family */, Check *Expression, NotNull bool, Default *Expression}` is a named base type plus an optional CHECK, NOT NULL and DEFAULT. A column references a domain through an additive `omitempty` `Column.DomainRef string` (JSON `domain`); a column without it stays byte-identical. The CHECK refers to the value under test with the placeholder node `Expression{Kind:"domain_value"}` (Section 3 grammar otherwise).

A domain-referencing column **must still carry `Type` equal to the domain's base type** and be otherwise neutral — `Nullable=true`, no own `Default`, no `Generated` — so the domain is the single source of the constraint (enforced by `Schema.Validate`). The redundant `Type` keeps the data chain (export/import canonicalization keys off `Column.Type.Family`) unchanged and keeps PostgreSQL (native) and SQLite (inlined) storing identical values; the mechanism is the least invasive one that does not alter the byte-identical behavior of columns without domains.

Compilation (`compat/ddl.go`) is **asymmetric but semantically equivalent**:

- **PostgreSQL** emits a native `CREATE DOMAIN "name" AS <base type> [DEFAULT ...] [NOT NULL] [CHECK (...)]` before the tables; the referencing column's SQL type is the domain name. The CHECK's `domain_value` compiles to the `VALUE` keyword.
- **SQLite has no domains**, so it emits no `CREATE DOMAIN`; each referencing column is inlined as `<base type> [NOT NULL] [DEFAULT ...] [CHECK (...)]`, with the CHECK's `domain_value` resolved to the column name. The base type differs per engine (e.g. `INTEGER`/`BIGINT`) but the enforced constraint and the stored data are identical.

Grammar validity of the domain CHECK is enforced at compile time by the same `compileExpression` path as generated columns and CHECK constraints, so an out-of-grammar CHECK is rejected by `CompileDDL`. `Schema.Validate` rejects a duplicate/unnamed/untyped domain and a column referencing an unknown domain, whose type does not match the domain base type, or that is not neutral.

**Asymmetry (honest).** The canonical path (schema created by this layer, kept in `__compat_schema`) round-trips the domain **exactly on both engines** — this is the primary proof. External inspection is different: a domain never physically existed in SQLite, so an inlined column is reconstructed as an ordinary column + CHECK (exact **as a column**, not as a domain — this is not a data divergence, only a shape difference, and is accepted). External **PostgreSQL** inspection **does not rebuild the domain** either: PostgreSQL deparses the domain CHECK with the `VALUE` keyword (and adds `::type` casts) into a form outside the canonical grammar, so a domain-typed column is placed in `Inspection.Unresolved` (`kind=domain_column`, `Exact=false`) rather than silently degraded to its underlying scalar type.

## 2. Constraint forms

`Constraint{Kind, Columns, References, Expression}` (`compat/schema.go`, compiled in `compat/ddl.go`):

- `primary_key` — `PRIMARY KEY (col, ...)`. Simple and composite.
- `unique` — `UNIQUE (col, ...)`. Simple and composite.
- `foreign_key` — `FOREIGN KEY (cols) REFERENCES table (cols) [ON UPDATE action] [ON DELETE action]`. `References` is required. Composite keys supported. Referential actions: `no_action` (default, omitted), `restrict`, `cascade`, `set_null`, `set_default`.
- `check` — `CHECK (expression)`. `Expression` is required and must be in the expression grammar below.

A constraint needs columns unless it is `check`.

### Indexes

`Index{Name, Table, Unique, Columns []IndexColumn, Where *Expression}` (`compat/schema.go`, compiled by `compileIndex` in `compat/ddl.go`) emits `CREATE [UNIQUE] INDEX name ON table (key [ASC|DESC], ...) [WHERE expr]`. The optional partial `Where` predicate uses the expression grammar (Section 3).

`IndexColumn{Column, Descending, Expression}` is one key:

- A **plain-column key** sets `Column`; it compiles to `quoteIdentifier(Column) [ASC|DESC]`. This path is byte-identical to before expression indexes existed.
- An **expression key** sets `Expression` (a Section 3 catalog expression) and leaves `Column` empty; it compiles to `(<compiled expr>) [ASC|DESC]` — the parenthesized key form both SQLite (≥ 3.9) and PostgreSQL require. Function keys (`lower(email)`), operator keys (`a + b`), `UNIQUE` expression indexes, and a mix of column and expression keys in one index are all supported. Grammar validity is enforced at compile time (`compileExpression`), so an expression outside Section 3 is rejected with a clear error; a key that sets both `Column` and `Expression` is rejected by `Schema.Validate`.

Native inspection reconstructs an expression index from external databases: in SQLite the key column name is NULL in `pragma_index_xinfo`, so the expression text is recovered from the `CREATE INDEX` SQL in `sqlite_master` and parsed with `parseCatalogExpression`; in PostgreSQL it comes from `pg_get_indexdef` per key column. **Honesty clause:** when an engine deparses the key into a form outside the canonical grammar the index is placed in `Inspection.Unresolved` (`Exact = false`) with a reason, never reconstructed as a wrong AST. In particular PostgreSQL deparses a bare string literal with an added `::type` cast (e.g. `coalesce(email, 'x')` → `COALESCE(email, 'x'::text)`), which is outside the grammar and lands in `Unresolved`, while SQLite stores the `CREATE INDEX` text verbatim and reconstructs the same index exact. The canonical path (a schema created by this layer, stored in `__compat_schema`) always round-trips exact; the limitation is only for external inspection of expressions the engine rewrites.

## 3. Expression grammar

Parser: `compat/sqlparse.go` (`parseCatalogExpression`). Operator precedence, **loosest to tightest** (transcribed from the `levels` table):

1. `OR`
2. `AND`
3. `NOT` — handled between `AND` and the `IS NULL`/comparison levels, so `NOT a = b` parses as `not(eq(a, b))`, not `eq(not(a), b)`.
4. `IS NULL` / `IS NOT NULL`
5. comparisons: `<=`, `>=`, `<>`, `!=`, `=`, `<`, `>`, `LIKE`; and the `BETWEEN` / `IN` predicates (same precedence as comparisons)
6. `||` (concatenation)
7. `+`, `-`
8. `*`, `/` (tightest)

Same-precedence binary operators associate to the left. The infix form `a NOT LIKE b` is folded into `not(like(a, b))`, matching the prefix form `NOT a LIKE b`.

### Predicates and conditional expressions

| Construct | AST kind | Args layout | Compiles to (both engines) |
|---|---|---|---|
| `a BETWEEN x AND y` | `between` | `[a, x, y]` | `(a BETWEEN x AND y)` |
| `a NOT BETWEEN x AND y` | `not_between` | `[a, x, y]` | `(a NOT BETWEEN x AND y)` |
| `a IN (v1, v2, …)` | `in` | `[a, v1, v2, …]` (≥1 value) | `(a IN (v1, v2, …))` |
| `a NOT IN (v1, …)` | `not_in` | `[a, v1, …]` (≥1 value) | `(a NOT IN (v1, …))` |
| `CASE WHEN c THEN v … [ELSE e] END` | `case` | `[c1, v1, c2, v2, …, (e)]` | `CASE WHEN c THEN v … [ELSE e] END` |

- `BETWEEN` is inclusive (`x <= a <= y`); the delimiting `AND` is part of the predicate, not a logical `AND`. `NOT BETWEEN` negates it.
- `IN`/`NOT IN` take an explicit **value list only** — subqueries are out of scope and rejected. Empty lists and non-value elements are rejected. Membership (including three-valued logic on `NULL`) is identical in both engines.
- `CASE` is the **searched** form only. Args are `WHEN`/`THEN` pairs; an **odd** Args length means the trailing element is the `ELSE` value, an **even** length means there is no `ELSE`. The simple/operand form `CASE x WHEN v …` is rejected.

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
| `nullif` | exactly two arguments | returns `NULL` when the arguments are equal |
| `replace` | exactly three arguments | |
| `gen_random_uuid` | exactly zero arguments (empty parentheses) | **non-deterministic**; see note below |

Every other function name is rejected with `unsupported catalog function %q`. In particular `round`, `substr`/`substring` and `cast` are **deliberately excluded**: they are not byte-identical between SQLite and PostgreSQL (round: half-to-even vs half-away-from-zero on doubles; substr: negative index/length; cast to integer: round vs truncate). `IS DISTINCT FROM` is deferred (needs SQLite ≥ 3.39 version gating). See `docs/reports/FEAT-CUBOA-1-REPORT.md`.

**`gen_random_uuid()` — the one non-deterministic node.** It generates a fresh random RFC 4122 v4 UUID on every evaluation. It compiles to PostgreSQL's native core built-in `gen_random_uuid()` (available since PG13; the project requires PG17, so no extension and no version gating) and, on SQLite (which has no core random-UUID function), to an inline expression over `randomblob`/`hex` that assembles a valid v4 UUID (8-4-4-4-12 layout, version nibble `4`, variant nibble in `{8,9,a,b}`), parenthesized so it is valid as a column `DEFAULT (expr)`. It is **not** a literal translation of the SQLite idiom `hex(randomblob(16))` (rejected — `hex` is not in the allowlist and PostgreSQL core has no equivalent random-bytes generator); it is a new canonical function of equivalent purpose. Because a random source cannot produce the same value on both engines, this node is the sole exception to the byte-identical rule: its equivalence proof is that (a) both engines compile and execute it without error, (b) every generated value is a valid v4 UUID, and (c) successive calls differ — not value equality. It may be used in a column `DEFAULT` (typically on a `family=uuid` column) and as a value inside a trigger action (`INSERT ... VALUES (gen_random_uuid(), ...)`). See `docs/reports/FEAT-RANDOMUUID-REPORT.md`.

### LIKE → ILIKE

SQLite's `LIKE` is case-insensitive (ASCII) by default; PostgreSQL's `LIKE` is case-sensitive. To preserve SQLite semantics, `like` compiles to `LIKE` on SQLite and **`ILIKE` on PostgreSQL**. This is the standard pragmatic mapping and is accepted as a known trade-off (`ILIKE` folds the full Unicode range; SQLite only folds ASCII).

## 4. SELECT grammar

Parser: `compat/sqlselect.go` (`parseCatalogSelect`). A view definition is `CREATE VIEW name AS <select>`; the parser strips the `CREATE ... AS SELECT ` prefix and requires a `SELECT`.

```
[WITH cte_name AS ( <query> ) [, cte_name AS ( <query> )]...]
<branch>
[ {UNION [ALL] | INTERSECT | EXCEPT} <branch> ]...
[ORDER BY expr [ASC|DESC], ...]
[LIMIT n]
[OFFSET n]

<branch> ::=
SELECT [DISTINCT] projection, ... 
FROM source[[ AS] alias]
[ {LEFT [OUTER] | INNER | CROSS | } JOIN source[[ AS] alias] [ON expr] ]...
[WHERE expr]
[GROUP BY expr, ...]
[HAVING expr]

source ::= table | ( <query> )
```

- **Projections**: comma-separated. Each is `expr` or `expr AS alias`; the alias is a simple identifier (no dot). `SELECT *` / a bare `*` projection is **rejected** (`unsupported catalog expression`).
- **CTEs (`WITH`)**: an optional, **non-recursive** `WITH` prefix binds one or more named common table expressions (`SelectQuery.With []CommonTableExpr{Name, Query}` in `compat/schema.go`, JSON `with` with `omitempty`), each a full query in this same grammar, referenced from a `FROM`/`JOIN` by its name like an ordinary table. Materialized-result and name-shadowing semantics (a CTE shadows a real table of the same name for the query) are identical in both engines. **`WITH RECURSIVE` is rejected** (`WITH RECURSIVE is outside the canonical grammar`): recursive termination and observable row ordering are not guaranteed byte-identical between engines, so such a view becomes an unresolved object (`Exact = false`) rather than divergent SQL.
- **FROM / JOIN source**: a named table (`table`, `table alias`, `table AS alias`; name not schema-qualified, no `.`) **or a derived table** `( <query> ) [AS] alias` — a parenthesized subquery in this same grammar (`TableSource.Subquery *SelectQuery`, JSON `subquery` with `omitempty`). A derived table **requires an alias** (PostgreSQL's rule; keeps the canonical form exact for both engines) and cannot be correlated with the enclosing query (standard SQL), so both engines materialize it identically. Any other compound source (top-level comma; a parenthesized group that is not a single subquery) is rejected with `compound table source is outside the canonical grammar`.
- **JOINs**: `LEFT [OUTER]`, `INNER`, `CROSS`, and bare `JOIN` (treated as `INNER`). `LEFT`/`INNER` require `ON <expr>`; `CROSS` takes no `ON`. Multiple joins chain.
- **WHERE / HAVING**: a catalog expression (Section 3).
- **GROUP BY**: comma-separated expressions.
- **ORDER BY**: comma-separated; each item is `expr [ASC|DESC]`.
- **LIMIT / OFFSET**: non-negative integers. A **negative** value is rejected at parse time with `unsupported negative LIMIT %d` / `unsupported negative OFFSET %d`.
- **Set operations (compounds)**: a left-associative chain of `UNION`, `UNION ALL`, `INTERSECT` and `EXCEPT` over branches (`q0 op1 q1 op2 q2 ...`). Each branch is a single SELECT with the grammar above (projections, FROM, JOINs, WHERE, GROUP BY, HAVING) but **no** ORDER BY/LIMIT/OFFSET of its own; a trailing ORDER BY/LIMIT/OFFSET applies to the whole compound and is hoisted onto the leading query (`SelectQuery.Compounds []CompoundSelect{Operator, Query}` in `compat/schema.go`, JSON `compounds` with `omitempty`). All four operators have identical set semantics in both engines. A chain that **mixes `INTERSECT` with `UNION`/`EXCEPT`** is rejected (`compound mixing INTERSECT with UNION/EXCEPT is outside the canonical grammar`): `INTERSECT` binds more tightly than `UNION`/`EXCEPT` in PostgreSQL but has equal (left-associative) precedence in SQLite, so a flat chain would group differently per engine; such a view becomes an unresolved object (`Exact = false`) rather than divergent SQL. A homogeneous chain of a single operator (including all-`INTERSECT`) and any chain of `{UNION, UNION ALL, EXCEPT}` are accepted. Parenthesized branches (the shape PostgreSQL deparses around a mixed-precedence compound) are rejected because a branch must begin with `SELECT`. A branch's `FROM` may use a derived table (`FROM (SELECT ...) alias`); a `WITH` prefix binds the **whole** compound (its leading query), not an individual branch.

Derived tables (`FROM (SELECT ...) alias`) and non-recursive CTEs (`WITH`) are supported (see above). Still always rejected in SELECT: `WITH RECURSIVE`; a **subquery in an `IN` predicate** (`x IN (SELECT ...)`) and other subqueries in the expression grammar; correlated subqueries and `EXISTS`; window functions; `SELECT *`; a derived table without an alias; other compound table sources (top-level comma); schema-qualified table names; and any compound chain that mixes `INTERSECT` with `UNION`/`EXCEPT`.

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
- A clause keyword (`WHERE`, `GROUP BY`, `HAVING`, `ORDER BY`, `LIMIT`, `OFFSET`) with no operand — e.g. `GROUP BY` at the end of the string — is rejected with `SELECT <keyword> clause has no operand`; SQLite and Postgres treat this as a syntax error, and the layer rejects it rather than accepting an empty clause that the emitter would silently discard.
- Non-`public` schema qualifier on a PostgreSQL trigger.
- `SELECT *` or a bare `*` projection.
- Compound or schema-qualified table sources in `FROM`.
- Derived tables (`FROM (SELECT ...) alias`) and non-recursive CTEs (`WITH`) are supported (Section 4). `WITH RECURSIVE`, subqueries in the expression grammar (including `x IN (SELECT ...)`), correlated subqueries, `EXISTS`, and window functions are rejected. Set operations (`UNION`/`UNION ALL`/`INTERSECT`/`EXCEPT`) are supported as compounds (Section 4), except a chain mixing `INTERSECT` with `UNION`/`EXCEPT`, which is rejected because its grouping diverges between engines.
- Any function not in the allowlist (Section 3).
- Any expression outside the grammar (Section 3).
- Unsupported PostgreSQL `DEFAULT` casts (only a known set is accepted: `text`, `character varying`, `character`, `boolean`, `smallint`, `integer`, `bigint`, `numeric`, `real`, `double precision`, `date`, `timestamp without time zone`, `timestamp with time zone`, `uuid`, `json`, `jsonb`).
- Routines with a non-void return, languages other than `plpgsql`/`sql`, or parameters with `DEFAULT`/`=`.
- `vector` without exactly one positive dimension argument.
- A `VIRTUAL` generated column (`Generated.Stored = false`), a generated column that also has a `Default`, or a generated column that is part of a `primary_key`.
- A domain that is unnamed, duplicated, or has an unsupported base type family; a domain CHECK outside the expression grammar; a column that references an unknown domain, whose `Type` does not equal the domain base type, or that is not neutral (`Nullable=false`, an own `Default`, or `Generated`).
- `Version{0,0,0}` as a source/destination version (invalid dedup key).
- Trigger/routine actions outside the three canonical forms.

## 8. CLI

A single `compat` binary dispatches three subcommands — `audit`, `copy`, `cutover` — that previously lived in three separate binaries (`compat-audit`, `compat-copy`, `compat-cutover`). Each subcommand preserves the observable behavior of its former binary byte-for-byte (same JSON envelopes, exit codes, streams, line order, findings/reports/dry-run plan) with one deliberate exception: the message prefixes changed from `compat-audit:` / `compat-copy:` / `compat-cutover:` to `compat audit:` / `compat copy:` / `compat cutover:`.

Invoked with no subcommand, an unknown subcommand, or any `--help`-ish leading token, `compat` prints a shared usage hint to stderr and emits a typed `ERR_USAGE` JSON envelope to stdout, exiting 2 — the same style as each subcommand's own usage path. A leading token that begins with `-` and is not `--help`-ish is a flag placed before the subcommand; it is still `ERR_USAGE` (exit 2) but the envelope message orients the user: `compat: flags must follow the subcommand (e.g. compat cutover --dry-run <config>)`. A leading `--` is the standard end-of-flags separator at the dispatch level too: `compat -- audit x.json` dispatches to `audit` like `compat audit x.json`.

Each subcommand takes exactly one JSON config argument (`compat cutover` accepts an optional `--dry-run` flag, in any position after the subcommand). Any argument that begins with `-` and is not a recognized flag (`--dry-run` for `compat cutover`; none for `compat audit`/`compat copy`) is rejected as `ERR_USAGE` (exit 2) — it is never treated as the positional config path. A recognized flag repeated (e.g. `compat cutover --dry-run --dry-run x`) is also `ERR_USAGE` (exit 2) with a `duplicate flag` message, rather than silently deduplicated. A bare `--` token is the standard end-of-flags separator: once seen, every later argument is positional even if it begins with `-` (so `compat audit -- --raro.json` treats `--raro.json` as the config path), and `--` itself is discarded. Exit codes: `0` success; `1` any error or non-exact/non-equivalent result; `2` wrong argument count, an unexpected or duplicate flag, or a missing/unknown subcommand.

### 8.1 Typed error protocol (machine-facing)

On any failure each CLI exits with its current code (`1`, or `2` for `ERR_USAGE`) and emits a machine-readable JSON signal. The shape of that signal depends on the failure path, so an agent should parse the per-case contract below rather than assume one fixed layout:

- **Simple error envelope (default).** Most failures print a single-line JSON envelope to **stdout**, and nothing else machine-readable:

```json
{"status":"error","code":"<CODE>","message":"<detalle>"}
```

- **`compat cutover` diverged.** `ERR_VERIFY_DIVERGED` emits exactly **one** JSON line on stdout — the `cutoverReport` with the typed `code` embedded (`{"status":"diverged","code":"ERR_VERIFY_DIVERGED",...}`). There is no separate `{"status":"error",...}` envelope on this path.
- **`compat copy` not-exact or diverged.** Before the simple error envelope on stdout, `compat copy` emits a structured JSON payload to **stderr**: the `[]Finding` array on `ERR_AUDIT_NOT_EXACT`, or the `VerificationReport` object on `ERR_VERIFY_DIVERGED`. The error line itself is printed to stderr **exactly once** (alongside the payload), and the plain error envelope follows on stdout with the same `code`; an agent reads the structured detail from stderr and the typed code from stdout. (`compat cutover` not-exact follows the same stderr-once pattern for its `[]Finding` array.)

Each envelope line is one parseable JSON object (the message is JSON-encoded, so embedded newlines never break it). An agent branches on `code`. The taxonomy is closed; the CLI picks the most specific applicable code. Free-text diagnostics also go to stderr for humans.

| Code | Emitted when | Exit |
|---|---|---|
| `ERR_USAGE` | Wrong argument count, an unexpected flag, a duplicated recognized flag, or a missing/unknown subcommand at the top level. | `2` |
| `ERR_CONFIG` | The config file is unreadable, fails to decode, or `compat.Audit` rejects the contract (`Contract.Validate`). Every config is decoded with `json.Decoder.DisallowUnknownFields`, so an unknown key is an explicit error rather than a silently-dropped key; a `schema`/`schema_ref` violation (both, neither, or an unreadable/invalid `schema_ref` file) is also `ERR_CONFIG`. | `1` |
| `ERR_AUDIT_NOT_EXACT` | A required (or inferred) feature is not `exact` (`RequireExact` fails). `compat audit` emits its findings array to stdout, then this envelope; `compat copy` and `compat cutover` emit the findings array to stderr, then this envelope. | `1` |
| `ERR_CONNECT_SOURCE` | The source store cannot be opened or pinged (`OpenStore`/`Ping` for the source). | `1` |
| `ERR_CONNECT_DESTINATION` | The destination store cannot be opened or pinged (`OpenStore`/`Ping` for the destination). | `1` |
| `ERR_SCHEMA` | `Schema.Validate` or `ApplySchema` fails. | `1` |
| `ERR_SNAPSHOT` | `ExportSnapshot` or `ImportSnapshot` fails. | `1` |
| `ERR_REPLICATION_CONFLICT` | A `compat.ConflictError` is raised while replaying the journal during catch-up (`ApplyChangesTolerant`). | `1` |
| `ERR_CAPTURE` | `InstallChangeCapture` or `ReadCapturedChanges` fails. | `1` |
| `ERR_VERIFY_DIVERGED` | Verification digests differ (`VerifySnapshots` → `Equivalent == false`). `compat cutover` emits one JSON line — its `diverged` result with this `code` embedded (no separate envelope). `compat copy` emits its `VerificationReport` to stderr, then the error envelope with this `code`. | `1` |
| `ERR_INTERNAL` | Any failure not covered above (e.g. `VerifySnapshots` returns an error, encoding fails, context cancellation). | `1` |

Errors are classified by **phase heuristic** (which step of the flow failed) plus `errors.As` against the existing exported `compat.ConflictError`; the public `compat/` API is not extended. `compat copy` maps a non-equivalent `VerificationReport` to `ERR_VERIFY_DIVERGED`.

### `compat audit <contract.json>`

Audits a `Contract` (`{source, destination, required_features}`) and prints one `Finding` per required feature as a JSON array on stdout.

```json
[{"feature":"tables","status":"exact"},
 {"feature":"canonical_foreign_keys","status":"exact"},
 {"feature":"transactions","status":"exact"}]
```

- Exit `0`: every required feature is `exact`.
- Exit `1`: any feature is not `exact`. The findings array is still printed to stdout first, followed by a typed `{"status":"error","code":"ERR_AUDIT_NOT_EXACT",...}` line; the failing reason is also printed to stderr. Any read/parse/audit error prints `ERR_CONFIG` instead.
- Exit `2`: argument count is not 1 (`ERR_USAGE`).

### `compat copy <migration.json>`

Config: `{source_dsn, destination_dsn, contract, schema | schema_ref}`. Exactly one of `schema` (an inline `compat.Schema`) or `schema_ref` (a path to a JSON file holding a bare `compat.Schema` object, resolved **relative to the config file**, not the cwd) is required; both or neither is `ERR_CONFIG`, and an unreadable or JSON-invalid `schema_ref` file is `ERR_CONFIG`. Infers features from the schema (`InferFeatures`), audits, requires exact, then exports the source snapshot, imports it into the destination, re-exports the destination, and verifies digests. Prints a `VerificationReport` on stdout:

```json
{"source_digest":"...","destination_digest":"...","equivalent":true}
```

- Exit `0`: `equivalent == true`.
- Exit `1`: any typed error (see 8.1). On `ERR_AUDIT_NOT_EXACT` the `[]Finding` array is printed to stderr before the envelope; on `ERR_VERIFY_DIVERGED` (digests differ) the `VerificationReport` is printed to stderr before the envelope, so the digests are recoverable as JSON rather than only as free text in the message. In both cases the error line is printed to stderr exactly once (not duplicated).
- Exit `2`: wrong argument count, an unexpected flag, or a duplicated recognized flag (`ERR_USAGE`). The destination must be empty for the described objects (import is additive).

### `compat cutover <cutover.json>`

Config: `{source_dsn, destination_dsn, contract, schema | schema_ref, options}`. Exactly one of `schema` (inline `compat.Schema`) or `schema_ref` (a path to a JSON file holding a bare `compat.Schema` object, resolved **relative to the config file**, not the cwd) is required; both or neither is `ERR_CONFIG`, and an unreadable or JSON-invalid `schema_ref` file is `ERR_CONFIG`. `options` is optional with defaults `poll_interval_ms=1000`, `drain_polls=3`, `batch_limit=500`. Orchestrates a zero-window SQLite → PostgreSQL cutover: audit → install change capture on source → export+import snapshot → drain the journal with `ApplyChangesTolerant` until `drain_polls` consecutive empty reads → verify digests. Prints a `cutoverReport` on stdout and progress lines on stderr:

```json
{"status":"ready","source_digest":"...","destination_digest":"...","changes_applied":N}
```

- Exit `0`: digests match → `status=ready`.
- Exit `1`: `status=diverged` (digests differ — the report keeps its shape and adds `"code":"ERR_VERIFY_DIVERGED"`), a required feature is not exact (`ERR_AUDIT_NOT_EXACT`, findings also printed to stderr once, with the error line once), or any typed error.
- Exit `2`: wrong argument count, an unexpected flag, or a duplicated recognized flag (`ERR_USAGE`).
- Cutting the application's DSN over to the destination is **manual** and is not this tool's responsibility; do it after `status=ready`.

The `diverged` report carries the typed code inline:

```json
{"status":"diverged","code":"ERR_VERIFY_DIVERGED","source_digest":"...","destination_digest":"...","changes_applied":N}
```

#### `compat cutover --dry-run <cutover.json>`

Runs only the read-only phases: parse config, audit the contract (refusing non-exact with `ERR_AUDIT_NOT_EXACT`), connect and ping both stores (`ERR_CONNECT_SOURCE`/`ERR_CONNECT_DESTINATION`), count source rows per contract table, and detect whether the destination already holds those tables. It prints a plan JSON on stdout and exits `0`:

```json
{"status":"plan","audit":[{"feature":"tables","status":"exact"}],
 "source_tables":[{"name":"entries","rows":N}],
 "destination_has_tables":false,
 "phases":["install_capture","snapshot","catch_up","verify"]}
```

- `audit`: the full `Finding` array for the required + inferred features.
- `source_tables`: one `{name, rows}` per `schema.tables` entry, counted via `SELECT count(*)`.
- `destination_has_tables`: `true` iff every contract table already exists on the destination (a real cutover's `ImportSnapshot` would collide with those).
- `phases`: the fixed sequence a real cutover would run after the plan.

`--dry-run` never installs capture, creates tables, imports a snapshot, or writes a journal on either store. If the audit is not exact or a connection fails, it emits the corresponding typed error JSON and exits `1`.

## 9. Migration flow (phases and verdicts)

A migration is a sequence of auditable verdicts. Each phase produces a machine-checkable result.

1. **Audit** — `compat audit contract.json` (or the audit step inside `compat copy`/`compat cutover`, which appends `InferFeatures(schema)` to `required_features`).
   - Verdict: every finding `status=exact` (exit `0`). Any `unknown`/`unsupported`/`transformed`/`emulated` stops the migration (exit `1`).
2. **Move** — choose one:
   - Offline snapshot copy: `compat copy migration.json`.
   - Zero-window cutover: `compat cutover cutover.json`.
   - Verdict (copy): `VerificationReport.equivalent == true` (exit `0`). Verdict (cutover): `cutoverReport.status == "ready"` (exit `0`).
3. **Verify** — `compat copy`/`compat cutover` verify internally (digest comparison via `VerifySnapshots`). Programmatic callers can re-verify at any time with `compat.VerifySnapshots(source, destination)`, expecting `Equivalent == true` and equal `SourceDigest`/`DestinationDigest`.

A canonical schema that contains a `vector` column infers `canonical_vectors`; one with views/triggers/routines/indexes infers the corresponding `canonical_*` features. Generic families (`foreign_keys`, `check_constraints`, `indexes`, `views`, `triggers`, `stored_routines`, `full_text`) remain `unknown` because they represent arbitrary native SQL and are not audited as exact.

See [`contracts/migration.contract.example.md`](contracts/migration.contract.example.md) for a full executable example.

## 10. Upgrade compatibility — capture trigger versioning

`InstallChangeCapture` installs change-capture triggers whose `DECIMAL`/`typeof='real'` branch emits a versioned `realDecimalMarker` prefix before the `printf('%!.17g')` text, so the applier can tell a REAL-stored decimal from arbitrary TEXT. Triggers installed by an **older** tool version emit that branch **without** the marker.

This is a **tooling version boundary**, not a data format the journal is self-describing about. Observe it when upgrading the tool across that boundary:

- **Reinstall capture after upgrading.** Triggers installed by a previous tool version must be **reinstalled** (`InstallChangeCapture`) on every store before capturing new changes with the new code. The old triggers do not emit the marker the new applier expects.
- **Drain or discard in-flight pre-upgrade journals.** A journal captured by pre-upgrade triggers (no marker) must **not** be applied with the new code: drain it to the destination before upgrading, or discard it and re-snapshot from a clean source. The documented migration contract (install capture → snapshot → catch-up drains the journal) already keeps you on the safe side; a mixed-version in-flight journal is outside that contract.
- **Divergence is detected, never silent.** If a pre-upgrade journal is nonetheless applied with the new code, `ApplyChanges` does not error, but `VerifySnapshots` reports `Equivalent == false` for affected high-magnitude REAL-stored decimals (the new code reads the unmarked text verbatim while the source canonizes the REAL `float64`). There is no silent corruption: a `verify` step surfaces the divergence.

## 11. Upgrade compatibility — `date` family mapping

`date` now compiles to **`TEXT`** on PostgreSQL (it was previously native `DATE`). A native `DATE` column is returned by pgx as a `time.Time`, which the canonical layer would fold to a `TimestampValue` (`"2020-01-01T00:00:00Z"`) and always diverge from the SQLite `TEXT` source (`"2020-01-01"`). `TEXT` mirrors the protective `timestamp`/`json`/`uuid` mapping and preserves the date byte-for-byte.

This is a **schema mapping boundary**. Observe it when upgrading the tool across it:

- **Recreate a legacy destination's schema.** A destination created by an **older** tool version still holds native `DATE` columns. Re-create the destination schema (drop and re-`ApplySchema`, or re-run the full `compat copy`/`compat cutover` migration from a clean source) so the columns become `TEXT`. `canonicalValue` is defensive: when the family is `date` it still canonicalizes a `time.Time` from a legacy native-`DATE` column to the date-only form, so a stray re-verify against such a column converges rather than diverging — but the supported path is to recreate the schema.
- **Nothing is silent either way.** Thanks to the defensive branch above, an isolated re-verify against a legacy native-`DATE` destination **converges** when the stored values are date-only (divergence only appears if the source carried a time component that the native `DATE` column truncated). Running `compat copy`/`compat cutover` against a pre-existing legacy destination does not reach verify at all: schema import collides with the existing table and fails with `ERR_SNAPSHOT` (`relation already exists`). Either way there is no silent corruption; the supported path remains recreating the destination schema.

## Interface note

The CLIs are the canonical interface for agents: every operation is available as a command with a machine-readable JSON result, a typed error envelope and a stable exit code. An MCP server wrapping the same library functions is a possible optional layer (useful for shell-less agents or long-running cutover monitoring) and is intentionally not part of this repository today.
