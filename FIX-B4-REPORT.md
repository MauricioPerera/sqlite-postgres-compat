# FIX-B4 — Routine ↔ trigger symmetry for UPDATE and DELETE (end-to-end)

## Scope

Symmetry between canonical **routines** and **triggers** for `UPDATE` and
`DELETE`, end-to-end: schema field, Postgres-body parsing, and runtime
execution inside one transaction.

## Files touched (only the allowed set)

- `compat/schema.go` — added `Where *Expression` to `RoutineAction` (additive, mirror of `TriggerAction`).
- `compat/sqlroutine.go` — routine body now accepts `UPDATE … SET … WHERE …` and `DELETE FROM … WHERE …`; parameter references in the `WHERE` are rewritten to `parameter` nodes; unsupported `WHERE` constructs are rejected explicitly.
- `compat/runtime.go` — `CallRoutine` executes `update`/`delete` actions in the same transaction, compiling the `WHERE` with engine-correct placeholders.
- `compat/sqlroutine_test.go` — new parse tests.
- `compat/runtime_test.go` — new execution tests.

No other files were modified. `compat/sqltrigger.go` was **reused** (its
`parseCatalogTriggerActions` and the `updateActionPattern`/`deleteActionPattern`
regexes already enforce `WHERE` and are shared by routine parsing), not edited.

## What changed

### `compat/schema.go`

`RoutineAction` gains `Where *Expression json:"where,omitempty"` and its
`Assignments` tag becomes `omitempty`, mirroring `TriggerAction` exactly:

```go
type RoutineAction struct {
	Kind        string       `json:"kind"`
	Table       string       `json:"table"`
	Assignments []Assignment `json:"assignments,omitempty"`
	Where       *Expression  `json:"where,omitempty"`
}
```

`Schema.Validate()` does **not** validate per-action structure for routines
today, and it does not do so for triggers either (trigger action structure is
enforced at parse/DDL-emit time). The task's validation extension was therefore
conditional on `Validate()` validating routine actions, which it does not — so
`Validate()` is unchanged, preserving the existing trigger↔routine symmetry at
that layer. Structural validation (UPDATE requires assignments + WHERE;
DELETE requires WHERE and zero assignments) is enforced at **parse time** in
`sqlroutine.go`, mirroring how `ddl.compileTriggerAction` enforces it for
triggers.

### `compat/sqlroutine.go`

The action loop now dispatches on `kind`:

- `insert` — unchanged (assignments via `routineCatalogExpression`).
- `update` — requires `len(Actions) != 0 && Where != nil`; assignments are
  converted with `routineCatalogExpression` (still parameter-or-literal only);
  the `WHERE` is rewritten with the new `routineCatalogWhereExpression`.
- `delete` — requires `Where != nil && len(Assignments) == 0`; `WHERE` rewritten
  with `routineCatalogWhereExpression`.
- anything else — explicit error `routine action %q is outside the canonical command grammar`.

The `WHERE` regexes come from `sqltrigger.go` (`updateActionPattern`,
`deleteActionPattern`), which already require a `WHERE` clause — so
`UPDATE`/`DELETE` **without** `WHERE` fails to match and yields an explicit
parse error (no silent acceptance).

`routineCatalogWhereExpression` walks the parsed `WHERE` AST:

- `column` → if the bare identifier names a declared parameter (and has no `.`),
  rewrite to `parameter`; otherwise keep as a table `column`. Qualified
  identifiers (`a.b`) are **rejected**.
- `parameter`, `null`, `string`, `integer`, `decimal`, `boolean` — kept.
- `and`/`or`/`eq`/`ne`/`lt`/`lte`/`gt`/`gte`/`like` — recursed (arity checked).
- `not`/`is_null`/`is_not_null` — recursed (arity checked).
- everything else → explicit error `routine WHERE expression %q is outside the canonical grammar`.

Assignment values stay restricted to parameter-or-literal exactly as before
(`routineCatalogExpression` is unchanged).

### `compat/runtime.go`

`CallRoutine` now branches on `action.Kind` inside the single transaction
(`store.Target.Engine` is captured once and threaded through):

- `insert` — unchanged (`insertRow`).
- `update` — builds `UPDATE "t" SET "c" = <ph>, … WHERE <compiled>` with
  placeholders per engine; assignment values resolve via `routineValue` +
  `driverValue` and are bound, never inlined.
- `delete` — builds `DELETE FROM "t" WHERE <compiled>` with placeholders.
- unknown kind — explicit error `routine %q action %q is unsupported`
  (unchanged contract, now reached for update/delete too).

`compileRoutineWhere` compiles the `WHERE` into a SQL fragment bound with
engine placeholders, appending resolved argument values to `args`:

- `parameter`/`string`/`integer`/`decimal`/`boolean` → resolved via
  `routineValue` and bound as a placeholder (`driverValue` handles the
  SQLite boolean→0/1 mapping, same path as `insertRow`).
- `null` → literal `NULL` (no placeholder needed).
- `column` → `quoteIdentifier(value)`; qualified `.` columns are rejected
  at runtime with an explicit error (defense in depth).
- comparison/logical operators → recursive; `like` compiles to `ILIKE` on
  PostgreSQL and `LIKE` on SQLite, mirroring `ddl.compileExpression`.
- unsupported kind → explicit error
  `routine WHERE expression %q is unsupported at runtime`.

Placeholder indexing is shared across the `SET` list and the `WHERE` for a
single statement and increments per bound value, so PostgreSQL `$N` numbering
stays correct; SQLite uses `?` throughout.

## What WHERE constructions the execution supports (explicit)

The runtime executor supports the **honest restricted subset** declared in the
task: comparisons of a column against a parameter or literal, composed with
logical operators, plus null tests.

**Supported (executed, parameterized):**

- Comparisons: `=`, `<>`/`!=`, `<`, `<=`, `>`, `>=`, `LIKE` (→ `ILIKE` on Postgres).
- Logical: `AND`, `OR`, `NOT`.
- Null tests: `IS NULL`, `IS NOT NULL`.
- Operands: table columns (bare identifiers), routine parameters, and
  literals (`NULL`, string, integer, decimal, boolean). Either side of a
  comparison may be the column or the parameter/literal (e.g. `WHERE p_status = status` works).

**Rejected with an explicit error (never silent):**

- `UPDATE`/`DELETE` without `WHERE` (rejected at parse by the shared regex).
- Arithmetic: `+`, `-`, `*`, `/`.
- String concatenation: `||`.
- Functions: `count`, `sum`, `avg`, `min`, `max`, `lower`, `upper`, `length`,
  `abs`, `trim`, `coalesce`, `replace`.
- `CURRENT_TIMESTAMP`.
- Qualified column references (`table.column`, including `NEW.`/`OLD.` — these
  are a trigger concept and are rejected for routines).
- Any unknown expression kind.

Both layers reject consistently: `routineCatalogWhereExpression` rejects at
parse time, and `compileRoutineWhere` rejects again at runtime as defense in
depth.

**Known limitation (declared honestly):** in a routine `WHERE`/assignment, a
bare identifier that collides with a declared parameter name is resolved as
the **parameter**, not the table column (this is the existing convention from
`routineCatalogExpression` for assignment values, now applied to `WHERE` too).
A column that shares a name with a parameter therefore cannot be referenced in
the `WHERE` by its bare name. This matches the pre-existing routine semantics
and is the same trade-off triggers avoid via `NEW.`/`OLD.` qualification.

## Tests added (none removed/modified)

### `compat/sqlroutine_test.go`

- `TestParsePostgresCatalogRoutineUpdate` — `UPDATE tasks SET status = p_status WHERE id = p_id` parses to `RoutineAction{Kind:"update"}` with assignments and a `Where` eq expression; `id` stays a `column`, `p_id`/`p_status` become `parameter`.
- `TestParsePostgresCatalogRoutineDelete` — `DELETE FROM tasks WHERE id = p_id` parses to `RoutineAction{Kind:"delete"}` with `Where` and zero assignments.
- `TestParsePostgresCatalogRoutineUpdateWithoutWhere` — `UPDATE … SET …` (no `WHERE`) is rejected.
- `TestParsePostgresCatalogRoutineDeleteWithoutWhere` — `DELETE FROM …` (no `WHERE`) is rejected.
- `TestParsePostgresCatalogRoutineInsertStillParses` — INSERT-only body still parses to a single insert action with no `Where` (no regression).

### `compat/runtime_test.go` (SQLite in-memory)

- `TestCallRoutineUpdateAffectsOnlyMatchingRows` — `update` changes only the row matching `WHERE id = p_id`; the non-matching row is verified unchanged.
- `TestCallRoutineDeleteAffectsOnlyMatchingRows` — `delete` removes only the matching row; the non-matching row survives.
- `TestCallRoutineRunsActionsAtomically` — a routine with `insert` + `update` + `delete` all apply within one transaction.
- `TestCallRoutineRejectsUnknownActionKind` — an action with `Kind:"bogus"` is rejected.

## Verification (real output, run from the repo root)

```
$ go build ./... && echo BUILD_OK && go vet ./... && echo VET_OK && go test ./...
BUILD_OK
VET_OK
?    	example.com/sqlite-postgres-compat/cmd/compat-audit	[no test files]
?    	example.com/sqlite-postgres-compat/cmd/compat-copy	[no test files]
ok  	example.com/sqlite-postgres-compat/compat	(cached)
```

Verbose run of the new tests:

```
$ go test ./compat/... -run 'TestParsePostgresCatalogRoutine|TestCallRoutine' -v
=== RUN   TestCallRoutineUpdateAffectsOnlyMatchingRows
--- PASS: TestCallRoutineUpdateAffectsOnlyMatchingRows (0.00s)
=== RUN   TestCallRoutineDeleteAffectsOnlyMatchingRows
--- PASS: TestCallRoutineDeleteAffectsOnlyMatchingRows (0.00s)
=== RUN   TestCallRoutineRunsActionsAtomically
--- PASS: TestCallRoutineRunsActionsAtomically (0.00s)
=== RUN   TestCallRoutineRejectsUnknownActionKind
--- PASS: TestCallRoutineRejectsUnknownActionKind (0.00s)
=== RUN   TestParsePostgresCatalogRoutine
--- PASS: TestParsePostgresCatalogRoutine (0.00s)
=== RUN   TestParsePostgresCatalogRoutineRejectsQueryFunction
--- PASS: TestParsePostgresCatalogRoutineRejectsQueryFunction (0.00s)
=== RUN   TestParsePostgresCatalogRoutineUpdate
--- PASS: TestParsePostgresCatalogRoutineUpdate (0.00s)
=== RUN   TestParsePostgresCatalogRoutineDelete
--- PASS: TestParsePostgresCatalogRoutineDelete (0.00s)
=== RUN   TestParsePostgresCatalogRoutineUpdateWithoutWhere
--- PASS: TestParsePostgresCatalogRoutineUpdateWithoutWhere (0.00s)
=== RUN   TestParsePostgresCatalogRoutineDeleteWithoutWhere
--- PASS: TestParsePostgresCatalogRoutineDeleteWithoutWhere (0.00s)
=== RUN   TestParsePostgresCatalogRoutineInsertStillParses
--- PASS: TestParsePostgresCatalogRoutineInsertStillParses (0.00s)
PASS
ok  	example.com/sqlite-postgres-compat/compat	2.304s
```

`go build ./...`, `go vet ./...`, and `go test ./...` are all green; no
pre-existing test was modified or deleted. The `e2e` package is build-tagged
(`//go:build e2e`) and requires a live PostgreSQL DSN, so it is excluded from
the default `go test ./...` baseline (unchanged from the starting state).