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

// tasksRoutineSchema is the shared fixture for routine execution tests: a
// single "tasks" table with an integer id and a text status column.
func tasksRoutineSchema(actions []RoutineAction) Schema {
	return Schema{
		Tables: []Table{{
			Name: "tasks",
			Columns: []Column{
				{Name: "id", Type: Type{Family: IntegerType}},
				{Name: "status", Type: Type{Family: TextType}},
			},
		}},
		Routines: []Routine{{
			Name: "routine",
			Parameters: []RoutineParameter{
				{Name: "p_id", Type: Type{Family: IntegerType}},
				{Name: "p_status", Type: Type{Family: TextType}},
				{Name: "p_del", Type: Type{Family: IntegerType}},
			},
			Actions: actions,
		}},
	}
}

// columnEqParameter builds the WHERE predicate "<column> = <parameter>".
func columnEqParameter(column, parameter string) *Expression {
	return &Expression{Kind: "eq", Args: []Expression{
		{Kind: "column", Value: column},
		{Kind: "parameter", Value: parameter},
	}}
}

func seedTasks(t *testing.T, store *Store) {
	t.Helper()
	for _, row := range []string{
		`INSERT INTO tasks (id, status) VALUES (1, 'open')`,
		`INSERT INTO tasks (id, status) VALUES (2, 'open')`,
	} {
		if _, err := store.DB.Exec(row); err != nil {
			t.Fatal(err)
		}
	}
}

// taskStatus reads the status of a single task by id, failing if the row is
// absent.
func taskStatus(t *testing.T, store *Store, id int) string {
	t.Helper()
	var status string
	err := store.DB.QueryRow(`SELECT status FROM tasks WHERE id = ?`, id).Scan(&status)
	if err != nil {
		t.Fatalf("select tasks.%d: %v", id, err)
	}
	return status
}

func taskExists(t *testing.T, store *Store, id int) bool {
	t.Helper()
	var count int
	if err := store.DB.QueryRow(`SELECT COUNT(*) FROM tasks WHERE id = ?`, id).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count > 0
}

// TestCallRoutineUpdateAffectsOnlyMatchingRows verifies an update action
// changes exactly the rows the WHERE predicate matches and leaves the rest
// untouched.
func TestCallRoutineUpdateAffectsOnlyMatchingRows(t *testing.T) {
	ctx := context.Background()
	schema := tasksRoutineSchema([]RoutineAction{{
		Kind:  "update",
		Table: "tasks",
		Assignments: []Assignment{{
			Column: "status",
			Value:  Expression{Kind: "parameter", Value: "p_status"},
		}},
		Where: columnEqParameter("id", "p_id"),
	}})

	store, err := OpenSQLite(Version{Major: 3}, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.ApplySchema(ctx, schema); err != nil {
		t.Fatal(err)
	}
	seedTasks(t, store)

	if err := store.CallRoutine(ctx, schema, "routine", map[string]Value{
		"p_id":     {Kind: IntegerValue, Value: "1"},
		"p_status": {Kind: TextValue, Value: "done"},
		"p_del":    {Kind: IntegerValue, Value: "0"},
	}); err != nil {
		t.Fatal(err)
	}

	if got := taskStatus(t, store, 1); got != "done" {
		t.Fatalf("matched row not updated: got %q want %q", got, "done")
	}
	if got := taskStatus(t, store, 2); got != "open" {
		t.Fatalf("non-matching row changed: got %q want %q", got, "open")
	}
}

// TestCallRoutineDeleteAffectsOnlyMatchingRows verifies a delete action removes
// exactly the rows the WHERE predicate matches and leaves the rest intact.
func TestCallRoutineDeleteAffectsOnlyMatchingRows(t *testing.T) {
	ctx := context.Background()
	schema := tasksRoutineSchema([]RoutineAction{{
		Kind:  "delete",
		Table: "tasks",
		Where: columnEqParameter("id", "p_id"),
	}})

	store, err := OpenSQLite(Version{Major: 3}, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.ApplySchema(ctx, schema); err != nil {
		t.Fatal(err)
	}
	seedTasks(t, store)

	if err := store.CallRoutine(ctx, schema, "routine", map[string]Value{
		"p_id":     {Kind: IntegerValue, Value: "1"},
		"p_status": {Kind: TextValue, Value: "ignored"},
		"p_del":    {Kind: IntegerValue, Value: "0"},
	}); err != nil {
		t.Fatal(err)
	}

	if taskExists(t, store, 1) {
		t.Fatal("matched row was not deleted")
	}
	if !taskExists(t, store, 2) {
		t.Fatal("non-matching row was deleted")
	}
}

// TestCallRoutineRunsActionsAtomically verifies that insert, update and delete
// actions in one routine all apply within a single transaction.
func TestCallRoutineRunsActionsAtomically(t *testing.T) {
	ctx := context.Background()
	schema := tasksRoutineSchema([]RoutineAction{
		{
			Kind:  "insert",
			Table: "tasks",
			Assignments: []Assignment{
				{Column: "id", Value: Expression{Kind: "parameter", Value: "p_id"}},
				{Column: "status", Value: Expression{Kind: "string", Value: "new"}},
			},
		},
		{
			Kind:  "update",
			Table: "tasks",
			Assignments: []Assignment{{
				Column: "status",
				Value:  Expression{Kind: "parameter", Value: "p_status"},
			}},
			Where: columnEqParameter("id", "p_id"),
		},
		{
			Kind:  "delete",
			Table: "tasks",
			Where: columnEqParameter("id", "p_del"),
		},
	})

	store, err := OpenSQLite(Version{Major: 3}, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.ApplySchema(ctx, schema); err != nil {
		t.Fatal(err)
	}
	seedTasks(t, store)

	if err := store.CallRoutine(ctx, schema, "routine", map[string]Value{
		"p_id":     {Kind: IntegerValue, Value: "1"},
		"p_status": {Kind: TextValue, Value: "done"},
		"p_del":    {Kind: IntegerValue, Value: "2"},
	}); err != nil {
		t.Fatal(err)
	}

	// insert + update target id=1: row 1 exists with the updated status.
	if !taskExists(t, store, 1) {
		t.Fatal("inserted/updated row missing")
	}
	if got := taskStatus(t, store, 1); got != "done" {
		t.Fatalf("row 1 status not updated atomically: got %q want %q", got, "done")
	}
	// delete removed id=2.
	if taskExists(t, store, 2) {
		t.Fatal("delete did not remove row 2")
	}
}

// TestCallRoutineRejectsUnknownActionKind verifies an action with an unknown
// kind is rejected rather than silently skipped.
func TestCallRoutineRejectsUnknownActionKind(t *testing.T) {
	ctx := context.Background()
	schema := tasksRoutineSchema([]RoutineAction{{
		Kind:  "bogus",
		Table: "tasks",
		Where: columnEqParameter("id", "p_id"),
	}})

	store, err := OpenSQLite(Version{Major: 3}, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.ApplySchema(ctx, schema); err != nil {
		t.Fatal(err)
	}
	seedTasks(t, store)

	if err := store.CallRoutine(ctx, schema, "routine", map[string]Value{
		"p_id":     {Kind: IntegerValue, Value: "1"},
		"p_status": {Kind: TextValue, Value: "ignored"},
		"p_del":    {Kind: IntegerValue, Value: "0"},
	}); err == nil {
		t.Fatal("expected unknown action kind to be rejected")
	}
}
