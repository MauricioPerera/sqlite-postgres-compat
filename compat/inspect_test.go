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

// TestInspectExternalSQLiteGeneratedStoredColumn proves native catalog inspection
// reconstructs a STORED generated column exactly (Exact, no Unresolved) with its
// generation expression recovered from the CREATE TABLE SQL, while a VIRTUAL
// generated column is reported unresolved (never silently accepted).
func TestInspectExternalSQLiteGeneratedStoredColumn(t *testing.T) {
	ctx := context.Background()
	store, err := OpenSQLite(Version{Major: 3}, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.DB.Exec(`CREATE TABLE gen_items (id INTEGER PRIMARY KEY, price INTEGER NOT NULL, quantity INTEGER NOT NULL, total INTEGER GENERATED ALWAYS AS (price * quantity) STORED)`); err != nil {
		t.Fatal(err)
	}
	inspection, err := store.InspectSchema(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !inspection.Exact || len(inspection.Unresolved) != 0 {
		t.Fatalf("STORED generated column must inspect as exact: %+v", inspection)
	}
	var total *Column
	for i := range inspection.Schema.Tables[0].Columns {
		if inspection.Schema.Tables[0].Columns[i].Name == "total" {
			total = &inspection.Schema.Tables[0].Columns[i]
		}
	}
	if total == nil || total.Generated == nil || !total.Generated.Stored {
		t.Fatalf("total column was not reconstructed as STORED generated: %+v", inspection.Schema.Tables[0].Columns)
	}
	if total.Generated.Expression.Kind != "mul" || len(total.Generated.Expression.Args) != 2 {
		t.Fatalf("generation expression not reconstructed: %+v", total.Generated.Expression)
	}

	virtual, err := OpenSQLite(Version{Major: 3}, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer virtual.Close()
	if _, err := virtual.DB.Exec(`CREATE TABLE gen_virtual (id INTEGER PRIMARY KEY, price INTEGER NOT NULL, doubled INTEGER GENERATED ALWAYS AS (price * 2) VIRTUAL)`); err != nil {
		t.Fatal(err)
	}
	vi, err := virtual.InspectSchema(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if vi.Exact || len(vi.Unresolved) == 0 {
		t.Fatalf("VIRTUAL generated column must be unresolved, not exact: %+v", vi)
	}
}

// TestInspectExternalSQLiteExpressionIndex proves native catalog inspection of
// an EXTERNAL SQLite database reconstructs expression index keys exactly: a
// function key, an operator key with DESC, a UNIQUE expression index, and a mix
// of plain-column and expression keys. An expression whose text is outside the
// canonical grammar (a function not in the allowlist) is placed in Unresolved
// rather than reconstructed as a wrong AST.
func TestInspectExternalSQLiteExpressionIndex(t *testing.T) {
	ctx := context.Background()
	store, err := OpenSQLite(Version{Major: 3}, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, stmt := range []string{
		`CREATE TABLE t (id INTEGER PRIMARY KEY, email TEXT, a INTEGER, b INTEGER)`,
		`CREATE INDEX i_lower ON t (lower(email))`,
		`CREATE INDEX i_sum_desc ON t (a + b DESC)`,
		`CREATE UNIQUE INDEX i_ulower ON t (lower(email))`,
		`CREATE INDEX i_mixed ON t (a, lower(email), b DESC)`,
	} {
		if _, err := store.DB.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	inspection, err := store.InspectSchema(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !inspection.Exact || len(inspection.Unresolved) != 0 {
		t.Fatalf("expression indexes must inspect as exact: %+v", inspection)
	}
	byName := map[string]Index{}
	for _, ix := range inspection.Schema.Indexes {
		byName[ix.Name] = ix
	}
	lowerEmail := Expression{Kind: "lower", Args: []Expression{{Kind: "column", Value: "email"}}}
	sumAB := Expression{Kind: "add", Args: []Expression{{Kind: "column", Value: "a"}, {Kind: "column", Value: "b"}}}
	assertIndexColumns := func(name string, want []IndexColumn, unique bool) {
		ix, ok := byName[name]
		if !ok {
			t.Fatalf("index %q missing from inspection: %+v", name, inspection.Schema.Indexes)
		}
		if ix.Unique != unique {
			t.Fatalf("index %q unique=%v, want %v", name, ix.Unique, unique)
		}
		if !reflect.DeepEqual(ix.Columns, want) {
			t.Fatalf("index %q columns:\n want %+v\n  got %+v", name, want, ix.Columns)
		}
	}
	assertIndexColumns("i_lower", []IndexColumn{{Expression: &lowerEmail}}, false)
	assertIndexColumns("i_sum_desc", []IndexColumn{{Expression: &sumAB, Descending: true}}, false)
	assertIndexColumns("i_ulower", []IndexColumn{{Expression: &lowerEmail}}, true)
	assertIndexColumns("i_mixed", []IndexColumn{
		{Column: "a"},
		{Expression: &lowerEmail},
		{Column: "b", Descending: true},
	}, false)

	// A function outside the allowlist is honestly unresolved, never a wrong AST.
	outside, err := OpenSQLite(Version{Major: 3}, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer outside.Close()
	for _, stmt := range []string{
		`CREATE TABLE u (id INTEGER PRIMARY KEY, email TEXT)`,
		`CREATE INDEX i_substr ON u (substr(email, 1, 2))`,
	} {
		if _, err := outside.DB.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	oi, err := outside.InspectSchema(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if oi.Exact || len(oi.Unresolved) == 0 {
		t.Fatalf("out-of-grammar expression index must be unresolved: %+v", oi)
	}
}

// TestInspectCanonicalMetadataExpressionIndexRoundTrip proves the canonical
// metadata path (__compat_schema) round-trips an expression index byte-for-byte:
// a schema created by this layer inspects back to exactly the same Schema.
func TestInspectCanonicalMetadataExpressionIndexRoundTrip(t *testing.T) {
	ctx := context.Background()
	lowerEmail := Expression{Kind: "lower", Args: []Expression{{Kind: "column", Value: "email"}}}
	sumAB := Expression{Kind: "add", Args: []Expression{{Kind: "column", Value: "a"}, {Kind: "column", Value: "b"}}}
	schema := Schema{
		Tables: []Table{{
			Name: "users",
			Columns: []Column{
				{Name: "email", Type: Type{Family: TextType}},
				{Name: "a", Type: Type{Family: IntegerType}},
				{Name: "b", Type: Type{Family: IntegerType}},
			},
		}},
		Indexes: []Index{
			{Name: "users_lower_email", Table: "users", Columns: []IndexColumn{{Expression: &lowerEmail}}},
			{Name: "users_mixed", Table: "users", Unique: true, Columns: []IndexColumn{
				{Column: "a"},
				{Expression: &sumAB, Descending: true},
			}},
		},
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
		t.Fatalf("unexpected inspection: %+v", inspection)
	}
	if !reflect.DeepEqual(schema, inspection.Schema) {
		t.Fatalf("schema mismatch:\n want %+v\n  got %+v", schema, inspection.Schema)
	}
}

// TestInspectCanonicalMetadataDomainRoundTrip is the primary proof for domains:
// a schema with a domain used by a column, applied to SQLite (where the domain is
// inlined) and read back through the canonical metadata (__compat_schema), must
// reconstruct byte-for-byte — domain definition and DomainRef included. This is
// the exact round-trip both engines share; external SQLite inspection does NOT
// rebuild a domain (the column appears as a plain column + CHECK), which is
// documented as the accepted asymmetry.
func TestInspectCanonicalMetadataDomainRoundTrip(t *testing.T) {
	ctx := context.Background()
	check := Expression{Kind: "gt", Args: []Expression{{Kind: "domain_value"}, {Kind: "integer", Value: "0"}}}
	schema := Schema{
		Domains: []Domain{{Name: "positive_qty", Type: Type{Family: IntegerType}, Check: &check, NotNull: true}},
		Tables: []Table{{
			Name: "items",
			Columns: []Column{
				{Name: "id", Type: Type{Family: IntegerType}},
				{Name: "qty", Type: Type{Family: IntegerType}, Nullable: true, DomainRef: "positive_qty"},
			},
			Constraints: []Constraint{{Kind: PrimaryKey, Columns: []string{"id"}}},
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
		t.Fatalf("unexpected inspection: %+v", inspection)
	}
	if !reflect.DeepEqual(schema, inspection.Schema) {
		t.Fatalf("schema mismatch:\n want %+v\n  got %+v", schema, inspection.Schema)
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
