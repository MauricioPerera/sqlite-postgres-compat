# FIX-M3 — JSON canonicalization + Postgres timestamp formats

Scope: `compat/store.go` and `compat/store_test.go` only. No other files touched.

## What changed

### BUG 1 — JSONType stored verbatim text (`compat/store.go`, JSONType branch)
`canonicalValue` already validated and decoded the JSON (with `UseNumber`), but returned the
**original** text. `SnapshotDigest`/`VerifySnapshots` compare by byte-for-byte marshaling, so the
same logical object with different key order or whitespace produced different digests (false
non-equivalence).

**Fix:** after the existing decode + trailing-data validation, re-serialize the decoded document
with `json.Marshal(document)` and store that. Go's `json.Marshal` emits map keys in sorted order,
compact, with no extra spacing, so every logically-equal document converges on one canonical byte
form. `UseNumber` keeps numbers as `json.Number`, so `json.Marshal` re-emits the **original digits**
of high-precision numbers with no `float64` precision loss.

### BUG 2 — TimestampType only accepted RFC3339Nano (`compat/store.go`, TimestampType branch)
The capture layer journals Postgres timestamp columns via `CAST(col AS TEXT)`, which emits a space
separator, a short (`+00`) or long (`+00:00`) numeric offset (or none), and an optional fractional
component — e.g. `2026-07-22 16:06:34+00`. `time.Parse(time.RFC3339Nano, ...)` rejected these and
aborted the Postgres→SQLite change stream.

**Fix:** replaced the single `time.Parse` with a small `parseTimestamp` helper that tries a list
of layouts (`timestampFormats`) and normalizes every match to UTC RFC3339Nano, as before:

- `time.RFC3339Nano` — canonical snapshot form (`2006-01-02T15:04:05.999999999Z07:00`)
- `2006-01-02 15:04:05.999999999Z07:00` — space separator, long offset
- `2006-01-02 15:04:05.999999999Z07` — space separator, short offset
- `2006-01-02 15:04:05.999999999` — space separator, no offset (parsed as UTC)

Go's optional-fraction token (`.999999999`) makes each layout match both whole-second and
fractional inputs, so 4 layouts cover the full Postgres family. Unparseable text still returns an
explicit error (unchanged behavior).

No public signatures changed. `normalizeFloat`, and the Boolean/UUID/Decimal/Date/Integer
branches are untouched.

## Existing test adjusted (declared)

`TestCanonicalJSONAndTimestampNormalization` previously asserted the buggy verbatim behavior:

```go
rawJSON := `{ "b": 123456789012345678901234567890, "a": 1 }`
jsonValue, err := canonicalValue(JSONType, rawJSON)
...
if jsonValue.Value != rawJSON {   // asserted verbatim (buggy)
    t.Fatalf("unexpected JSON %s", jsonValue.Value)
}
```

That assertion codified BUG 1, so it had to change. It now asserts the canonical form and adds a
high-precision-number check and an invalid-JSON check. The timestamp half of that test is
unchanged and still passes. Diff of the changed assertion:

```diff
-	rawJSON := `{ "b": 123456789012345678901234567890, "a": 1 }`
-	jsonValue, err := canonicalValue(JSONType, rawJSON)
-	if err != nil {
-		t.Fatal(err)
-	}
-	if jsonValue.Value != rawJSON {
-		t.Fatalf("unexpected JSON %s", jsonValue.Value)
-	}
+	loose := `{ "b": 123456789012345678901234567890, "a": 1 }`
+	tight := `{"a":1,"b":123456789012345678901234567890}`
+	looseValue, err := canonicalValue(JSONType, loose)
+	...
+	tightValue, err := canonicalValue(JSONType, tight)
+	...
+	if looseValue.Value != tightValue.Value {
+		t.Fatalf("expected canonical equality, got %q vs %q", looseValue.Value, tightValue.Value)
+	}
+	if looseValue.Value != tight {
+		t.Fatalf("unexpected canonical JSON %q", looseValue.Value)
+	}
+	precise, err := canonicalValue(JSONType, `{"pi":3.141592653589793238}`)
+	...
+	if precise.Value != `{"pi":3.141592653589793238}` {
+		t.Fatalf("high-precision number lost digits: %q", precise.Value)
+	}
+	if _, err := canonicalValue(JSONType, `{bad`); err == nil {
+		t.Fatal("expected error for invalid JSON")
+	}
```

`TestSQLiteSnapshotRoundTrip` was **not** modified: its `{"name":"one"}` payload is already
canonical and survives unchanged. No other preexisting test was modified or deleted.

## New test added

`TestCanonicalTimestampPostgresFormats` (`compat/store_test.go`) asserts:
- `2026-07-22 16:06:34+00`, `2026-07-22 16:06:34+00:00`, `2026-07-22T16:06:34Z` all normalize to
  `2026-07-22T16:06:34Z` (the same whole-second instant in UTC RFC3339Nano).
- `2026-07-22 16:06:34.123456+00` normalizes to `2026-07-22T16:06:34.123456Z` (accepted; it is a
  distinct instant because of its fractional component — see Trade-offs).
- `not-a-date` returns an error.

Note on the definition-of-done wording "los cuatro normalizan al mismo instante": the four listed
inputs cannot all share one instant, because `16:06:34.123456` differs from `16:06:34` by .123456s.
The test asserts the literal-correct interpretation: each input is accepted and normalizes to its
correct RFC3339Nano-UTC instant, and the three whole-second inputs are mutually equal.

## Real output

```
$ go build ./... && go vet ./... && go test -count=1 ./...
?       example.com/sqlite-postgres-compat/cmd/compat-audit      [no test files]
?       example.com/sqlite-postgres-compat/cmd/compat-copy       [no test files]
ok      example.com/sqlite-postgres-compat/compat        2.336s
```

New/adjusted tests, verbose:

```
$ go test -count=1 -v ./compat/ -run 'TestCanonicalJSONAndTimestampNormalization|TestCanonicalTimestampPostgresFormats|TestSQLiteSnapshotRoundTrip'
=== RUN   TestSQLiteSnapshotRoundTrip
--- PASS: TestSQLiteSnapshotRoundTrip (0.00s)
=== RUN   TestCanonicalJSONAndTimestampNormalization
--- PASS: TestCanonicalJSONAndTimestampNormalization (0.01s)
=== RUN   TestCanonicalTimestampPostgresFormats
--- PASS: TestCanonicalTimestampPostgresFormats (0.00s)
PASS
ok      example.com/sqlite-postgres-compat/compat        2.301s
```

## Note on an intermittent preexisting failure

During this work, `TestSearchTextSkipsNullColumns` (in `compat/runtime_test.go`, part of another
dev's uncommitted WIP — not a file in scope) failed once, then passed on every retry. Verified it
is not caused by these changes:

- With my changes reverted (other WIP intact): the full `./compat/` suite passed 5/5 clean runs.
- With my changes: 10/10 clean runs and 3/3 cache-free runs passed.

The single failure was an order/state-dependent flake in the other dev's `runtime_test.go` WIP,
independent of the JSON/timestamp canonicalization in `store.go`. No file outside
`compat/store.go` / `compat/store_test.go` was touched.

## Trade-offs

- `json.Marshal` HTML-escapes `<`, `>`, `&` (e.g. `<`) by default. Both snapshot and journal
  sides canonicalize identically, so digest equivalence is unaffected; a JSON string containing
  those characters now stores the escaped form. This follows the explicit instruction to use
  Go's `json.Marshal` for canonicalization.
- The four Postgres-format inputs in the definition of done are not a single instant (one has
  fractional seconds); the test asserts each normalizes correctly rather than forcing equality.
- Numeric offset forms only; no named zones (e.g. `UTC`, `EST`) are accepted — Postgres
  `CAST(... AS TEXT)` does not emit those, so no behavior change for the journal path.