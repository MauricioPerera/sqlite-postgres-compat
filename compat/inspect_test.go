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

func TestSqliteReferentialActionParsesCanonicalKeywords(t *testing.T) {
	tests := []struct {
		input string
		want  ReferentialAction
	}{
		{input: "NO ACTION", want: ""},
		{input: "RESTRICT", want: Restrict},
		{input: "CASCADE", want: Cascade},
		{input: "SET NULL", want: SetNull},
		{input: "SET DEFAULT", want: SetDefault},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, ok := sqliteReferentialAction(tt.input)
			if !ok {
				t.Fatalf("expected ok for %q", tt.input)
			}
			if got != tt.want {
				t.Fatalf("input %q: want %q got %q", tt.input, tt.want, got)
			}
		})
	}
}

func TestSqliteReferentialActionRejectsUnknownAndIsCaseSensitive(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{name: "empty string", input: ""},
		{name: "lower case no action", input: "no action"},
		{name: "title case restrict", input: "Restrict"},
		{name: "lower case cascade", input: "cascade"},
		{name: "trailing space", input: "CASCADE "},
		{name: "leading space", input: " CASCADE"},
		{name: "extra words", input: "CASCADE DELETE"},
		{name: "arbitrary", input: "bogus"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := sqliteReferentialAction(tt.input)
			if ok {
				t.Fatalf("expected not ok for %q, got %q", tt.input, got)
			}
			if got != "" {
				t.Fatalf("expected empty action for unknown %q, got %q", tt.input, got)
			}
		})
	}
}

func TestPostgresReferentialActionParsesSingleLetterCodes(t *testing.T) {
	tests := []struct {
		input string
		want  ReferentialAction
	}{
		{input: "a", want: ""},
		{input: "r", want: Restrict},
		{input: "c", want: Cascade},
		{input: "n", want: SetNull},
		{input: "d", want: SetDefault},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, ok := postgresReferentialAction(tt.input)
			if !ok {
				t.Fatalf("expected ok for %q", tt.input)
			}
			if got != tt.want {
				t.Fatalf("input %q: want %q got %q", tt.input, tt.want, got)
			}
		})
	}
}

func TestPostgresReferentialActionRejectsUnknownAndIsCaseSensitive(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{name: "empty string", input: ""},
		{name: "upper case A", input: "A"},
		{name: "full word", input: "cascade"},
		{name: "multi letter", input: "ac"},
		{name: "unknown letter", input: "x"},
		{name: "whitespace", input: " "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := postgresReferentialAction(tt.input)
			if ok {
				t.Fatalf("expected not ok for %q, got %q", tt.input, got)
			}
			if got != "" {
				t.Fatalf("expected empty action for unknown %q, got %q", tt.input, got)
			}
		})
	}
}

func TestExtractIndexPredicate(t *testing.T) {
	tests := []struct {
		name    string
		defined string
		want    string
	}{
		{name: "simple predicate", defined: `CREATE INDEX i ON t (a) WHERE a > 1`, want: "a > 1"},
		{name: "no where clause", defined: `CREATE INDEX i ON t (a)`, want: ""},
		{name: "empty definition", defined: "", want: ""},
		{name: "returns original case even though match is upper", defined: `CREATE INDEX i ON t (a) where a > 1`, want: "a > 1"},
		{name: "mixed case where keyword", defined: `CREATE INDEX i ON t (a) WhErE a > 1`, want: "a > 1"},
		{name: "trailing whitespace trimmed", defined: `CREATE INDEX i ON t (a) WHERE a > 1   `, want: "a > 1"},
		{name: "leading whitespace after where trimmed", defined: `CREATE INDEX i ON t (a) WHERE    a > 1`, want: "a > 1"},
		{name: "keyword without leading space ignored", defined: `CREATE INDEX i ON t (a)WHERE a > 1`, want: ""},
		{name: "keyword without trailing space ignored", defined: `CREATE INDEX i ON t (a) WHEREa > 1`, want: ""},
		{name: "last where wins", defined: `SELECT 1 WHERE x WHERE y < 5`, want: "y < 5"},
		{name: "where substring inside word ignored", defined: `CREATE INDEX i ON t (a) elsewhere foo`, want: ""},
		{name: "predicate with nested parens", defined: `CREATE INDEX i ON t (a) WHERE (a > 1 AND b < 2)`, want: "(a > 1 AND b < 2)"},
		{name: "only keyword no predicate", defined: `CREATE INDEX i ON t (a) WHERE `, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractIndexPredicate(tt.defined)
			if got != tt.want {
				t.Fatalf("extractIndexPredicate(%q): want %q got %q", tt.defined, tt.want, got)
			}
		})
	}
}
