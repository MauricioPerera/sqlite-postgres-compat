package compat

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
)

// SnapshotDigest creates a deterministic data-integrity proof for a canonical
// snapshot. Row order is normalized; table, column and value names are encoded
// by encoding/json with stable map ordering.
func SnapshotDigest(snapshot Snapshot) (string, error) {
	normalized := Snapshot{Schema: snapshot.Schema, Rows: make(map[string][]Row, len(snapshot.Rows))}
	for table, rows := range snapshot.Rows {
		copyRows := append([]Row(nil), rows...)
		sort.Slice(copyRows, func(i, j int) bool {
			left, _ := json.Marshal(copyRows[i])
			right, _ := json.Marshal(copyRows[j])
			return string(left) < string(right)
		})
		normalized.Rows[table] = copyRows
	}
	data, err := json.Marshal(normalized)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}

type VerificationReport struct {
	SourceDigest      string `json:"source_digest"`
	DestinationDigest string `json:"destination_digest"`
	Equivalent        bool   `json:"equivalent"`
}

func VerifySnapshots(source, destination Snapshot) (VerificationReport, error) {
	sourceDigest, err := SnapshotDigest(source)
	if err != nil {
		return VerificationReport{}, err
	}
	destinationDigest, err := SnapshotDigest(destination)
	if err != nil {
		return VerificationReport{}, err
	}
	return VerificationReport{
		SourceDigest:      sourceDigest,
		DestinationDigest: destinationDigest,
		Equivalent:        sourceDigest == destinationDigest,
	}, nil
}

func RequireEquivalent(report VerificationReport) error {
	if !report.Equivalent {
		return fmt.Errorf("snapshot mismatch: source %s destination %s", report.SourceDigest, report.DestinationDigest)
	}
	return nil
}
