package compat

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestApplyChangesIsTransactionalAndIdempotent(t *testing.T) {
	ctx := context.Background()
	schema := replicationTestSchema("replication_items")
	store, err := OpenSQLite(Version{Major: 3}, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.ApplySchema(ctx, schema); err != nil {
		t.Fatal(err)
	}
	source := Target{Engine: Postgres, Version: Version{Major: 17}}
	before := Row{"id": {Kind: IntegerValue, Value: "1"}, "title": {Kind: TextValue, Value: "first"}}
	after := Row{"id": {Kind: IntegerValue, Value: "1"}, "title": {Kind: TextValue, Value: "updated"}}
	changes := []Change{
		{Source: source, Sequence: 1, Kind: Insert, Table: "replication_items", PrimaryKey: Row{"id": before["id"]}, After: before},
		{Source: source, Sequence: 2, Kind: Update, Table: "replication_items", PrimaryKey: Row{"id": before["id"]}, Before: before, After: after},
	}
	if err := store.ApplyChanges(ctx, schema, changes); err != nil {
		t.Fatal(err)
	}
	if err := store.ApplyChanges(ctx, schema, changes); err != nil {
		t.Fatal("reapplying an identical stream must be idempotent:", err)
	}
	var title string
	if err := store.DB.QueryRow(`SELECT title FROM replication_items WHERE id = 1`).Scan(&title); err != nil {
		t.Fatal(err)
	}
	if title != "updated" {
		t.Fatalf("unexpected title %q", title)
	}
}

func TestApplyChangesDetectsConflictAndRollsBack(t *testing.T) {
	ctx := context.Background()
	schema := replicationTestSchema("conflict_items")
	store, err := OpenSQLite(Version{Major: 3}, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.ApplySchema(ctx, schema); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB.Exec(`INSERT INTO conflict_items (id, title) VALUES (1, 'local')`); err != nil {
		t.Fatal(err)
	}
	change := Change{
		Source:     Target{Engine: Postgres, Version: Version{Major: 17}},
		Sequence:   1,
		Kind:       Update,
		Table:      "conflict_items",
		PrimaryKey: Row{"id": {Kind: IntegerValue, Value: "1"}},
		Before:     Row{"id": {Kind: IntegerValue, Value: "1"}, "title": {Kind: TextValue, Value: "remote-before"}},
		After:      Row{"id": {Kind: IntegerValue, Value: "1"}, "title": {Kind: TextValue, Value: "remote-after"}},
	}
	err = store.ApplyChanges(ctx, schema, []Change{change})
	var conflict *ConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("expected ConflictError, got %v", err)
	}
	var title string
	if err := store.DB.QueryRow(`SELECT title FROM conflict_items WHERE id = 1`).Scan(&title); err != nil {
		t.Fatal(err)
	}
	if title != "local" {
		t.Fatalf("conflicting row was modified: %q", title)
	}
}

func TestConflictErrorReportsExpectedAndActualValues(t *testing.T) {
	ctx := context.Background()
	schema := replicationTestSchema("conflict_report_items")
	store, err := OpenSQLite(Version{Major: 3}, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.ApplySchema(ctx, schema); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB.Exec(`INSERT INTO conflict_report_items (id, title) VALUES (1, 'local')`); err != nil {
		t.Fatal(err)
	}
	change := Change{
		Source:     Target{Engine: Postgres, Version: Version{Major: 17}},
		Sequence:   1,
		Kind:       Update,
		Table:      "conflict_report_items",
		PrimaryKey: Row{"id": {Kind: IntegerValue, Value: "1"}},
		Before:     Row{"id": {Kind: IntegerValue, Value: "1"}, "title": {Kind: TextValue, Value: "remote-before"}},
		After:      Row{"id": {Kind: IntegerValue, Value: "1"}, "title": {Kind: TextValue, Value: "remote-after"}},
	}
	err = store.ApplyChanges(ctx, schema, []Change{change})
	var conflict *ConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("expected ConflictError, got %v", err)
	}
	message := err.Error()
	for _, want := range []string{
		"conflict_report_items", // table
		"1",                     // primary key value
		"remote-before",         // expected value that differs
		"local",                 // actual value that differs
	} {
		if !strings.Contains(message, want) {
			t.Fatalf("conflict error missing %q in message:\n%s", want, message)
		}
	}
}

func TestApplyChangesReplicatesFloatColumn(t *testing.T) {
	ctx := context.Background()
	schema := floatTestSchema("float_items")

	source, err := OpenSQLite(Version{Major: 3}, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	if err := source.ApplySchema(ctx, schema); err != nil {
		t.Fatal(err)
	}
	if err := source.InstallChangeCapture(ctx, schema); err != nil {
		t.Fatal(err)
	}

	destination, err := OpenSQLite(Version{Major: 3}, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer destination.Close()
	if err := destination.ApplySchema(ctx, schema); err != nil {
		t.Fatal(err)
	}
	if err := destination.InstallChangeCapture(ctx, schema); err != nil {
		t.Fatal(err)
	}

	// SQLite stores REAL values; CAST(1.0 AS TEXT) yields "1.0" in the journal
	// while the reconstructed row formats as "1", which previously caused
	// spurious conflicts on update/delete and left deletes un-applied.
	if _, err := source.DB.Exec(`INSERT INTO float_items (id, flt) VALUES (1, 1.0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := source.DB.Exec(`UPDATE float_items SET flt = 1.5 WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	if _, err := source.DB.Exec(`DELETE FROM float_items WHERE id = 1`); err != nil {
		t.Fatal(err)
	}

	changes, err := source.ReadCapturedChanges(ctx, schema, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 3 {
		t.Fatalf("expected 3 captured changes, got %d", len(changes))
	}
	if err := destination.ApplyChanges(ctx, schema, changes); err != nil {
		t.Fatalf("float replication must not conflict: %v", err)
	}

	var count int
	if err := destination.DB.QueryRow(`SELECT count(*) FROM float_items WHERE id = 1`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("expected row removed after delete, found %d", count)
	}

	// Replaying the same stream must stay idempotent once the row is gone.
	if err := destination.ApplyChanges(ctx, schema, changes); err != nil {
		t.Fatalf("reapplying float stream must be idempotent: %v", err)
	}
}

func TestApplyChangesReplicatesFloatUpdateApplied(t *testing.T) {
	ctx := context.Background()
	schema := floatTestSchema("float_update_items")

	source, err := OpenSQLite(Version{Major: 3}, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	if err := source.ApplySchema(ctx, schema); err != nil {
		t.Fatal(err)
	}
	if err := source.InstallChangeCapture(ctx, schema); err != nil {
		t.Fatal(err)
	}

	destination, err := OpenSQLite(Version{Major: 3}, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer destination.Close()
	if err := destination.ApplySchema(ctx, schema); err != nil {
		t.Fatal(err)
	}
	if err := destination.InstallChangeCapture(ctx, schema); err != nil {
		t.Fatal(err)
	}

	if _, err := source.DB.Exec(`INSERT INTO float_update_items (id, flt) VALUES (1, 1.0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := source.DB.Exec(`UPDATE float_update_items SET flt = 2.5 WHERE id = 1`); err != nil {
		t.Fatal(err)
	}

	changes, err := source.ReadCapturedChanges(ctx, schema, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	// Apply only insert+update so the row survives and the update is observable.
	if err := destination.ApplyChanges(ctx, schema, changes[:2]); err != nil {
		t.Fatalf("float update must not conflict: %v", err)
	}

	var flt float64
	if err := destination.DB.QueryRow(`SELECT flt FROM float_update_items WHERE id = 1`).Scan(&flt); err != nil {
		t.Fatal(err)
	}
	if flt != 2.5 {
		t.Fatalf("expected updated float 2.5, got %v", flt)
	}
}

func floatTestSchema(name string) Schema {
	return Schema{Tables: []Table{{
		Name: name,
		Columns: []Column{
			{Name: "id", Type: Type{Family: IntegerType}},
			{Name: "flt", Type: Type{Family: FloatType}},
		},
		Constraints: []Constraint{{Kind: PrimaryKey, Columns: []string{"id"}}},
	}}}
}

func replicationTestSchema(name string) Schema {
	return Schema{Tables: []Table{{
		Name: name,
		Columns: []Column{
			{Name: "id", Type: Type{Family: IntegerType}},
			{Name: "title", Type: Type{Family: TextType}},
		},
		Constraints: []Constraint{{Kind: PrimaryKey, Columns: []string{"id"}}},
	}}}
}

// decimalTestSchema declares a DECIMAL column so the capture trigger uses the
// typeof-gated printf path for REAL storage and the raw CAST path for TEXT.
func decimalTestSchema(name string) Schema {
	return Schema{Tables: []Table{{
		Name: name,
		Columns: []Column{
			{Name: "id", Type: Type{Family: IntegerType}},
			{Name: "amount", Type: Type{Family: DecimalType, Arguments: []int{38, 18}}},
		},
		Constraints: []Constraint{{Kind: PrimaryKey, Columns: []string{"id"}}},
	}}}
}

// TestApplyChangesPreservesHighPrecisionFloat guards against the silent float
// precision loss of AUDIT2-B Hallazgo 1: a FLOAT value whose decimal form needs
// more than 15 significant digits must reach the destination with its exact
// canonical value, without conflict, across insert and update.
func TestApplyChangesPreservesHighPrecisionFloat(t *testing.T) {
	ctx := context.Background()
	schema := floatTestSchema("float_precision_items")

	source, err := OpenSQLite(Version{Major: 3}, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	if err := source.ApplySchema(ctx, schema); err != nil {
		t.Fatal(err)
	}
	if err := source.InstallChangeCapture(ctx, schema); err != nil {
		t.Fatal(err)
	}
	destination, err := OpenSQLite(Version{Major: 3}, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer destination.Close()
	if err := destination.ApplySchema(ctx, schema); err != nil {
		t.Fatal(err)
	}
	if err := destination.InstallChangeCapture(ctx, schema); err != nil {
		t.Fatal(err)
	}

	const value = 1.2345678901234567e+14
	if _, err := source.DB.Exec(`INSERT INTO float_precision_items (id, flt) VALUES (1, ?)`, value); err != nil {
		t.Fatal(err)
	}
	// An update to the same high-precision value exercises the Before/After
	// comparison path that previously diverged on capture-vs-driver formatting.
	if _, err := source.DB.Exec(`UPDATE float_precision_items SET flt = ? WHERE id = 1`, value); err != nil {
		t.Fatal(err)
	}

	changes, err := source.ReadCapturedChanges(ctx, schema, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if err := destination.ApplyChanges(ctx, schema, changes); err != nil {
		t.Fatalf("high-precision float replication must not conflict: %v", err)
	}

	// The canonical value arriving at the destination must equal the canonical
	// value of the source's own stored float, byte-for-byte, with no precision
	// loss.
	sourceSnapshot, err := source.ExportSnapshot(ctx, schema)
	if err != nil {
		t.Fatal(err)
	}
	destinationSnapshot, err := destination.ExportSnapshot(ctx, schema)
	if err != nil {
		t.Fatal(err)
	}
	sourceCanonical := sourceSnapshot.Rows["float_precision_items"][0]["flt"].Value
	destinationCanonical := destinationSnapshot.Rows["float_precision_items"][0]["flt"].Value
	if sourceCanonical != destinationCanonical {
		t.Fatalf("silent float precision loss: source %q != destination %q", sourceCanonical, destinationCanonical)
	}
	if destinationCanonical != "1.2345678901234567e+14" {
		t.Fatalf("expected exact canonical 1.2345678901234567e+14, got %q", destinationCanonical)
	}

	// Replaying the stream must stay idempotent once the row matches.
	if err := destination.ApplyChanges(ctx, schema, changes); err != nil {
		t.Fatalf("reapplying high-precision float stream must be idempotent: %v", err)
	}
}

// TestApplyChangesDecimalRealStorageNoSpuriousConflict guards against the
// spurious ConflictError of AUDIT2-B Hallazgo 2. A native NUMERIC-affinity table
// stores fractional decimals as REAL, so the old CAST(col AS TEXT) capture
// truncated the journal while the destination driver reconstructed the full
// float64. With the typeof-gated printf capture and the float reconciliation in
// canonicalValue, replication must apply without a spurious conflict.
func TestApplyChangesDecimalRealStorageNoSpuriousConflict(t *testing.T) {
	ctx := context.Background()
	schema := decimalTestSchema("decimal_real_items")

	openNumeric := func() *Store {
		store, err := OpenSQLite(Version{Major: 3}, ":memory:")
		if err != nil {
			t.Fatal(err)
		}
		// Native NUMERIC affinity: fractional values store as REAL, reproducing
		// the AUDIT2-B scenario. The capture is installed from the DecimalType
		// schema so the trigger emits the typeof-gated printf form.
		if _, err := store.DB.Exec(`CREATE TABLE decimal_real_items (id INTEGER PRIMARY KEY, amount NUMERIC(38,18))`); err != nil {
			t.Fatal(err)
		}
		if err := store.InstallChangeCapture(ctx, schema); err != nil {
			t.Fatal(err)
		}
		return store
	}
	source := openNumeric()
	defer source.Close()
	destination := openNumeric()
	defer destination.Close()

	if _, err := source.DB.Exec(`INSERT INTO decimal_real_items (id, amount) VALUES (1, ?)`, 123456789012345.6789); err != nil {
		t.Fatal(err)
	}
	if _, err := source.DB.Exec(`UPDATE decimal_real_items SET amount = ? WHERE id = 1`, 99999999999999.99); err != nil {
		t.Fatal(err)
	}

	changes, err := source.ReadCapturedChanges(ctx, schema, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 2 {
		t.Fatalf("expected 2 captured changes, got %d", len(changes))
	}
	if err := destination.ApplyChanges(ctx, schema, changes); err != nil {
		t.Fatalf("REAL-stored decimal replication must not raise a spurious conflict: %v", err)
	}
	// Replaying the stream must stay idempotent.
	if err := destination.ApplyChanges(ctx, schema, changes); err != nil {
		t.Fatalf("reapplying REAL-stored decimal stream must be idempotent: %v", err)
	}
}

// TestApplyChangesPreservesArbitraryPrecisionDecimal is the non-regression
// guard for the arbitrary-precision guarantee: a DECIMAL value stored as TEXT
// (the canonical DDL mapping) must arrive at the destination byte-identical,
// even though the DecimalType branch now reconciles float storage.
func TestApplyChangesPreservesArbitraryPrecisionDecimal(t *testing.T) {
	ctx := context.Background()
	schema := decimalTestSchema("decimal_text_items")

	source, err := OpenSQLite(Version{Major: 3}, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	if err := source.ApplySchema(ctx, schema); err != nil {
		t.Fatal(err)
	}
	if err := source.InstallChangeCapture(ctx, schema); err != nil {
		t.Fatal(err)
	}
	destination, err := OpenSQLite(Version{Major: 3}, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer destination.Close()
	if err := destination.ApplySchema(ctx, schema); err != nil {
		t.Fatal(err)
	}

	want := "12345678901234567890.123456789012345678"
	if _, err := source.DB.ExecContext(ctx, `INSERT INTO decimal_text_items (id, amount) VALUES (?, ?)`, 1, want); err != nil {
		t.Fatal(err)
	}
	changes, err := source.ReadCapturedChanges(ctx, schema, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if err := destination.ApplyChanges(ctx, schema, changes); err != nil {
		t.Fatalf("arbitrary-precision decimal replication must apply: %v", err)
	}
	var got string
	if err := destination.DB.QueryRow(`SELECT amount FROM decimal_text_items WHERE id = 1`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("arbitrary-precision decimal was not preserved byte-identical: want %q, got %q", want, got)
	}
}

// TestApplyChangesTolerantSkipsInsertAlreadyPresent covers the overlap window
// where an insert journaled after capture-install already traveled inside the
// snapshot: the row exists and equals the change's after state, so the tolerant
// mode treats it as already applied instead of failing on the unique constraint.
func TestApplyChangesTolerantSkipsInsertAlreadyPresent(t *testing.T) {
	ctx := context.Background()
	schema := replicationTestSchema("tolerant_insert_items")
	store, err := OpenSQLite(Version{Major: 3}, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.ApplySchema(ctx, schema); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB.Exec(`INSERT INTO tolerant_insert_items (id, title) VALUES (1, 'first')`); err != nil {
		t.Fatal(err)
	}
	change := Change{
		Source:     Target{Engine: Postgres, Version: Version{Major: 17}},
		Sequence:   1,
		Kind:       Insert,
		Table:      "tolerant_insert_items",
		PrimaryKey: Row{"id": {Kind: IntegerValue, Value: "1"}},
		After:      Row{"id": {Kind: IntegerValue, Value: "1"}, "title": {Kind: TextValue, Value: "first"}},
	}
	if err := store.ApplyChangesTolerant(ctx, schema, []Change{change}); err != nil {
		t.Fatalf("tolerant insert of an already-present identical row must succeed: %v", err)
	}
	var title string
	if err := store.DB.QueryRow(`SELECT title FROM tolerant_insert_items WHERE id = 1`).Scan(&title); err != nil {
		t.Fatal(err)
	}
	if title != "first" {
		t.Fatalf("unexpected title %q", title)
	}
}

// TestApplyChangesTolerantSkipsUpdateWhenAfterMatchesActual covers an update
// whose Before no longer matches the destination (the snapshot already carried
// the after state) but whose after state equals the current row: the tolerant
// mode treats it as already applied rather than raising a ConflictError.
func TestApplyChangesTolerantSkipsUpdateWhenAfterMatchesActual(t *testing.T) {
	ctx := context.Background()
	schema := replicationTestSchema("tolerant_update_items")
	store, err := OpenSQLite(Version{Major: 3}, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.ApplySchema(ctx, schema); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB.Exec(`INSERT INTO tolerant_update_items (id, title) VALUES (1, 'updated')`); err != nil {
		t.Fatal(err)
	}
	change := Change{
		Source:     Target{Engine: Postgres, Version: Version{Major: 17}},
		Sequence:   1,
		Kind:       Update,
		Table:      "tolerant_update_items",
		PrimaryKey: Row{"id": {Kind: IntegerValue, Value: "1"}},
		Before:     Row{"id": {Kind: IntegerValue, Value: "1"}, "title": {Kind: TextValue, Value: "stale-before"}},
		After:      Row{"id": {Kind: IntegerValue, Value: "1"}, "title": {Kind: TextValue, Value: "updated"}},
	}
	if err := store.ApplyChangesTolerant(ctx, schema, []Change{change}); err != nil {
		t.Fatalf("tolerant update whose after state matches actual must succeed: %v", err)
	}
	var title string
	if err := store.DB.QueryRow(`SELECT title FROM tolerant_update_items WHERE id = 1`).Scan(&title); err != nil {
		t.Fatal(err)
	}
	if title != "updated" {
		t.Fatalf("unexpected title %q", title)
	}
}

// TestApplyChangesTolerantStillDetectsGenuineConflict confirms the tolerant mode
// is not a bypass: when the destination diverges from the change's final state
// and Before no longer matches either, a strict ConflictError is raised.
func TestApplyChangesTolerantStillDetectsGenuineConflict(t *testing.T) {
	ctx := context.Background()
	schema := replicationTestSchema("tolerant_conflict_items")
	store, err := OpenSQLite(Version{Major: 3}, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.ApplySchema(ctx, schema); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB.Exec(`INSERT INTO tolerant_conflict_items (id, title) VALUES (1, 'local')`); err != nil {
		t.Fatal(err)
	}
	change := Change{
		Source:     Target{Engine: Postgres, Version: Version{Major: 17}},
		Sequence:   1,
		Kind:       Update,
		Table:      "tolerant_conflict_items",
		PrimaryKey: Row{"id": {Kind: IntegerValue, Value: "1"}},
		Before:     Row{"id": {Kind: IntegerValue, Value: "1"}, "title": {Kind: TextValue, Value: "remote-before"}},
		After:      Row{"id": {Kind: IntegerValue, Value: "1"}, "title": {Kind: TextValue, Value: "remote-after"}},
	}
	err = store.ApplyChangesTolerant(ctx, schema, []Change{change})
	var conflict *ConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("expected ConflictError for genuine divergence, got %v", err)
	}
}

// TestApplyChangesTolerantDedupStillIdempotent confirms the per-source dedup
// table still governs reapplication in tolerant mode: a stream applied twice
// does not reprocess changes the first pass recorded.
func TestApplyChangesTolerantDedupStillIdempotent(t *testing.T) {
	ctx := context.Background()
	schema := replicationTestSchema("tolerant_dedup_items")
	store, err := OpenSQLite(Version{Major: 3}, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.ApplySchema(ctx, schema); err != nil {
		t.Fatal(err)
	}
	source := Target{Engine: Postgres, Version: Version{Major: 17}}
	changes := []Change{
		{Source: source, Sequence: 1, Kind: Insert, Table: "tolerant_dedup_items", PrimaryKey: Row{"id": {Kind: IntegerValue, Value: "1"}}, After: Row{"id": {Kind: IntegerValue, Value: "1"}, "title": {Kind: TextValue, Value: "first"}}},
		{Source: source, Sequence: 2, Kind: Update, Table: "tolerant_dedup_items", PrimaryKey: Row{"id": {Kind: IntegerValue, Value: "1"}}, Before: Row{"id": {Kind: IntegerValue, Value: "1"}, "title": {Kind: TextValue, Value: "first"}}, After: Row{"id": {Kind: IntegerValue, Value: "1"}, "title": {Kind: TextValue, Value: "updated"}}},
	}
	if err := store.ApplyChangesTolerant(ctx, schema, changes); err != nil {
		t.Fatal(err)
	}
	if err := store.ApplyChangesTolerant(ctx, schema, changes); err != nil {
		t.Fatal("reapplying a tolerant stream must be idempotent:", err)
	}
	var title string
	if err := store.DB.QueryRow(`SELECT title FROM tolerant_dedup_items WHERE id = 1`).Scan(&title); err != nil {
		t.Fatal(err)
	}
	if title != "updated" {
		t.Fatalf("unexpected title %q", title)
	}
}

// TestApplyChangesRejectsDestinationVectorDimensionMismatch exercises the
// loadRow path: when the destination already holds a vector whose component
// count does not match the declared dimension, reconstructing that row for a
// conflict check must surface the mismatch rather than canonicalize it silently.
func TestApplyChangesRejectsDestinationVectorDimensionMismatch(t *testing.T) {
	ctx := context.Background()
	schema := Schema{Tables: []Table{{
		Name: "vector_conflict_items",
		Columns: []Column{
			{Name: "id", Type: Type{Family: IntegerType}},
			{Name: "v", Type: Type{Family: VectorType, Arguments: []int{3}}, Nullable: true},
		},
		Constraints: []Constraint{{Kind: PrimaryKey, Columns: []string{"id"}}},
	}}}
	destination, err := OpenSQLite(Version{Major: 3}, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer destination.Close()
	if err := destination.ApplySchema(ctx, schema); err != nil {
		t.Fatal(err)
	}
	// Store a 2-component value directly against a declared dimension of 3; the
	// TEXT carrier allows it, so the mismatch only surfaces when loadRow
	// canonicalizes the row for the update conflict check.
	if _, err := destination.DB.Exec(`INSERT INTO vector_conflict_items (id, v) VALUES (1, '[1, 2]')`); err != nil {
		t.Fatal(err)
	}
	change := Change{
		Source:     Target{Engine: Postgres, Version: Version{Major: 17}},
		Sequence:   1,
		Kind:       Update,
		Table:      "vector_conflict_items",
		PrimaryKey: Row{"id": {Kind: IntegerValue, Value: "1"}},
		Before:     Row{"id": {Kind: IntegerValue, Value: "1"}, "v": {Kind: VectorValue, Value: "[1,2,3]"}},
		After:      Row{"id": {Kind: IntegerValue, Value: "1"}, "v": {Kind: VectorValue, Value: "[4,5,6]"}},
	}
	err = destination.ApplyChanges(ctx, schema, []Change{change})
	if err == nil {
		t.Fatal("expected ApplyChanges to reject a dimension-mismatched destination vector value")
	}
	if !strings.Contains(err.Error(), "dimension") {
		t.Fatalf("expected error to mention dimension, got: %v", err)
	}
}

// TestSQLiteSetCaptureSuppressedUpdatesStateTable confirms the engine-aware
// signature change did not regress the SQLite branch: on SQLite the flag still
// toggles the single-row __compat_capture_state row that the SQLite triggers
// read. The Postgres branch is exercised against a live database in the e2e
// suite (e2e/suppress_test.go).
func TestSQLiteSetCaptureSuppressedUpdatesStateTable(t *testing.T) {
	ctx := context.Background()
	store, err := OpenSQLite(Version{Major: 3}, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	tx, err := store.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := createAppliedChangesTable(ctx, tx); err != nil {
		t.Fatal(err)
	}
	var suppress int
	if err := tx.QueryRowContext(ctx, "SELECT "+quoteIdentifier("suppress")+" FROM "+quoteIdentifier(captureStateTable)+" WHERE "+quoteIdentifier("id")+" = 1").Scan(&suppress); err != nil {
		t.Fatal(err)
	}
	if suppress != 0 {
		t.Fatalf("initial suppress flag must be 0, got %d", suppress)
	}
	if err := setCaptureSuppressed(ctx, tx, SQLite, true); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRowContext(ctx, "SELECT "+quoteIdentifier("suppress")+" FROM "+quoteIdentifier(captureStateTable)+" WHERE "+quoteIdentifier("id")+" = 1").Scan(&suppress); err != nil {
		t.Fatal(err)
	}
	if suppress != 1 {
		t.Fatalf("suppressed=true must set the flag to 1, got %d", suppress)
	}
	if err := setCaptureSuppressed(ctx, tx, SQLite, false); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRowContext(ctx, "SELECT "+quoteIdentifier("suppress")+" FROM "+quoteIdentifier(captureStateTable)+" WHERE "+quoteIdentifier("id")+" = 1").Scan(&suppress); err != nil {
		t.Fatal(err)
	}
	if suppress != 0 {
		t.Fatalf("suppressed=false must reset the flag to 0, got %d", suppress)
	}
}
