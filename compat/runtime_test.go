package compat

import (
	"context"
	"testing"
)

func TestTokenizeIsUnicodeAndCaseStable(t *testing.T) {
	tokens := tokenize("ÁRBOL, árbol! Go-123")
	want := []string{"árbol", "árbol", "go", "123"}
	if len(tokens) != len(want) {
		t.Fatalf("unexpected tokens %#v", tokens)
	}
	for i := range want {
		if tokens[i] != want[i] {
			t.Fatalf("unexpected tokens %#v", tokens)
		}
	}
}

// TestSearchTextSkipsNullColumns ensures a NULL text column does not contribute
// searchable tokens. Previously stringify(nil) emitted "<nil>", which tokenized
// to the token "nil" and produced false matches for queries containing "nil".
func TestSearchTextSkipsNullColumns(t *testing.T) {
	ctx := context.Background()
	schema := Schema{Tables: []Table{{
		Name: "documents",
		Columns: []Column{
			{Name: "id", Type: Type{Family: IntegerType}},
			{Name: "body", Type: Type{Family: TextType}, Nullable: true},
		},
	}}}

	store, err := OpenSQLite(Version{Major: 3}, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.ApplySchema(ctx, schema); err != nil {
		t.Fatal(err)
	}

	// Row 1: NULL body must not match a query for "nil".
	if _, err := store.DB.Exec(`INSERT INTO documents (id, body) VALUES (1, NULL)`); err != nil {
		t.Fatal(err)
	}
	// Row 2: literal text "nil" is the positive control.
	if _, err := store.DB.Exec(`INSERT INTO documents (id, body) VALUES (2, 'nil')`); err != nil {
		t.Fatal(err)
	}

	results, err := store.SearchText(ctx, "documents", "id", []string{"body"}, "nil")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("expected exactly one match, got %#v", results)
	}
	if results[0].ID != "2" {
		t.Fatalf("expected only the literal-nil row to match, got %q", results[0].ID)
	}
}
