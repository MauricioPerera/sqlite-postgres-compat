package compat

import "testing"

func TestContractRejectsUnknownEngine(t *testing.T) {
	contract := Contract{
		Source:      Target{Engine: "other", Version: Version{Major: 1}},
		Destination: Target{Engine: Postgres, Version: Version{Major: 17}},
	}
	if err := contract.Validate(); err == nil {
		t.Fatal("expected invalid engine to be rejected")
	}
}

func TestAuditDoesNotSilentlyDowngrade(t *testing.T) {
	contract := Contract{
		Source:           Target{Engine: SQLite, Version: Version{Major: 3, Minor: 45}},
		Destination:      Target{Engine: Postgres, Version: Version{Major: 17}},
		RequiredFeatures: []Feature{Tables, FullText},
	}
	findings, err := Audit(contract)
	if err != nil {
		t.Fatal(err)
	}
	if findings[0].Status != Exact || findings[1].Status != Unknown {
		t.Fatalf("unexpected findings: %#v", findings)
	}
	if err := RequireExact(findings); err == nil {
		t.Fatal("expected unknown feature to fail an exact contract")
	}
}

func TestAuditSeparatesCanonicalChecksAndIndexesFromArbitrarySQL(t *testing.T) {
	contract := Contract{
		Source:      Target{Engine: SQLite, Version: Version{Major: 3}},
		Destination: Target{Engine: Postgres, Version: Version{Major: 17}},
		RequiredFeatures: []Feature{
			CanonicalForeignKeys, CanonicalChecks, CanonicalIndexes, ForeignKeys, CheckRules, Indexes,
		},
	}
	findings, err := Audit(contract)
	if err != nil {
		t.Fatal(err)
	}
	want := []MappingStatus{Exact, Exact, Exact, Unknown, Unknown, Unknown}
	for i := range want {
		if findings[i].Status != want[i] {
			t.Fatalf("feature %s: got %s, want %s", findings[i].Feature, findings[i].Status, want[i])
		}
	}
}
