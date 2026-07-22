package compat

import (
	"strings"
	"testing"
)

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

func TestRequireEquivalentAcceptsMatchingSnapshots(t *testing.T) {
	digest := "abc123"
	report := VerificationReport{SourceDigest: digest, DestinationDigest: digest, Equivalent: true}
	if err := RequireEquivalent(report); err != nil {
		t.Fatalf("expected nil error for equivalent snapshots, got %v", err)
	}
}

func TestRequireEquivalentRejectsMismatchedSnapshots(t *testing.T) {
	tests := []struct {
		name   string
		report VerificationReport
	}{
		{
			name:   "distinct digests",
			report: VerificationReport{SourceDigest: "aaa", DestinationDigest: "bbb", Equivalent: false},
		},
		{
			name:   "empty digests still reported",
			report: VerificationReport{SourceDigest: "", DestinationDigest: "", Equivalent: false},
		},
		{
			name:   "mismatch flag set with matching digests (defensive)",
			report: VerificationReport{SourceDigest: "same", DestinationDigest: "same", Equivalent: false},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := RequireEquivalent(tt.report)
			if err == nil {
				t.Fatal("expected error for non-equivalent snapshots")
			}
			if !strings.Contains(err.Error(), "snapshot mismatch") {
				t.Fatalf("error %q must mention snapshot mismatch", err.Error())
			}
			if !strings.Contains(err.Error(), tt.report.SourceDigest) {
				t.Fatalf("error %q must mention source digest %q", err.Error(), tt.report.SourceDigest)
			}
			if !strings.Contains(err.Error(), tt.report.DestinationDigest) {
				t.Fatalf("error %q must mention destination digest %q", err.Error(), tt.report.DestinationDigest)
			}
		})
	}
}
