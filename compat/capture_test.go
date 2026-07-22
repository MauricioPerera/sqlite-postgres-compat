package compat

import (
	"context"
	"strings"
	"testing"
)

func TestSQLiteChangeCaptureProducesCanonicalStream(t *testing.T) {
	ctx := context.Background()
	schema := replicationTestSchema("captured_items")
	store, err := OpenSQLite(Version{Major: 3}, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.ApplySchema(ctx, schema); err != nil {
		t.Fatal(err)
	}
	if err := store.InstallChangeCapture(ctx, schema); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB.ExecContext(ctx, `INSERT INTO captured_items (id, title) VALUES (1, 'first')`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB.ExecContext(ctx, `UPDATE captured_items SET title = 'second' WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB.ExecContext(ctx, `DELETE FROM captured_items WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	changes, err := store.ReadCapturedChanges(ctx, schema, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 3 || changes[0].Kind != Insert || changes[1].Kind != Update || changes[2].Kind != Delete {
		t.Fatalf("unexpected captured stream: %+v", changes)
	}
	if changes[1].Before["title"].Value != "first" || changes[1].After["title"].Value != "second" {
		t.Fatalf("update images were not preserved: %+v", changes[1])
	}
}

// vectorCaptureSchema is a schema whose vector column declares a dimension of 3.
// The TEXT carrier in SQLite accepts any bracketed text, so a value with the
// wrong component count can be inserted directly and must be caught downstream.
func vectorCaptureSchema(name string) Schema {
	return Schema{Tables: []Table{{
		Name: name,
		Columns: []Column{
			{Name: "id", Type: Type{Family: IntegerType}},
			{Name: "v", Type: Type{Family: VectorType, Arguments: []int{3}}, Nullable: true},
		},
		Constraints: []Constraint{{Kind: PrimaryKey, Columns: []string{"id"}}},
	}}}
}

func TestSQLiteCaptureRejectsVectorDimensionMismatch(t *testing.T) {
	ctx := context.Background()
	schema := vectorCaptureSchema("vector_capture_items")
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
	// Insert a 2-component value directly: the TEXT carrier does not enforce the
	// declared dimension, so the mutation is journaled verbatim. Reading it back
	// must surface the dimension mismatch rather than canonicalize it silently.
	if _, err := source.DB.ExecContext(ctx, `INSERT INTO vector_capture_items (id, v) VALUES (1, '[1, 2]')`); err != nil {
		t.Fatal(err)
	}
	_, err = source.ReadCapturedChanges(ctx, schema, 0, 10)
	if err == nil {
		t.Fatal("expected ReadCapturedChanges to reject a dimension-mismatched vector value")
	}
	if !strings.Contains(err.Error(), "dimension") {
		t.Fatalf("expected error to mention dimension, got: %v", err)
	}
}

func TestSQLiteCaptureReplicatesDimensionedVector(t *testing.T) {
	ctx := context.Background()
	schema := vectorCaptureSchema("vector_replicate_items")
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
	if _, err := source.DB.ExecContext(ctx, `INSERT INTO vector_replicate_items (id, v) VALUES (1, '[1, 2.0, 3]')`); err != nil {
		t.Fatal(err)
	}
	changes, err := source.ReadCapturedChanges(ctx, schema, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 1 || changes[0].Kind != Insert {
		t.Fatalf("unexpected captured stream: %+v", changes)
	}
	if got := changes[0].After["v"]; got.Kind != VectorValue || got.Value != "[1,2,3]" {
		t.Fatalf("unexpected captured vector value %+v", got)
	}

	destination, err := OpenSQLite(Version{Major: 3}, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer destination.Close()
	if err := destination.ApplySchema(ctx, schema); err != nil {
		t.Fatal(err)
	}
	if err := destination.ApplyChanges(ctx, schema, changes); err != nil {
		t.Fatalf("vector replication must apply: %v", err)
	}
	var v string
	if err := destination.DB.QueryRow(`SELECT v FROM vector_replicate_items WHERE id = 1`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	if v != "[1,2,3]" {
		t.Fatalf("expected canonical vector [1,2,3] at destination, got %q", v)
	}
}

// TestCompileCaptureTriggersUsesTransactionLocalGUCOnPostgres guards the
// anti-echo suppression design without requiring a live database. The Postgres
// capture functions must gate journaling on the transaction-local GUC
// compat.suppress and must NOT read the __compat_capture_state table, so that a
// concurrent replication transaction cannot leak its suppression under MVCC.
// The SQLite triggers keep the single-row state-table condition, which is safe
// because SQLite is pinned to one connection.
func TestCompileCaptureTriggersUsesTransactionLocalGUCOnPostgres(t *testing.T) {
	schema := replicationTestSchema("suppress_trigger_items")
	primary, err := primaryKeyColumns(schema.Tables[0])
	if err != nil {
		t.Fatal(err)
	}

	postgresStatements := compileCaptureTriggers(Postgres, schema.Tables[0], primary)
	joined := strings.Join(postgresStatements, "\n")
	if !strings.Contains(joined, "current_setting('compat.suppress', true) IS DISTINCT FROM '1'") {
		t.Fatalf("postgres capture trigger must gate on transaction-local GUC:\n%s", joined)
	}
	if strings.Contains(joined, captureStateTable) {
		t.Fatalf("postgres capture trigger must not depend on %s:\n%s", captureStateTable, joined)
	}

	sqliteStatements := compileCaptureTriggers(SQLite, schema.Tables[0], primary)
	sqliteJoined := strings.Join(sqliteStatements, "\n")
	if !strings.Contains(sqliteJoined, "WHEN (SELECT "+quoteIdentifier("suppress")+" FROM "+quoteIdentifier(captureStateTable)) {
		t.Fatalf("sqlite capture trigger must keep the state-table condition:\n%s", sqliteJoined)
	}
}

// TestCompileCaptureTriggersUsesRoundTripFloatEncodingOnSQLite guards the
// lossless capture encoding for FLOAT and REAL-stored DECIMAL columns. SQLite's
// CAST(REAL AS TEXT) truncates to ~15 significant digits, so the journal must
// emit the 17-significant-digit round-trip form via printf('%!.17g'). DECIMAL
// columns gate that on typeof(col) = 'real' so arbitrary-precision TEXT and
// INTEGER storage still pass through CAST verbatim. Postgres must keep the
// plain CAST, since its float8/numeric text already round-trips.
func TestCompileCaptureTriggersUsesRoundTripFloatEncodingOnSQLite(t *testing.T) {
	schema := Schema{Tables: []Table{{
		Name: "encoding_items",
		Columns: []Column{
			{Name: "id", Type: Type{Family: IntegerType}},
			{Name: "flt", Type: Type{Family: FloatType}},
			{Name: "amt", Type: Type{Family: DecimalType, Arguments: []int{38, 18}}},
		},
		Constraints: []Constraint{{Kind: PrimaryKey, Columns: []string{"id"}}},
	}}}
	primary, err := primaryKeyColumns(schema.Tables[0])
	if err != nil {
		t.Fatal(err)
	}

	sqliteJoined := strings.Join(compileCaptureTriggers(SQLite, schema.Tables[0], primary), "\n")
	if !strings.Contains(sqliteJoined, "printf('%!.17g', ") {
		t.Fatalf("sqlite capture trigger must encode floats with printf('%%!.17g'):\n%s", sqliteJoined)
	}
	if !strings.Contains(sqliteJoined, "CASE typeof(") || !strings.Contains(sqliteJoined, "WHEN 'real' THEN printf('%!.17g', ") {
		t.Fatalf("sqlite capture trigger must gate decimal printf on typeof 'real':\n%s", sqliteJoined)
	}

	postgresJoined := strings.Join(compileCaptureTriggers(Postgres, schema.Tables[0], primary), "\n")
	if strings.Contains(postgresJoined, "printf('%!.17g'") {
		t.Fatalf("postgres capture trigger must not use sqlite printf:\n%s", postgresJoined)
	}
	if !strings.Contains(postgresJoined, "CAST(") {
		t.Fatalf("postgres capture trigger must keep CAST for float/decimal text:\n%s", postgresJoined)
	}
}
