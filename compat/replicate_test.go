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
