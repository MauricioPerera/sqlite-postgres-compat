package compat

import (
	"context"
	"reflect"
	"testing"
)

func TestInspectSchemaRestoresCanonicalMetadata(t *testing.T) {
	ctx := context.Background()
	schema := Schema{
		Tables: []Table{{
			Name: "metadata_entries",
			Columns: []Column{
				{Name: "id", Type: Type{Family: UUIDType}},
				{Name: "payload", Type: Type{Family: JSONType}},
			},
			Constraints: []Constraint{{Kind: PrimaryKey, Columns: []string{"id"}}},
		}},
		Routines: []Routine{{
			Name:       "metadata_routine",
			Parameters: []RoutineParameter{{Name: "id", Type: Type{Family: UUIDType}}},
			Actions: []RoutineAction{{
				Kind:  "insert",
				Table: "metadata_entries",
				Assignments: []Assignment{
					{Column: "id", Value: Expression{Kind: "parameter", Value: "id"}},
					{Column: "payload", Value: Expression{Kind: "string", Value: "{}"}},
				},
			}},
		}},
	}
	store, err := OpenSQLite(Version{Major: 3}, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.ApplySchema(ctx, schema); err != nil {
		t.Fatal(err)
	}
	inspection, err := store.InspectSchema(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !inspection.Exact || inspection.Source != "canonical_metadata" {
		t.Fatalf("unexpected inspection %+v", inspection)
	}
	if !reflect.DeepEqual(schema, inspection.Schema) {
		t.Fatalf("schema mismatch: want=%+v got=%+v", schema, inspection.Schema)
	}
}

func TestInspectExternalSQLiteCatalog(t *testing.T) {
	ctx := context.Background()
	store, err := OpenSQLite(Version{Major: 3}, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.DB.Exec(`CREATE TABLE external_items (id INTEGER PRIMARY KEY, title TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	inspection, err := store.InspectSchema(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !inspection.Exact || inspection.Source != "sqlite_catalog" || len(inspection.Schema.Tables) != 1 {
		t.Fatalf("unexpected inspection %+v", inspection)
	}
	if inspection.Schema.Tables[0].Columns[0].Type.Family != IntegerType {
		t.Fatalf("unexpected schema %+v", inspection.Schema)
	}
}

func TestReservedMetadataTableIsRejected(t *testing.T) {
	for _, name := range []string{schemaMetadataTable, appliedChangesTable, captureStateTable, changeJournalTable} {
		schema := Schema{Tables: []Table{{Name: name, Columns: []Column{{Name: "x", Type: Type{Family: TextType}}}}}}
		if err := schema.Validate(); err == nil {
			t.Fatalf("expected reserved table %q to be rejected", name)
		}
	}
}
