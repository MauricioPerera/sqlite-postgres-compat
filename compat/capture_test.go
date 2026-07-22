package compat

import (
	"context"
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
