# FIX-M2 Report

Two confirmed bugs fixed, moving both from "silent acceptance with semantic loss"
to "explicit error". Baseline green preserved.

## What changed

### BUG 1 — negative LIMIT/OFFSET accepted at parse time
**File:** `compat/sqlselect.go` (LIMIT/OFFSET clause apply funcs)

`LIMIT`/`OFFSET` used `strconv.Atoi` without sign validation, so `LIMIT -1`
(and negative `OFFSET`) was accepted into the AST and only rejected later at
compile time. In SQLite `LIMIT -1` means "no limit" — a semantics this layer
cannot preserve.

Fix: after a successful `Atoi`, reject `number < 0` at parse time with
`unsupported negative LIMIT %d` / `unsupported negative OFFSET %d`. `LIMIT 0`
and `OFFSET 0` (number == 0, not < 0) keep parsing OK — zero is not regressed.

### BUG 2 — non-public schema silently discarded in Postgres triggers
**File:** `compat/sqltrigger.go` (`postgresTriggerPattern` + `parsePostgresCatalogTrigger`)

The pattern used a non-capturing `(?:<id>\.)?` that dropped *any* schema
qualifier silently (`myschema.products` → `Table="products"`), inconsistent
with `parseCatalogSelect`, which rejects qualified names outright.

Fix: changed the schema group to capturing `(<id>\.)?` (capture groups shift:
table is now `match[5]`, WHEN is `match[6]`). After matching, the qualifier is
unquoted and compared case-insensitively to `public`: `public.` is accepted
and dropped (existing documented behavior, preserved by the unmodified
`TestParsePostgresCatalogTrigger`); any other schema returns
`unsupported trigger schema %q: only "public" is allowed` (contains both
"unsupported" and the schema name).

## New tests

`compat/sqlselect_test.go`:
- `TestParseCatalogSelectRejectsNegativeLimit` — `SELECT a FROM t LIMIT -1`
  → error containing "unsupported" or "negative".
- `TestParseCatalogSelectRejectsNegativeOffset` — `SELECT a FROM t OFFSET -5`
  → same.
- `TestParseCatalogSelectAllowsZeroLimitAndOffset` — `LIMIT 0` and
  `LIMIT 10 OFFSET 0` still parse OK (zero not regressed).

`compat/sqltrigger_test.go`:
- `TestParsePostgresCatalogTriggerAcceptsPublicSchema` —
  `public.products` parses OK with `Table == "products"`.
- `TestParsePostgresCatalogTriggerRejectsNonPublicSchema` —
  `myschema.products` → error containing "unsupported" and "myschema".

All new assertions verify the error message/content, not just `err != nil`.
No pre-existing test was modified or removed.

## Trade-offs
- `public` is accepted case-insensitively (e.g. `PUBLIC.products`), matching
  the SQL default-schema equivalence. Other schemas are rejected even though
  PostgreSQL itself would allow them; this is intentional per the desired
  behavior (the layer only targets the default schema).
- Schema validation happens after the regex match, so a syntactically valid
  but unsupported-schema trigger fails with a schema-specific message rather
  than the generic "outside the canonical grammar".

## Real output

```
$ go build ./... && go vet ./... && go test ./...
?       example.com/sqlite-postgres-compat/cmd/compat-audit   [no test files]
?       example.com/sqlite-postgres-compat/cmd/compat-copy   [no test files]
ok      example.com/sqlite-postgres-compat/compat   2.044s

$ go test ./compat/ -run 'TestParseCatalogSelect|TestParsePostgresCatalogTrigger' -v
=== RUN   TestParseCatalogSelectCommonView
--- PASS: TestParseCatalogSelectCommonView (0.00s)
=== RUN   TestParseCatalogSelectWithJoinAndAggregation
--- PASS: TestParseCatalogSelectWithJoinAndAggregation (0.00s)
=== RUN   TestParseCatalogSelectRejectsNegativeLimit
--- PASS: TestParseCatalogSelectRejectsNegativeLimit (0.00s)
=== RUN   TestParseCatalogSelectRejectsNegativeOffset
--- PASS: TestParseCatalogSelectRejectsNegativeOffset (0.00s)
=== RUN   TestParseCatalogSelectAllowsZeroLimitAndOffset
--- PASS: TestParseCatalogSelectAllowsZeroLimitAndOffset (0.00s)
=== RUN   TestParsePostgresCatalogTrigger
--- PASS: TestParsePostgresCatalogTrigger (0.00s)
=== RUN   TestParsePostgresCatalogTriggerAcceptsPublicSchema
--- PASS: TestParsePostgresCatalogTriggerAcceptsPublicSchema (0.00s)
=== RUN   TestParsePostgresCatalogTriggerRejectsNonPublicSchema
--- PASS: TestParsePostgresCatalogTriggerRejectsNonPublicSchema (0.00s)
PASS
ok      example.com/sqlite-postgres-compat/compat   2.315s
```

Only the four permitted files were touched (`compat/sqlselect.go`,
`compat/sqlselect_test.go`, `compat/sqltrigger.go`, `compat/sqltrigger_test.go`).
No public signatures changed.