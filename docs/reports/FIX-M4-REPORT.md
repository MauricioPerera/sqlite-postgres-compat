# FIX-M4-REPORT

Fixes the two confirmed audit findings (BUG 1 and BUG 2). Touched files, per
scope: `compat/runtime.go`, `compat/runtime_test.go`, `compat/features.go`,
`compat/spec_test.go`. No other files were modified.

## BUG 1 — NULL columns produced a spurious `nil` token in `SearchText`

`compat/runtime.go`, in `SearchText`'s row loop:

```go
var content strings.Builder
for _, value := range values[1:] {
    // A NULL column carries no searchable text; stringify(nil) would
    // otherwise emit "<nil>" and tokenize to a spurious "nil" token.
    if value == nil {
        continue
    }
    content.WriteString(stringify(value))
    content.WriteByte(' ')
}
```

`values[i]` is a Go `any` populated by `rows.Scan`. For a SQL NULL, the driver
leaves it as `nil`, and the old code passed `nil` straight into `stringify`,
whose `default` branch is `fmt.Sprint(nil)` → the literal string `"<nil>"`.
`tokenize` then split on non-alphanumeric runes and produced the token `nil`,
so any query containing the word "nil" matched rows whose only matching text
was a NULL column. The fix skips `nil` values before stringifying, so a NULL
column contributes no tokens — no other behavior changes.

## BUG 2 — `InferFeatures` never emitted `CanonicalFullText`

`compat/features.go`, `InferFeatures`:

```go
// CanonicalFullText is provided unconditionally by the portable SearchText
// runtime, independent of the schema's tables, so it is always inferred —
// mirroring how Tables is seeded below rather than gated on a schema check.
seen := map[Feature]struct{}{Tables: {}, CanonicalFullText: {}}
```

`assess(CanonicalFullText)` already returns `Exact` (the runtime `SearchText`
implements deterministic canonical full-text matching), but `InferFeatures`
never added the feature to the contract, so the feature-gate was disconnected
from the implementation. The capability is provided by the runtime
unconditionally — it does not depend on the schema having any particular
table, column, or constraint — so it is seeded into the initial `seen` map,
exactly as `Tables` is (which is also seeded unconditionally rather than gated
on `len(schema.Tables) > 0`). This is the most consistent pattern with the
file: `Tables` is the only feature seeded without a schema condition, and
`CanonicalFullText` shares that property. I chose unconditional seeding over
gating on (e.g.) presence of text columns because `SearchText` is a
runtime-level capability available to any schema, and gating it would re-introduce
the exact disconnect this fix closes (a schema without text columns today
could still call `SearchText` later). `assess` was already correct and is
unchanged.

## Tests added

`compat/runtime_test.go` — `TestSearchTextSkipsNullColumns`: in-memory SQLite
table with a nullable `body` column; inserts a row with `body = NULL` and a
row with the literal text `'nil'`. `SearchText(..., "nil")` returns exactly
one result, the literal-nil row (id `2`). The NULL row is the negative case
(it previously matched), the literal row is the positive control.

`compat/spec_test.go` — `TestInferFeaturesIncludesCanonicalFullText`: a valid
schema with a table + primary key; asserts `InferFeatures` includes `Tables`,
`PrimaryKeys`, and `CanonicalFullText`; then builds a contract requiring
`canonical_full_text`, audits it, and asserts the finding is `{Exact}` and
that `RequireExact` passes.

## Pre-existing test assertions

No existing test enumerates the exact set of inferred features, so no
existing assert needed adjustment. The only changed assertions are the new
tests above; pre-existing tests are untouched.

## Trade-offs

- BUG 1: a NULL cell now silently contributes no text. This is the intended
  behavior (a NULL is "no value"), and it removes the false-positive `nil`
  token. No real token content is lost because `stringify(nil)` never produced
  genuine searchable text — only the artifact `"<nil>"`.
- BUG 2: `CanonicalFullText` is now always inferred, so every contract built
  via `InferFeatures` will require and audit it as `Exact`. Since the runtime
  genuinely provides it, this is correct; it only fails if a caller relies on
  `InferFeatures` returning the previous smaller set, which no caller does
  (see `cmd/compat-copy/main.go`, which appends the result to
  `RequiredFeatures`).

## Verification (real output)

```
$ go build ./... && echo "=== BUILD OK ===" && go vet ./... && echo "=== VET OK ===" && go test ./...
=== BUILD OK ===
=== VET OK ===
?   	example.com/sqlite-postgres-compat/cmd/compat-audit	[no test files]
?   	example.com/sqlite-postgres-compat/cmd/compat-copy	[no test files]
ok  	example.com/sqlite-postgres-compat/compat	2.208s
```

Targeted run of the new tests:

```
$ go test ./compat/ -run 'TestSearchTextSkipsNullColumns|TestInferFeaturesIncludesCanonicalFullText' -v
=== RUN   TestSearchTextSkipsNullColumns
--- PASS: TestSearchTextSkipsNullColumns (0.00s)
=== RUN   TestInferFeaturesIncludesCanonicalFullText
--- PASS: TestInferFeaturesIncludesCanonicalFullText (0.00s)
PASS
ok  	example.com/sqlite-postgres-compat/compat	2.106s
```