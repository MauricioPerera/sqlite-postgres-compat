package compat

import "testing"

func TestParsePostgresCatalogRoutine(t *testing.T) {
	routine, err := parsePostgresCatalogRoutine("create_audit", `BEGIN INSERT INTO audit (code) VALUES (p_code); END`, "p_code text", "-", "plpgsql", "p")
	if err != nil {
		t.Fatal(err)
	}
	if len(routine.Parameters) != 1 || len(routine.Actions) != 1 || routine.Actions[0].Assignments[0].Value.Kind != "parameter" {
		t.Fatalf("unexpected routine: %+v", routine)
	}
}

func TestParsePostgresCatalogRoutineRejectsQueryFunction(t *testing.T) {
	if _, err := parsePostgresCatalogRoutine("answer", `SELECT 42`, "", "bigint", "sql", "f"); err == nil {
		t.Fatal("expected value-returning function to be rejected")
	}
}

// TestParsePostgresCatalogRoutineUpdate parses a routine whose body is an
// UPDATE ... SET ... WHERE ... and asserts the resulting RoutineAction carries
// the assignments and the WHERE predicate, with the parameter reference
// rewritten to a "parameter" node and the table column left as a "column".
func TestParsePostgresCatalogRoutineUpdate(t *testing.T) {
	routine, err := parsePostgresCatalogRoutine(
		"bump_status",
		`BEGIN UPDATE tasks SET status = p_status WHERE id = p_id; END`,
		"p_id integer, p_status text",
		"-", "plpgsql", "p",
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(routine.Actions) != 1 {
		t.Fatalf("expected one action, got %d", len(routine.Actions))
	}
	action := routine.Actions[0]
	if action.Kind != "update" || action.Table != "tasks" {
		t.Fatalf("unexpected action: %+v", action)
	}
	if action.Where == nil || action.Where.Kind != "eq" || len(action.Where.Args) != 2 {
		t.Fatalf("unexpected WHERE: %+v", action.Where)
	}
	// WHERE id = p_id: left is the table column "id", right is parameter "p_id".
	if action.Where.Args[0].Kind != "column" || action.Where.Args[0].Value != "id" {
		t.Fatalf("unexpected WHERE left operand: %+v", action.Where.Args[0])
	}
	if action.Where.Args[1].Kind != "parameter" || action.Where.Args[1].Value != "p_id" {
		t.Fatalf("unexpected WHERE right operand: %+v", action.Where.Args[1])
	}
	if len(action.Assignments) != 1 || action.Assignments[0].Column != "status" || action.Assignments[0].Value.Kind != "parameter" || action.Assignments[0].Value.Value != "p_status" {
		t.Fatalf("unexpected assignments: %+v", action.Assignments)
	}
}

// TestParsePostgresCatalogRoutineDelete parses a routine whose body is a
// DELETE FROM ... WHERE ... and asserts the RoutineAction carries the WHERE
// predicate and no assignments.
func TestParsePostgresCatalogRoutineDelete(t *testing.T) {
	routine, err := parsePostgresCatalogRoutine(
		"purge_tasks",
		`BEGIN DELETE FROM tasks WHERE id = p_id; END`,
		"p_id integer",
		"-", "plpgsql", "p",
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(routine.Actions) != 1 {
		t.Fatalf("expected one action, got %d", len(routine.Actions))
	}
	action := routine.Actions[0]
	if action.Kind != "delete" || action.Table != "tasks" || len(action.Assignments) != 0 {
		t.Fatalf("unexpected action: %+v", action)
	}
	if action.Where == nil || action.Where.Kind != "eq" {
		t.Fatalf("unexpected WHERE: %+v", action.Where)
	}
	if action.Where.Args[0].Kind != "column" || action.Where.Args[0].Value != "id" {
		t.Fatalf("unexpected WHERE left operand: %+v", action.Where.Args[0])
	}
	if action.Where.Args[1].Kind != "parameter" || action.Where.Args[1].Value != "p_id" {
		t.Fatalf("unexpected WHERE right operand: %+v", action.Where.Args[1])
	}
}

// TestParsePostgresCatalogRoutineUpdateWithoutWhere rejects an UPDATE body
// without a WHERE clause, mirroring the explicit error triggers produce.
func TestParsePostgresCatalogRoutineUpdateWithoutWhere(t *testing.T) {
	if _, err := parsePostgresCatalogRoutine(
		"bad_update",
		`BEGIN UPDATE tasks SET status = p_status; END`,
		"p_status text",
		"-", "plpgsql", "p",
	); err == nil {
		t.Fatal("expected UPDATE without WHERE to be rejected")
	}
}

// TestParsePostgresCatalogRoutineDeleteWithoutWhere rejects a DELETE body
// without a WHERE clause, mirroring the explicit error triggers produce.
func TestParsePostgresCatalogRoutineDeleteWithoutWhere(t *testing.T) {
	if _, err := parsePostgresCatalogRoutine(
		"bad_delete",
		`BEGIN DELETE FROM tasks; END`,
		"",
		"-", "plpgsql", "p",
	); err == nil {
		t.Fatal("expected DELETE without WHERE to be rejected")
	}
}

// TestParsePostgresCatalogRoutineInsertStillParses guards against regression:
// an INSERT-only body continues to parse to a single insert action with no WHERE.
func TestParsePostgresCatalogRoutineInsertStillParses(t *testing.T) {
	routine, err := parsePostgresCatalogRoutine("create_audit", `BEGIN INSERT INTO audit (code) VALUES (p_code); END`, "p_code text", "-", "plpgsql", "p")
	if err != nil {
		t.Fatal(err)
	}
	if len(routine.Actions) != 1 || routine.Actions[0].Kind != "insert" || routine.Actions[0].Where != nil {
		t.Fatalf("insert routine regressed: %+v", routine.Actions)
	}
}
