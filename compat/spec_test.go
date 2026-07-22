package compat

import (
	"reflect"
	"sort"
	"testing"
)

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

// TestInferFeaturesIncludesCanonicalFullText verifies that InferFeatures emits
// CanonicalFullText — the capability is provided unconditionally by the
// portable SearchText runtime — and that a contract requiring it audits Exact.
func TestInferFeaturesIncludesCanonicalFullText(t *testing.T) {
	schema := Schema{Tables: []Table{{
		Name: "records",
		Columns: []Column{
			{Name: "id", Type: Type{Family: TextType}},
			{Name: "body", Type: Type{Family: TextType}},
		},
		Constraints: []Constraint{{Kind: PrimaryKey, Columns: []string{"id"}}},
	}}}

	features := InferFeatures(schema)
	sort.Slice(features, func(i, j int) bool { return features[i] < features[j] })

	for _, want := range []Feature{Tables, PrimaryKeys, CanonicalFullText} {
		found := false
		for _, feature := range features {
			if feature == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("InferFeatures missing %s: got %#v", want, features)
		}
	}

	contract := Contract{
		Source:           Target{Engine: SQLite, Version: Version{Major: 3, Minor: 45}},
		Destination:      Target{Engine: Postgres, Version: Version{Major: 17}},
		RequiredFeatures: []Feature{CanonicalFullText},
	}
	findings, err := Audit(contract)
	if err != nil {
		t.Fatal(err)
	}
	want := []Finding{{Feature: CanonicalFullText, Status: Exact}}
	if !reflect.DeepEqual(findings, want) {
		t.Fatalf("unexpected audit findings: %#v", findings)
	}
	if err := RequireExact(findings); err != nil {
		t.Fatalf("canonical_full_text should satisfy an exact contract: %v", err)
	}
}
