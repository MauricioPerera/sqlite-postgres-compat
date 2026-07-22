# FIX-Q1 — Hex literals interpreted as signed two's-complement like SQLite

## Bug
`compat/sqlparse.go` `catalogHexLiteral` parsed the hex digits with
`strconv.ParseUint(..., 16, 64)` and emitted the value with
`strconv.FormatUint(decimal, 10)` — i.e. the unsigned decimal. SQLite evaluates
hex literals as signed 64-bit two's-complement integers, so a value with the
high bit set was silently mistranslated: `0xFFFFFFFFFFFFFFFF` (SQLite `-1`)
emitted as `18446744073709551615`, and `0x8000000000000000` (SQLite
`-9223372036854775808`) emitted as `9223372036854775808`. This broke
SQLite→Postgres parity for high-bit-set hex literals with no error.

Repro from the audit: `parseCatalogExpression("x = 0xFFFFFFFFFFFFFFFF")` →
`Expression{Kind:"integer", Value:"18446744073709551615"}`.

## Fix
Parse the hex digits as `uint64` (unchanged — this is the only way to accept the
full 64-bit range), then reinterpret the bit pattern as `int64` and emit the
signed decimal with `strconv.FormatInt(int64(parsed), 10)`. This matches
SQLite's semantics exactly:

- `0xFFFFFFFFFFFFFFFF` → `-1`
- `0x8000000000000000` → `-9223372036854775808`
- `0x7FFFFFFFFFFFFFFF` → `9223372036854775807`
- `0x10` → `16` (small values unchanged)

Literals with more than 16 hex digits still overflow `ParseUint(..., 64)` and
return the existing explicit "unsupported catalog hexadecimal literal" error —
the out-of-range behavior is untouched.

Only `compat/sqlparse.go` and `compat/sqlparse_test.go` were touched. No public
signatures changed. No other files modified.

### Diff (sqlparse.go, catalogHexLiteral)
```go
// The hex digits are parsed as an unsigned 64-bit value and then reinterpreted
// as a signed two's-complement int64, matching how SQLite evaluates hex
// literals: 0xFFFFFFFFFFFFFFFF is -1, not 18446744073709551615.
func catalogHexLiteral(text string) (value string, handled bool, err error) {
    ...
    parsed, parseErr := strconv.ParseUint(digits, 16, 64)
    if parseErr != nil {
        return "", true, fmt.Errorf("unsupported catalog hexadecimal literal %q", text)
    }
    return strconv.FormatInt(int64(parsed), 10), true, nil
}
```

## Tests
Extended the table-driven `TestParseCatalogExpressionHexLiteral` in
`compat/sqlparse_test.go` with four new cases asserting the full AST
(`kind "integer"` + exact signed value): all-bits-set → `-1`, high-bit-only →
`-9223372036854775808`, max-positive → `9223372036854775807`, and small value
`0x10` → `16`. The pre-existing `>16 hex digits is rejected` error case was
left unchanged.

No pre-existing test asserted the old unsigned value for a high-bit-set
literal (the existing hex cases use `0x10` and `0XABCDEF`, both small and
unaffected by the sign reinterpretation), so no existing test was modified.

## Validation
```
$ go build ./... && go vet ./... && go test ./...
?  	example.com/sqlite-postgres-compat/cmd/compat-audit   [no test files]
?  	example.com/sqlite-postgres-compat/cmd/compat-copy    [no test files]
?  	example.com/sqlite-postgres-compat/cmd/compat-cutover [no test files]
?  	example.com/sqlite-postgres-compat/cmd/internal/cliout [no test files]
ok  	example.com/sqlite-postgres-compat/compat             1.923s
```

All green: build, vet, and tests pass.