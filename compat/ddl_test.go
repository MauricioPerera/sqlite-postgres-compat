package compat

import (
	"strings"
	"testing"
)

func TestCompileDDLForBothEngines(t *testing.T) {
	schema := Schema{Tables: []Table{{
		Name: "accounts",
		Columns: []Column{
			{Name: "id", Type: Type{Family: UUIDType}},
			{Name: "profile", Type: Type{Family: JSONType}, Nullable: true},
		},
		Constraints: []Constraint{{Kind: PrimaryKey, Columns: []string{"id"}}},
	}}}

	sqlite, err := CompileDDL(Target{Engine: SQLite, Version: Version{Major: 3}}, schema)
	if err != nil {
		t.Fatal(err)
	}
	postgres, err := CompileDDL(Target{Engine: Postgres, Version: Version{Major: 17}}, schema)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sqlite[0], `"id" TEXT`) {
		t.Fatalf("unexpected sqlite DDL: %s", sqlite[0])
	}
	if !strings.Contains(postgres[0], `"id" TEXT`) || !strings.Contains(postgres[0], `"profile" TEXT`) {
		t.Fatalf("unexpected postgres DDL: %s", postgres[0])
	}
}

func TestPostgresJSONAndUUIDUseLosslessTextStorage(t *testing.T) {
	for _, family := range []TypeFamily{JSONType, UUIDType} {
		typ, err := compileType(Postgres, Type{Family: family})
		if err != nil {
			t.Fatal(err)
		}
		if typ != "TEXT" {
			t.Fatalf("expected TEXT for %s, got %s", family, typ)
		}
	}
}

func TestSQLiteDecimalUsesLosslessTextStorage(t *testing.T) {
	typ, err := compileType(SQLite, Type{Family: DecimalType, Arguments: []int{38, 18}})
	if err != nil {
		t.Fatal(err)
	}
	if typ != "TEXT" {
		t.Fatalf("expected TEXT for exact decimal storage, got %s", typ)
	}
}

func TestPostgresTimestampUsesLosslessTextStorage(t *testing.T) {
	typ, err := compileType(Postgres, Type{Family: TimestampType})
	if err != nil {
		t.Fatal(err)
	}
	if typ != "TEXT" {
		t.Fatalf("expected TEXT for nanosecond timestamp storage, got %s", typ)
	}
}

func TestCompileCanonicalView(t *testing.T) {
	where := Expression{Kind: "gt", Args: []Expression{
		{Kind: "column", Value: "e.score"},
		{Kind: "integer", Value: "10"},
	}}
	schema := Schema{
		Tables: []Table{{Name: "entries", Columns: []Column{{Name: "score", Type: Type{Family: IntegerType}}}}},
		Views: []View{{
			Name: "high_scores",
			Query: SelectQuery{
				Columns: []Projection{{Expression: Expression{Kind: "column", Value: "e.score"}, Alias: "score"}},
				From:    TableSource{Table: "entries", Alias: "e"},
				Where:   &where,
			},
		}},
	}
	statements, err := CompileDDL(Target{Engine: Postgres, Version: Version{Major: 17}}, schema)
	if err != nil {
		t.Fatal(err)
	}
	if len(statements) != 2 || !strings.Contains(statements[1], `CREATE VIEW "high_scores" AS SELECT "e"."score" AS "score"`) {
		t.Fatalf("unexpected view DDL: %#v", statements)
	}
}

func TestCompileCanonicalTriggerForBothEngines(t *testing.T) {
	trigger := Trigger{
		Name:   "capture_insert",
		Table:  "entries",
		Timing: "after",
		Event:  "insert",
		Actions: []TriggerAction{{
			Kind:  "insert",
			Table: "audit",
			Assignments: []Assignment{
				{Column: "entry_id", Value: Expression{Kind: "column", Value: "new.id"}},
			},
		}},
	}
	for _, engine := range []Engine{SQLite, Postgres} {
		statements, err := CompileDDL(Target{Engine: engine, Version: Version{Major: 17}}, Schema{Triggers: []Trigger{trigger}})
		if err != nil {
			t.Fatal(err)
		}
		if engine == SQLite && (len(statements) != 1 || !strings.Contains(statements[0], `NEW."id"`)) {
			t.Fatalf("unexpected SQLite trigger: %#v", statements)
		}
		if engine == Postgres && (len(statements) != 2 || !strings.Contains(statements[0], "RETURNS TRIGGER")) {
			t.Fatalf("unexpected PostgreSQL trigger: %#v", statements)
		}
	}
}
