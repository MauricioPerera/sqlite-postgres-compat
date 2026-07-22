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

func TestCompileLikePreservesSQLiteSemanticsForBothEngines(t *testing.T) {
	like := Expression{Kind: "like", Args: []Expression{
		{Kind: "column", Value: "code"},
		{Kind: "string", Value: "prod_%"},
	}}
	schema := Schema{
		Tables: []Table{{Name: "products", Columns: []Column{
			{Name: "code", Type: Type{Family: TextType}},
			{Name: "price", Type: Type{Family: IntegerType}},
		}}},
		Views: []View{{
			Name: "product_codes",
			Query: SelectQuery{
				Columns: []Projection{{Expression: Expression{Kind: "column", Value: "code"}, Alias: "code"}},
				From:    TableSource{Table: "products"},
				Where:   &like,
			},
		}},
	}

	for _, engine := range []Engine{SQLite, Postgres} {
		statements, err := CompileDDL(Target{Engine: engine, Version: Version{Major: 17}}, schema)
		if err != nil {
			t.Fatalf("%s: %v", engine, err)
		}
		if len(statements) != 2 {
			t.Fatalf("%s: expected 2 statements, got %#v", engine, statements)
		}
		view := statements[1]
		if engine == Postgres {
			if !strings.Contains(view, " ILIKE ") {
				t.Fatalf("postgres view must use ILIKE: %s", view)
			}
			if strings.Contains(view, " LIKE ") {
				t.Fatalf("postgres view must not emit bare LIKE: %s", view)
			}
			if !strings.Contains(view, `("code" ILIKE 'prod_%')`) {
				t.Fatalf("unexpected postgres view DDL: %s", view)
			}
		}
		if engine == SQLite {
			if !strings.Contains(view, " LIKE ") {
				t.Fatalf("sqlite view must use LIKE: %s", view)
			}
			if strings.Contains(view, "ILIKE") {
				t.Fatalf("sqlite view must not emit ILIKE: %s", view)
			}
			if !strings.Contains(view, `("code" LIKE 'prod_%')`) {
				t.Fatalf("unexpected sqlite view DDL: %s", view)
			}
		}
	}
}

func TestCompileConcatForBothEngines(t *testing.T) {
	concat := Expression{Kind: "concat", Args: []Expression{
		{Kind: "column", Value: "first"},
		{Kind: "concat", Args: []Expression{
			{Kind: "string", Value: " "},
			{Kind: "column", Value: "last"},
		}},
	}}
	schema := Schema{
		Tables: []Table{{Name: "people", Columns: []Column{
			{Name: "first", Type: Type{Family: TextType}},
			{Name: "last", Type: Type{Family: TextType}},
		}}},
		Views: []View{{
			Name: "full_names",
			Query: SelectQuery{
				Columns: []Projection{{Expression: concat, Alias: "name"}},
				From:    TableSource{Table: "people"},
			},
		}},
	}

	for _, engine := range []Engine{SQLite, Postgres} {
		statements, err := CompileDDL(Target{Engine: engine, Version: Version{Major: 17}}, schema)
		if err != nil {
			t.Fatalf("%s: %v", engine, err)
		}
		view := statements[1]
		if !strings.Contains(view, `("first" || (' ' || "last"))`) {
			t.Fatalf("%s: expected || concat, got %s", engine, view)
		}
		if strings.Contains(view, "ILIKE") || strings.Contains(view, " LIKE ") {
			t.Fatalf("%s: concat must not involve LIKE, got %s", engine, view)
		}
	}
}

func TestCompileScalarFunctionsForBothEngines(t *testing.T) {
	schema := Schema{
		Tables: []Table{{Name: "items", Columns: []Column{
			{Name: "code", Type: Type{Family: TextType}},
			{Name: "value", Type: Type{Family: IntegerType}},
		}}},
		Views: []View{{
			Name: "item_report",
			Query: SelectQuery{
				Columns: []Projection{
					{Expression: Expression{Kind: "length", Args: []Expression{{Kind: "column", Value: "code"}}}, Alias: "len"},
					{Expression: Expression{Kind: "abs", Args: []Expression{{Kind: "column", Value: "value"}}}, Alias: "abs_val"},
					{Expression: Expression{Kind: "trim", Args: []Expression{{Kind: "column", Value: "code"}}}, Alias: "clean"},
					{Expression: Expression{Kind: "coalesce", Args: []Expression{
						{Kind: "column", Value: "code"},
						{Kind: "string", Value: ""},
					}}, Alias: "fallback"},
					{Expression: Expression{Kind: "replace", Args: []Expression{
						{Kind: "column", Value: "code"},
						{Kind: "string", Value: "-"},
						{Kind: "string", Value: "_"},
					}}, Alias: "normalized"},
				},
				From: TableSource{Table: "items"},
			},
		}},
	}

	for _, engine := range []Engine{SQLite, Postgres} {
		statements, err := CompileDDL(Target{Engine: engine, Version: Version{Major: 17}}, schema)
		if err != nil {
			t.Fatalf("%s: %v", engine, err)
		}
		view := statements[1]
		for _, want := range []string{
			`LENGTH("code")`,
			`ABS("value")`,
			`TRIM("code")`,
			`COALESCE("code", '')`,
			`REPLACE("code", '-', '_')`,
		} {
			if !strings.Contains(view, want) {
				t.Fatalf("%s: missing %q in %s", engine, want, view)
			}
		}
	}
}

func TestCompileNotLikeForBothEngines(t *testing.T) {
	schema := Schema{
		Tables: []Table{{Name: "products", Columns: []Column{
			{Name: "code", Type: Type{Family: TextType}},
		}}},
		Views: []View{{
			Name: "non_matching",
			Query: SelectQuery{
				Columns: []Projection{{Expression: Expression{Kind: "column", Value: "code"}, Alias: "code"}},
				From:    TableSource{Table: "products"},
				Where: &Expression{Kind: "not", Args: []Expression{{Kind: "like", Args: []Expression{
					{Kind: "column", Value: "code"},
					{Kind: "string", Value: "x%"},
				}}}},
			},
		}},
	}

	for _, engine := range []Engine{SQLite, Postgres} {
		statements, err := CompileDDL(Target{Engine: engine, Version: Version{Major: 17}}, schema)
		if err != nil {
			t.Fatalf("%s: %v", engine, err)
		}
		view := statements[1]
		if !strings.Contains(view, "(NOT (") {
			t.Fatalf("%s: expected (NOT (...)) wrapper, got %s", engine, view)
		}
		if engine == Postgres {
			if !strings.Contains(view, `("code" ILIKE 'x%')`) {
				t.Fatalf("postgres not-like must wrap ILIKE: %s", view)
			}
		} else {
			if !strings.Contains(view, `("code" LIKE 'x%')`) {
				t.Fatalf("sqlite not-like must wrap LIKE: %s", view)
			}
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

// TestCompileTriggerNewOldStillMagic verifies that inside a trigger context a
// leading new/old segment still resolves to the unquoted NEW/OLD transition
// variable (no regression from gating the behavior behind inTrigger).
func TestCompileTriggerNewOldStillMagic(t *testing.T) {
	cases := []struct {
		name    string
		expr    Expression
		wantAll map[Engine]string // substring every engine must contain
	}{
		{
			name: "new.column in WHEN",
			expr: Expression{Kind: "gt", Args: []Expression{
				{Kind: "column", Value: "new.amount"},
				{Kind: "integer", Value: "0"},
			}},
			wantAll: map[Engine]string{SQLite: `NEW."amount"`, Postgres: `NEW."amount"`},
		},
		{
			name: "old.column in WHEN",
			expr: Expression{Kind: "ne", Args: []Expression{
				{Kind: "column", Value: "old.code"},
				{Kind: "string", Value: "x"},
			}},
			wantAll: map[Engine]string{SQLite: `OLD."code"`, Postgres: `OLD."code"`},
		},
		{
			name: "case-insensitive NEW prefix stays magic",
			expr: Expression{Kind: "gt", Args: []Expression{
				{Kind: "column", Value: "NEW.amount"},
				{Kind: "integer", Value: "0"},
			}},
			wantAll: map[Engine]string{SQLite: `NEW."amount"`, Postgres: `NEW."amount"`},
		},
		{
			name: "nested new.column inside coalesce in action value",
			expr: Expression{Kind: "coalesce", Args: []Expression{
				{Kind: "column", Value: "new.amount"},
				{Kind: "integer", Value: "0"},
			}},
			wantAll: map[Engine]string{SQLite: `COALESCE(NEW."amount", 0)`, Postgres: `COALESCE(NEW."amount", 0)`},
		},
	}
	for _, tc := range cases {
		when := tc.expr
		trigger := Trigger{
			Name: "trg", Table: "entries", Timing: "before", Event: "update", When: &when,
			Actions: []TriggerAction{{Kind: "update", Table: "entries",
				Assignments: []Assignment{{Column: "amount", Value: Expression{Kind: "column", Value: "new.amount"}}},
				Where:       &Expression{Kind: "eq", Args: []Expression{{Kind: "column", Value: "new.amount"}, {Kind: "integer", Value: "0"}}}}},
		}
		for _, engine := range []Engine{SQLite, Postgres} {
			statements, err := CompileDDL(Target{Engine: engine, Version: Version{Major: 17}}, Schema{Triggers: []Trigger{trigger}})
			if err != nil {
				t.Fatalf("%s/%s: %v", engine, tc.name, err)
			}
			joined := strings.Join(statements, "\n")
			if !strings.Contains(joined, tc.wantAll[engine]) {
				t.Fatalf("%s/%s: want %q in trigger DDL, got:\n%s", engine, tc.name, tc.wantAll[engine], joined)
			}
			// A column named new must never be quoted inside a trigger body.
			if strings.Contains(joined, `"new"`) || strings.Contains(joined, `"old"`) || strings.Contains(joined, `"NEW"`) || strings.Contains(joined, `"OLD"`) {
				t.Fatalf("%s/%s: new/old leaked as quoted identifier in trigger DDL:\n%s", engine, tc.name, joined)
			}
		}
	}
}

// TestCompileNonTriggerNewOldColumnIsQuoted verifies that outside a trigger
// (CHECK constraint, partial index WHERE, and view) a column literally named
// new/old — quoted "New" or unquoted — compiles to a quoted identifier, not to
// the bare NEW/OLD transition variable. This is the AUDIT2-A MEDIA #2 fix.
func TestCompileNonTriggerNewOldColumnIsQuoted(t *testing.T) {
	// Each case is built around a table whose column is literally named with a
	// new/old-ish identifier, and the expression referencing it.
	cases := []struct {
		name     string
		schema   Schema
		find     func(statements []string) string
		wantSubs []string // substrings the located statement must contain
		badSubs  []string // substrings it must NOT contain
	}{
		{
			name: "CHECK with unquoted column named new",
			schema: Schema{Tables: []Table{{
				Name: "t",
				Columns: []Column{
					{Name: "new", Type: Type{Family: IntegerType}},
				},
				Constraints: []Constraint{{Kind: Check, Expression: &Expression{Kind: "gt", Args: []Expression{
					{Kind: "column", Value: "new"}, {Kind: "integer", Value: "0"},
				}}}},
			}}},
			find:     func(s []string) string { return s[0] },
			wantSubs: []string{`CHECK (("new" > 0))`},
			badSubs:  []string{`(NEW > 0)`, `(new > 0)`},
		},
		{
			name: "CHECK with quoted column named New (case-sensitive)",
			schema: Schema{Tables: []Table{{
				Name: "t",
				Columns: []Column{
					{Name: "New", Type: Type{Family: IntegerType}},
				},
				Constraints: []Constraint{{Kind: Check, Expression: &Expression{Kind: "gt", Args: []Expression{
					{Kind: "column", Value: "New"}, {Kind: "integer", Value: "0"},
				}}}},
			}}},
			find:     func(s []string) string { return s[0] },
			wantSubs: []string{`CHECK (("New" > 0))`},
			badSubs:  []string{`(NEW > 0)`, `(new > 0)`, `("new" > 0)`},
		},
		{
			name: "partial index WHERE with column named old",
			schema: Schema{
				Tables: []Table{{Name: "t", Columns: []Column{
					{Name: "id", Type: Type{Family: IntegerType}},
					{Name: "old", Type: Type{Family: BooleanType}},
				}}},
				Indexes: []Index{{Name: "idx", Table: "t", Columns: []IndexColumn{{Column: "id"}}, Where: &Expression{Kind: "eq", Args: []Expression{
					{Kind: "column", Value: "old"}, {Kind: "boolean", Value: "true"},
				}}}},
			},
			find:     func(s []string) string { return s[1] },
			wantSubs: []string{`WHERE ("old" = TRUE)`},
			badSubs:  []string{`(OLD = TRUE)`, `(old = TRUE)`},
		},
		{
			name: "view projection+where with column named new",
			schema: Schema{
				Tables: []Table{{Name: "t", Columns: []Column{
					{Name: "new", Type: Type{Family: IntegerType}},
				}}},
				Views: []View{{Name: "v", Query: SelectQuery{
					Columns: []Projection{{Expression: Expression{Kind: "column", Value: "new"}, Alias: "new"}},
					From:    TableSource{Table: "t"},
					Where: &Expression{Kind: "gt", Args: []Expression{
						{Kind: "column", Value: "new"}, {Kind: "integer", Value: "0"},
					}},
				}}},
			},
			find:     func(s []string) string { return s[1] },
			wantSubs: []string{`SELECT "new" AS "new"`, `WHERE ("new" > 0)`},
			badSubs:  []string{`SELECT NEW`, `(NEW > 0)`, `(new > 0)`},
		},
	}
	for _, engine := range []Engine{SQLite, Postgres} {
		for _, tc := range cases {
			statements, err := CompileDDL(Target{Engine: engine, Version: Version{Major: 17}}, tc.schema)
			if err != nil {
				t.Fatalf("%s/%s: %v", engine, tc.name, err)
			}
			target := tc.find(statements)
			for _, want := range tc.wantSubs {
				if !strings.Contains(target, want) {
					t.Fatalf("%s/%s: want %q in %s", engine, tc.name, want, target)
				}
			}
			for _, bad := range tc.badSubs {
				if strings.Contains(target, bad) {
					t.Fatalf("%s/%s: forbidden %q in %s", engine, tc.name, bad, target)
				}
			}
		}
	}
}

// TestCompileColumnExpressionGatesTriggerNewOld pins the inTrigger behavior of
// the extracted column compiler directly: a leading new/old segment is the
// trigger transition variable only when inTrigger is set, and every other
// segment is quoted (preserving the case of a column literally named "New").
func TestCompileColumnExpressionGatesTriggerNewOld(t *testing.T) {
	cases := []struct {
		value     string
		inTrigger bool
		want      string
	}{
		{"new.amount", true, `NEW."amount"`},
		{"OLD.code", true, `OLD."code"`},
		{"a.new", true, `"a"."new"`}, // only the leading segment is magic
		{"New", false, `"New"`},      // case preserved, quoted, not folded to NEW
		{"a.b", false, `"a"."b"`},
	}
	for _, tc := range cases {
		got, err := compileColumnExpression(tc.value, tc.inTrigger)
		if err != nil {
			t.Fatalf("compileColumnExpression(%q,%v): %v", tc.value, tc.inTrigger, err)
		}
		if got != tc.want {
			t.Fatalf("compileColumnExpression(%q,%v) = %q; want %q", tc.value, tc.inTrigger, got, tc.want)
		}
	}
	if _, err := compileColumnExpression(".x", false); err == nil {
		t.Fatal("expected error for empty leading segment, got nil")
	}
}
