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
