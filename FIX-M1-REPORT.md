# FIX-M1 — LIKE case-sensitivity preservation across engines

## Bug
`compat/ddl.go` `compileExpression` emitted the literal operator `LIKE` for both
engines via a fixed operator map (`ddl.go:267-271`). SQLite's `LIKE` is
case-insensitive (ASCII) by default; Postgres' `LIKE` is case-sensitive. A `like`
expression compiled to Postgres therefore silently changed results without any
error, breaking SQLite→Postgres semantic parity.

## Fix
`compileExpression` already receives the target `engine`. Removed `"like"` from
the shared operator map and added an explicit branch: when `expression.Kind == "like"`,
compile to `ILIKE` for Postgres and `LIKE` for SQLite.

Only `compat/ddl.go` and `compat/ddl_test.go` were touched. No public signatures
changed. No other files modified.

### Diff (ddl.go, binary-operator case)
```go
operators := map[string]string{
    "and": "AND", "or": "OR", "eq": "=", "ne": "<>", "lt": "<", "lte": "<=", "gt": ">", "gte": ">=",
    "add": "+", "sub": "-", "mul": "*", "div": "/",
}
// SQLite's LIKE is case-insensitive (ASCII) by default, while Postgres's
// LIKE is case-sensitive. Compile to ILIKE on Postgres to preserve the
// SQLite semantics. Note: ILIKE is case-insensitive across the full
// Unicode range, whereas SQLite only folds ASCII — this is the standard
// pragmatic mapping, accepted as a known trade-off.
if expression.Kind == "like" {
    operator := "LIKE"
    if engine == Postgres {
        operator = "ILIKE"
    }
    return "(" + left + " " + operator + " " + right + ")", nil
}
return "(" + left + " " + operators[expression.Kind] + " " + right + ")", nil
```

## Tests
New test `TestCompileLikePreservesSQLiteSemanticsForBothEngines` in
`compat/ddl_test.go`: builds a `Schema` with a table `products` and a view
`product_codes` whose `WHERE` is a `like` expression (`code LIKE 'prod_%'`),
compiles via `CompileDDL` for both SQLite and Postgres, and asserts on the full
emitted view statement:
- Postgres: contains ` ILIKE ` and `("code" ILIKE 'prod_%')`, does NOT contain ` LIKE ` or `ILIKE`-less form.
- SQLite: contains ` LIKE ` and `("code" LIKE 'prod_%')`, does NOT contain `ILIKE`.

The case is exercised inside a complete compiled view (not just the bare
expression), per the definition of done.

No preexisting test asserted the bare string `LIKE` in Postgres output, so no
existing test required adjustment.

## Trade-off (declared, accepted up front)
`ILIKE` is case-insensitive across the full Unicode range; SQLite's default
`LIKE` only folds ASCII. `ILIKE` is the standard pragmatic mapping to preserve
SQLite semantics on Postgres, but non-ASCII case folding may differ. This is
the known, accepted limitation of this fix.

## Real command output
```
$ go build ./... && go vet ./... && go test ./...
?       example.com/sqlite-postgres-compat/cmd/compat-audit     [no test files]
?       example.com/sqlite-postgres-compat/cmd/compat-copy      [no test files]
ok      example.com/sqlite-postgres-compat/compat      2.291s

$ go test ./compat/ -run TestCompileLikePreservesSQLiteSemanticsForBothEngines -v
=== RUN   TestCompileLikePreservesSQLiteSemanticsForBothEngines
--- PASS: TestCompileLikePreservesSQLiteSemanticsForBothEngines (0.00s)
PASS
ok      example.com/sqlite-postgres-compat/compat      2.326s
```

Build, vet, and full test suite green. No tests modified or removed.