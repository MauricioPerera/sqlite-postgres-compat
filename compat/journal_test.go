package compat

import "testing"

func TestOrderedChangesRejectsDuplicateSourceSequences(t *testing.T) {
	base := Change{
		Source:     Target{Engine: SQLite, Version: Version{Major: 3}},
		Sequence:   1,
		Kind:       Insert,
		Table:      "accounts",
		PrimaryKey: Row{"id": {Kind: UUIDValue, Value: "a"}},
		After:      Row{"id": {Kind: UUIDValue, Value: "a"}},
	}
	if _, err := OrderedChanges([]Change{base, base}); err == nil {
		t.Fatal("expected duplicate sequence to fail")
	}
}
