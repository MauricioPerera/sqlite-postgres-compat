# FIX-B2 Report

Two audit findings corrected. Only files in the allowed set were touched:
`compat/replicate.go`, `compat/replicate_test.go`, `compat/spec.go`,
`compat/spec_test.go`.

## Finding 1 — `ConflictError.Error()` dropped diagnostic values

`compat/replicate.go:22` — `Error()` only printed table + primary key and
discarded the `Expected`/`Actual` fields, so a real conflict never said WHAT
differed.

### Change (message only; struct fields and type unchanged)

```go
func (err *ConflictError) Error() string {
	// Render Expected/Actual as compact, key-sorted JSON so the diagnostic
	// shows exactly which values differ, not just the table and primary key.
	expected, _ := json.Marshal(err.Expected)
	actual, _ := json.Marshal(err.Actual)
	return fmt.Sprintf("replication conflict on %s primary key %v: expected %s, actual %s",
		err.Table, err.PrimaryKey, expected, actual)
}
```

`encoding/json` and `fmt` were already imported. No struct field, type, or
public signature changed.

### New test — `TestConflictErrorReportsExpectedAndActualValues`

Fires a real update conflict (same pattern as the preexisting
`TestApplyChangesDetectsConflictAndRollsBack`) and asserts `err.Error()`
contains the table, the primary key value, and a representation of both the
expected (`remote-before`) and actual (`local`) differing values via
`strings.Contains`.

## Finding 2 — `Version.Valid()` accepted the all-zero value

`compat/spec.go:23` — `Major >= 0 && Minor >= 0 && Patch >= 0` made the zero
value `Version{0,0,0}` "valid". Replication dedup uses `Version.String()` as
part of the key (`replicate.go:112,123`), so two distinct real sources with a
zero version would collide. `0.9.0` and `1.0.0` are legitimate; only the
totally-zero value is invalid.

### Change

```go
func (v Version) Valid() bool {
	// The zero value Version{0,0,0} is invalid: it carries no real version
	// information and is unsafe to use as a replication dedup key (two distinct
	// zero-version sources would collide). A truly zero version is rejected,
	// negative components remain rejected, and versions like 0.9.0 or 1.0.0
	// stay valid.
	if v.Major == 0 && v.Minor == 0 && v.Patch == 0 {
		return false
	}
	return v.Major >= 0 && v.Minor >= 0 && v.Patch >= 0
}
```

### New tests in `compat/spec_test.go`

- `TestVersionValidRejectsAllZero` — `Version{0,0,0}.Valid() == false`
- `TestVersionValidAcceptsPartialZero` — `0,9,0`, `1,0,0`, `0,0,1` all valid
- `TestVersionValidRejectsNegatives` — `-1,0,0`, `0,-1,0`, `1,2,-3` all invalid
- `TestTargetValidateRejectsZeroVersion` — `Target{Engine: SQLite, Version: {0,0,0}}` fails `Validate()`
- `TestContractValidateRejectsZeroVersion` — contract with a zero version fails `Validate()`

## Side-effect check on preexisting tests

Before any change the suite was green. No preexisting test constructs a
`Target`/`Contract` with a zero `Version` (all use `Major: 3`, `Major: 17`,
or `Major: 1` with non-zero minor). `TestContractRejectsUnknownEngine` uses
an unknown engine that fails the engine check before reaching the version
check, so it is unaffected. No preexisting test was modified or removed.

## Definition of done — real output

### `go build ./... && go vet ./...`

```
?  	example.com/sqlite-postgres-compat/cmd/compat-audit	[no test files]
?  	example.com/sqlite-postgres-compat/cmd/compat-copy	[no test files]
ok  	example.com/sqlite-postgres-compat/compat	2.234s
```

### `go test ./compat/ -run '<new tests>' -v`

```
=== RUN   TestConflictErrorReportsExpectedAndActualValues
--- PASS: TestConflictErrorReportsExpectedAndActualValues (0.00s)
=== RUN   TestVersionValidRejectsAllZero
--- PASS: TestVersionValidRejectsAllZero (0.00s)
=== RUN   TestVersionValidAcceptsPartialZero
--- PASS: TestVersionValidAcceptsPartialZero (0.00s)
=== RUN   TestVersionValidRejectsNegatives
--- PASS: TestVersionValidRejectsNegatives (0.00s)
=== RUN   TestTargetValidateRejectsZeroVersion
--- PASS: TestTargetValidateRejectsZeroVersion (0.00s)
=== RUN   TestContractValidateRejectsZeroVersion
--- PASS: TestContractValidateRejectsZeroVersion (0.00s)
PASS
ok  	example.com/sqlite-postgres-compat/compat	2.093s
```

### `go build ./... && go vet ./... && go test ./...`

```
?  	example.com/sqlite-postgres-compat/cmd/compat-audit	[no test files]
?  	example.com/sqlite-postgres-compat/cmd/compat-copy	[no test files]
ok  	example.com/sqlite-postgres-compat/compat	2.234s
```

All green. No preexisting tests modified or removed. No files outside the
allowed set were touched.