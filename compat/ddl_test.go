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

func TestCompileCanonicalCheckAndIndexesForBothEngines(t *testing.T) {
	nonNegative := Expression{Kind: "gte", Args: []Expression{
		{Kind: "column", Value: "price"},
		{Kind: "integer", Value: "0"},
	}}
	active := Expression{Kind: "eq", Args: []Expression{
		{Kind: "column", Value: "active"},
		{Kind: "boolean", Value: "true"},
	}}
	schema := Schema{
		Tables: []Table{{
			Name: "products",
			Columns: []Column{
				{Name: "code", Type: Type{Family: TextType}},
				{Name: "price", Type: Type{Family: IntegerType}},
				{Name: "active", Type: Type{Family: BooleanType}},
			},
			Constraints: []Constraint{{Kind: Check, Expression: &nonNegative}},
		}},
		Indexes: []Index{
			{Name: "products_code_unique", Table: "products", Unique: true, Columns: []IndexColumn{{Column: "code"}}},
			{Name: "products_active_price", Table: "products", Columns: []IndexColumn{{Column: "price", Descending: true}}, Where: &active},
		},
	}
	for _, engine := range []Engine{SQLite, Postgres} {
		statements, err := CompileDDL(Target{Engine: engine, Version: Version{Major: 17}}, schema)
		if err != nil {
			t.Fatal(err)
		}
		if len(statements) != 3 {
			t.Fatalf("unexpected statements for %s: %#v", engine, statements)
		}
		if !strings.Contains(statements[0], `CHECK (("price" >= 0))`) {
			t.Fatalf("missing CHECK for %s: %s", engine, statements[0])
		}
		if statements[1] != `CREATE UNIQUE INDEX "products_code_unique" ON "products" ("code" ASC)` {
			t.Fatalf("unexpected unique index for %s: %s", engine, statements[1])
		}
		if !strings.Contains(statements[2], `("price" DESC) WHERE ("active" = TRUE)`) {
			t.Fatalf("unexpected partial index for %s: %s", engine, statements[2])
		}
	}
}

func TestSchemaRejectsIndexOnUnknownColumn(t *testing.T) {
	schema := Schema{
		Tables:  []Table{{Name: "products", Columns: []Column{{Name: "id", Type: Type{Family: IntegerType}}}}},
		Indexes: []Index{{Name: "invalid", Table: "products", Columns: []IndexColumn{{Column: "missing"}}}},
	}
	if err := schema.Validate(); err == nil || !strings.Contains(err.Error(), "unknown column") {
		t.Fatalf("expected unknown index column error, got %v", err)
	}
}

func TestCompileCanonicalForeignKeyActions(t *testing.T) {
	constraint := Constraint{
		Kind:    ForeignKey,
		Columns: []string{"parent_id"},
		References: &Reference{
			Table: "parents", Columns: []string{"id"}, OnUpdate: Cascade, OnDelete: SetNull,
		},
	}
	for _, engine := range []Engine{SQLite, Postgres} {
		got, err := compileConstraint(engine, constraint)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(got, `ON UPDATE CASCADE ON DELETE SET NULL`) {
			t.Fatalf("%s lost referential actions: %s", engine, got)
		}
	}
}

func TestCompileCanonicalTriggerUpdateAndDeleteActions(t *testing.T) {
	predicate := Expression{Kind: "eq", Args: []Expression{{Kind: "column", Value: "code"}, {Kind: "column", Value: "old.code"}}}
	for _, action := range []TriggerAction{
		{Kind: "update", Table: "audit", Assignments: []Assignment{{Column: "code", Value: Expression{Kind: "column", Value: "new.code"}}}, Where: &predicate},
		{Kind: "delete", Table: "audit", Where: &predicate},
	} {
		for _, engine := range []Engine{SQLite, Postgres} {
			compiled, err := compileTriggerAction(engine, action)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(compiled, strings.ToUpper(action.Kind)) || !strings.Contains(compiled, " WHERE ") {
				t.Fatalf("%s %s action was not compiled: %s", engine, action.Kind, compiled)
			}
		}
	}
}
