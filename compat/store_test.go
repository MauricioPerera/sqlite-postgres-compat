package compat

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"
)

func TestSQLiteSnapshotRoundTrip(t *testing.T) {
	ctx := context.Background()
	schema := Schema{Tables: []Table{{
		Name: "records",
		Columns: []Column{
			{Name: "id", Type: Type{Family: UUIDType}},
			{Name: "enabled", Type: Type{Family: BooleanType}},
			{Name: "payload", Type: Type{Family: JSONType}},
		},
		Constraints: []Constraint{{Kind: PrimaryKey, Columns: []string{"id"}}},
	}}}

	source, err := OpenSQLite(Version{Major: 3}, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	if err := source.ApplySchema(ctx, schema); err != nil {
		t.Fatal(err)
	}
	if _, err := source.DB.Exec(`INSERT INTO records (id, enabled, payload) VALUES ('id-1', 1, '{"name":"one"}')`); err != nil {
		t.Fatal(err)
	}

	snapshot, err := source.ExportSnapshot(ctx, schema)
	if err != nil {
		t.Fatal(err)
	}
	destination, err := OpenSQLite(Version{Major: 3}, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer destination.Close()
	if err := destination.ImportSnapshot(ctx, snapshot); err != nil {
		t.Fatal(err)
	}

	copy, err := destination.ExportSnapshot(ctx, schema)
	if err != nil {
		t.Fatal(err)
	}
	if got := copy.Rows["records"][0]["payload"].Value; got != `{"name":"one"}` {
		t.Fatalf("unexpected payload %q", got)
	}
	if got := copy.Rows["records"][0]["enabled"].Value; got != "true" {
		t.Fatalf("unexpected boolean %q", got)
	}
}

func TestCanonicalJSONAndTimestampNormalization(t *testing.T) {
	// Two logically-equal JSON documents with different key order and spacing
	// must canonicalize to the same compact, key-sorted byte form. The previous
	// implementation stored the verbatim text, so SnapshotDigest flagged these as
	// non-equivalent (audit BUG 1).
	loose := `{ "b": 123456789012345678901234567890, "a": 1 }`
	tight := `{"a":1,"b":123456789012345678901234567890}`
	looseValue, err := canonicalValue(JSONType, loose)
	if err != nil {
		t.Fatal(err)
	}
	tightValue, err := canonicalValue(JSONType, tight)
	if err != nil {
		t.Fatal(err)
	}
	if looseValue.Value != tightValue.Value {
		t.Fatalf("expected canonical equality, got %q vs %q", looseValue.Value, tightValue.Value)
	}
	if looseValue.Value != tight {
		t.Fatalf("unexpected canonical JSON %q", looseValue.Value)
	}
	// High-precision numbers must survive canonicalization unchanged: UseNumber
	// keeps json.Number, so json.Marshal re-emits the original digits.
	precise, err := canonicalValue(JSONType, `{"pi":3.141592653589793238}`)
	if err != nil {
		t.Fatal(err)
	}
	if precise.Value != `{"pi":3.141592653589793238}` {
		t.Fatalf("high-precision number lost digits: %q", precise.Value)
	}
	// Invalid JSON must still error.
	if _, err := canonicalValue(JSONType, `{bad`); err == nil {
		t.Fatal("expected error for invalid JSON")
	}

	timestamp, err := canonicalValue(TimestampType, "2026-07-22T12:34:56.123456789-06:00")
	if err != nil {
		t.Fatal(err)
	}
	if timestamp.Value != "2026-07-22T18:34:56.123456789Z" {
		t.Fatalf("unexpected timestamp %s", timestamp.Value)
	}
}

func TestCanonicalTimestampPostgresFormats(t *testing.T) {
	// Postgres journals timestamp columns via CAST(col AS TEXT), which emits a
	// space separator, a short or long numeric offset (or none, read as UTC), and
	// an optional fractional component. canonicalValue must accept all of them
	// and normalize to RFC 3339 Nano UTC (audit BUG 2).
	wholeSecond := []string{
		"2026-07-22 16:06:34+00",
		"2026-07-22 16:06:34+00:00",
		"2026-07-22T16:06:34Z",
	}
	var first string
	for _, raw := range wholeSecond {
		value, err := canonicalValue(TimestampType, raw)
		if err != nil {
			t.Fatalf("canonicalValue(%q): %v", raw, err)
		}
		if value.Value != "2026-07-22T16:06:34Z" {
			t.Fatalf("canonicalValue(%q) = %q, want 2026-07-22T16:06:34Z", raw, value.Value)
		}
		if first == "" {
			first = value.Value
		} else if value.Value != first {
			t.Fatalf("canonicalValue(%q) = %q, want %q", raw, value.Value, first)
		}
	}

	fractional, err := canonicalValue(TimestampType, "2026-07-22 16:06:34.123456+00")
	if err != nil {
		t.Fatalf("canonicalValue(fractional): %v", err)
	}
	if fractional.Value != "2026-07-22T16:06:34.123456Z" {
		t.Fatalf("fractional = %q, want 2026-07-22T16:06:34.123456Z", fractional.Value)
	}

	if _, err := canonicalValue(TimestampType, "not-a-date"); err == nil {
		t.Fatal("expected error for non-timestamp text")
	}
}

func TestCanonicalFloatNormalization(t *testing.T) {
	// The capture journal and the reconstructed current row produce different
	// text for the same float ("1.0" vs "1"); canonicalValue must map both to a
	// single canonical form so rowsEqual stays byte-strict.
	onePointZero, err := canonicalValue(FloatType, "1.0")
	if err != nil {
		t.Fatal(err)
	}
	one, err := canonicalValue(FloatType, "1")
	if err != nil {
		t.Fatal(err)
	}
	if onePointZero.Value != one.Value {
		t.Fatalf("expected canonical equality for 1.0 and 1, got %q vs %q", onePointZero.Value, one.Value)
	}
	if onePointZero.Kind != FloatValue {
		t.Fatalf("unexpected kind %q", onePointZero.Kind)
	}
	if onePointZero.Value != "1" {
		t.Fatalf("unexpected canonical form %q", onePointZero.Value)
	}

	half, err := canonicalValue(FloatType, "1.5")
	if err != nil {
		t.Fatal(err)
	}
	if half.Value != "1.5" {
		t.Fatalf("expected 1.5 to be preserved, got %q", half.Value)
	}

	// A driver-supplied float64 must canonicalize identically to the captured text.
	fromFloat, err := canonicalValue(FloatType, float64(1))
	if err != nil {
		t.Fatal(err)
	}
	if fromFloat.Value != one.Value {
		t.Fatalf("expected float64(1) to match canonical 1, got %q vs %q", fromFloat.Value, one.Value)
	}

	if _, err := canonicalValue(FloatType, "not-a-number"); err == nil {
		t.Fatal("expected error for non-numeric float text")
	}
}

// TestCanonicalFloatRejectsInfNaN guards BAJA-2: the FloatType branch must
// reject non-finite values with a clear error instead of canonicalizing them to
// "+Inf"/"NaN", mirroring the finiteness gate the DecimalType branch keeps and
// how the rest of the layer refuses impossible values. Both the textual forms a
// driver/trigger may emit and the Go float64 shapes are rejected.
func TestCanonicalFloatRejectsInfNaN(t *testing.T) {
	for _, in := range []any{"inf", "+inf", "-inf", "Inf", "nan", "NaN", float64(math.Inf(1)), float64(math.Inf(-1)), math.NaN()} {
		if _, err := canonicalValue(FloatType, in); err == nil {
			t.Fatalf("expected error for non-finite float %v, got nil", in)
		}
	}
	// A finite float still canonicalizes normally.
	got, err := canonicalValue(FloatType, "1.5")
	if err != nil {
		t.Fatal(err)
	}
	if got.Value != "1.5" {
		t.Fatalf("finite float must still canonicalize: want 1.5, got %q", got.Value)
	}
}

func TestCanonicalDecimalReconcilesFloatStorage(t *testing.T) {
	// A DECIMAL value that SQLite stored as REAL surfaces two ways: as a Go
	// float64 from the destination driver, and as the capture trigger's
	// realDecimalMarker-prefixed printf('%!.17g') text. canonicalValue must map
	// both to one shortest form so rowsEqual does not raise a spurious
	// ConflictError. The marker prefix is what distinguishes a REAL-journaled
	// value from an arbitrary-precision TEXT decimal (which is preserved verbatim
	// — see TestCanonicalDecimalPreservesArbitraryPrecisionText).
	fromFloat, err := canonicalValue(DecimalType, 1.2345678901234567e+14)
	if err != nil {
		t.Fatal(err)
	}
	fromText, err := canonicalValue(DecimalType, realDecimalMarker+"123456789012345.67")
	if err != nil {
		t.Fatal(err)
	}
	if fromFloat.Value != fromText.Value {
		t.Fatalf("expected driver float64 and capture text to converge, got %q vs %q", fromFloat.Value, fromText.Value)
	}
	if fromFloat.Kind != DecimalValue {
		t.Fatalf("unexpected kind %q", fromFloat.Kind)
	}
	if fromFloat.Value != "1.2345678901234567e+14" {
		t.Fatalf("unexpected canonical decimal %q", fromFloat.Value)
	}

	// Arbitrary-precision decimals (18+ significant digits) never round-trip
	// through a single float64 and must be preserved verbatim.
	long := "12345678901234567890.123456789012345678"
	preserved, err := canonicalValue(DecimalType, long)
	if err != nil {
		t.Fatal(err)
	}
	if preserved.Value != long {
		t.Fatalf("arbitrary-precision decimal was altered: want %q, got %q", long, preserved.Value)
	}

	// A high-magnitude decimal that SQLite would render with a rounded CAST
	// ("99999999999999.984") is reconciled to the same form as the driver float64
	// when it arrives as REAL-stored (marker-prefixed) capture text, not left as a
	// divergent raw string.
	fromRounded, err := canonicalValue(DecimalType, realDecimalMarker+"99999999999999.984")
	if err != nil {
		t.Fatal(err)
	}
	fromDriver, err := canonicalValue(DecimalType, 9.999999999999998e+13)
	if err != nil {
		t.Fatal(err)
	}
	if fromRounded.Value != fromDriver.Value {
		t.Fatalf("expected rounded text and driver float64 to converge, got %q vs %q", fromRounded.Value, fromDriver.Value)
	}

	// Pure-integer decimals are preserved exactly (not rewritten as a float).
	integer, err := canonicalValue(DecimalType, "1234567890123456789")
	if err != nil {
		t.Fatal(err)
	}
	if integer.Value != "1234567890123456789" {
		t.Fatalf("integer-valued decimal must be preserved, got %q", integer.Value)
	}
}

// TestCanonicalDecimalPreservesArbitraryPrecisionText guards the ALTA-1 fix: an
// arbitrary-precision DECIMAL stored as TEXT (the canonical DDL mapping) must
// pass through canonicalValue byte-for-byte. The former isCompactFloatText
// heuristic rewrote any ≤17-significant-digit text that looked like a float,
// corrupting values that are not the round-trip representation of any float64
// (value change "0.10000000000000001" -> "0.1"), dropping scale ("1.50" -> "1.5"),
// and changing repr ("123456789012345.67" -> "1.2345678901234567e+14"). Only
// REAL-stored values (Go float64, or marker-prefixed capture text) are reconciled.
func TestCanonicalDecimalPreservesArbitraryPrecisionText(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"seventeen-digit non-round-trip", "0.10000000000000001"},
		{"scale-bearing", "1.50"},
		{"seventeen-digit magnitude", "123456789012345.67"},
		{"seventeen-nines integer", "99999999999999999"},
		{"leading zeros", "007"},
		{"trailing zero scale", "0.10"},
		{"thirty-eight-digit", "12345678901234567890.123456789012345678"},
		{"round-trip float text stays equal", "0.1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := canonicalValue(DecimalType, tc.in)
			if err != nil {
				t.Fatalf("canonicalValue(DecimalType, %q): %v", tc.in, err)
			}
			if got.Value != tc.in {
				t.Fatalf("TEXT-stored decimal was altered: want %q, got %q", tc.in, got.Value)
			}
			if got.Kind != DecimalValue {
				t.Fatalf("unexpected kind %q for %q", got.Kind, tc.in)
			}
		})
	}
}

// TestCanonicalDateHandlesTimeTimeFromLegacyNativeDate guards the ALTA fix from
// AUDIT5 §4.1. The SQLite source stores a date as TEXT and canonicalValue reads
// it as a string, yielding DateValue "2020-01-01". A destination created by an
// older tool version (before DateType mapped to TEXT) keeps a native PG DATE
// column, which pgx returns as a time.Time at midnight UTC. canonicalValue must
// fold that time.Time to the same date-only canonical form when the family is
// DateType, so a legacy native-DATE destination re-verified against the current
// source still converges instead of diverging as a TimestampValue. New
// destinations store DateType as TEXT and never reach the time.Time branch.
func TestCanonicalDateHandlesTimeTimeFromLegacyNativeDate(t *testing.T) {
	fromText, err := canonicalValue(DateType, "2020-01-01")
	if err != nil {
		t.Fatal(err)
	}
	if fromText.Kind != DateValue || fromText.Value != "2020-01-01" {
		t.Fatalf("text date = {%q, %q}, want {date, 2020-01-01}", fromText.Kind, fromText.Value)
	}
	fromTime, err := canonicalValue(DateType, time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if fromTime.Kind != DateValue || fromTime.Value != "2020-01-01" {
		t.Fatalf("time.Time date = {%q, %q}, want {date, 2020-01-01}", fromTime.Kind, fromTime.Value)
	}
	if fromText.Value != fromTime.Value {
		t.Fatalf("expected text and time.Time dates to converge, got %q vs %q", fromText.Value, fromTime.Value)
	}
	// A non-UTC offset must still canonicalize to the UTC date-only form.
	fromOffset, err := canonicalValue(DateType, time.Date(2020, 1, 1, 2, 30, 0, 0, time.FixedZone("UTC+2", 2*3600)))
	if err != nil {
		t.Fatal(err)
	}
	if fromOffset.Value != "2020-01-01" {
		t.Fatalf("offset date = %q, want 2020-01-01", fromOffset.Value)
	}
	// The generic time.Time path must remain a TimestampValue for non-date families.
	ts, err := canonicalValue(TimestampType, time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if ts.Kind != TimestampValue || ts.Value != "2020-01-01T00:00:00Z" {
		t.Fatalf("timestamp = {%q, %q}, want {timestamp, 2020-01-01T00:00:00Z}", ts.Kind, ts.Value)
	}
}

func TestSQLiteForeignKeysAreEnabled(t *testing.T) {
	store, err := OpenSQLite(Version{Major: 3}, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var enabled int
	if err := store.DB.QueryRow("PRAGMA foreign_keys").Scan(&enabled); err != nil {
		t.Fatal(err)
	}
	if enabled != 1 {
		t.Fatal("foreign keys are not enabled")
	}
	if !strings.Contains(sqliteDSN("file:test.db?cache=shared"), "&_pragma=foreign_keys(1)") {
		t.Fatal("pragma was not appended to DSN with existing query parameters")
	}
}
