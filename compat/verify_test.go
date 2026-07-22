package compat

import "testing"

func TestSnapshotDigestIgnoresRowOrder(t *testing.T) {
	schema := Schema{Tables: []Table{{Name: "items", Columns: []Column{{Name: "id", Type: Type{Family: IntegerType}}}}}}
	first := Snapshot{Schema: schema, Rows: map[string][]Row{"items": {
		{"id": {Kind: IntegerValue, Value: "1"}},
		{"id": {Kind: IntegerValue, Value: "2"}},
	}}}
	second := Snapshot{Schema: schema, Rows: map[string][]Row{"items": {
		{"id": {Kind: IntegerValue, Value: "2"}},
		{"id": {Kind: IntegerValue, Value: "1"}},
	}}}
	report, err := VerifySnapshots(first, second)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Equivalent {
		t.Fatalf("expected equivalent snapshots: %#v", report)
	}
}
