package compat

import (
	"context"
	"errors"
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
