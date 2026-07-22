package compat

import (
	"context"
	"strings"
	"testing"
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
	rawJSON := `{ "b": 123456789012345678901234567890, "a": 1 }`
	jsonValue, err := canonicalValue(JSONType, rawJSON)
	if err != nil {
		t.Fatal(err)
	}
	if jsonValue.Value != rawJSON {
		t.Fatalf("unexpected JSON %s", jsonValue.Value)
	}

	timestamp, err := canonicalValue(TimestampType, "2026-07-22T12:34:56.123456789-06:00")
	if err != nil {
		t.Fatal(err)
	}
	if timestamp.Value != "2026-07-22T18:34:56.123456789Z" {
		t.Fatalf("unexpected timestamp %s", timestamp.Value)
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
