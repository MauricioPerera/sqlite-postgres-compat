package compat

import "fmt"

type Feature string

const (
	Tables               Feature = "tables"
	PrimaryKeys          Feature = "primary_keys"
	ForeignKeys          Feature = "foreign_keys"
	CanonicalForeignKeys Feature = "canonical_foreign_keys"
	CheckRules           Feature = "check_constraints"
	CanonicalChecks      Feature = "canonical_check_constraints"
	Transactions         Feature = "transactions"
	Indexes              Feature = "indexes"
	CanonicalIndexes     Feature = "canonical_indexes"
	JSONValues           Feature = "json"
	UUIDValues           Feature = "uuid"
	Triggers             Feature = "triggers"
	CanonicalTriggers    Feature = "canonical_triggers"
	Views                Feature = "views"
	CanonicalViews       Feature = "canonical_views"
	StoredRoutines       Feature = "stored_routines"
	CanonicalRoutines    Feature = "canonical_routines"
	FullText             Feature = "full_text"
	CanonicalFullText    Feature = "canonical_full_text"
)

type MappingStatus string

const (
	Exact       MappingStatus = "exact"
	Transformed MappingStatus = "transformed"
	Emulated    MappingStatus = "emulated"
	Unsupported MappingStatus = "unsupported"
	Unknown     MappingStatus = "unknown"
)

type Finding struct {
	Feature Feature       `json:"feature"`
	Status  MappingStatus `json:"status"`
	Reason  string        `json:"reason,omitempty"`
}

// Audit reports the current compatibility status. It never represents a
// transformed or emulated behavior as exact equivalence.
func Audit(contract Contract) ([]Finding, error) {
	if err := contract.Validate(); err != nil {
		return nil, err
	}
	findings := make([]Finding, 0, len(contract.RequiredFeatures))
	for _, feature := range contract.RequiredFeatures {
		findings = append(findings, assess(feature))
	}
	return findings, nil
}

func assess(feature Feature) Finding {
	switch feature {
	case Tables, PrimaryKeys, Transactions, CanonicalForeignKeys, CanonicalChecks, CanonicalIndexes, CanonicalViews, CanonicalTriggers, CanonicalRoutines, CanonicalFullText:
		return Finding{Feature: feature, Status: Exact}
	case JSONValues, UUIDValues:
		return Finding{Feature: feature, Status: Exact, Reason: "lossless canonical text representation"}
	case ForeignKeys, CheckRules, Indexes, Triggers, Views, StoredRoutines, FullText:
		return Finding{Feature: feature, Status: Unknown, Reason: "requires parser and semantic compiler"}
	default:
		return Finding{Feature: feature, Status: Unknown, Reason: "feature is not in the compatibility catalog"}
	}
}

func RequireExact(findings []Finding) error {
	for _, finding := range findings {
		if finding.Status != Exact {
			return fmt.Errorf("feature %q is %s: %s", finding.Feature, finding.Status, finding.Reason)
		}
	}
	return nil
}

// InferFeatures derives the minimum capabilities required by a canonical
// schema. Migration callers use this to prevent a schema from claiming an
// exact plan while omitting its difficult capabilities from the contract.
func InferFeatures(schema Schema) []Feature {
	// CanonicalFullText is provided unconditionally by the portable SearchText
	// runtime, independent of the schema's tables, so it is always inferred —
	// mirroring how Tables is seeded below rather than gated on a schema check.
	seen := map[Feature]struct{}{Tables: {}, CanonicalFullText: {}}
	if len(schema.Views) > 0 {
		seen[CanonicalViews] = struct{}{}
	}
	if len(schema.Triggers) > 0 {
		seen[CanonicalTriggers] = struct{}{}
	}
	if len(schema.Routines) > 0 {
		seen[CanonicalRoutines] = struct{}{}
	}
	if len(schema.Indexes) > 0 {
		seen[CanonicalIndexes] = struct{}{}
	}
	for _, table := range schema.Tables {
		for _, column := range table.Columns {
			switch column.Type.Family {
			case JSONType:
				seen[JSONValues] = struct{}{}
			case UUIDType:
				seen[UUIDValues] = struct{}{}
			}
		}
		for _, constraint := range table.Constraints {
			switch constraint.Kind {
			case PrimaryKey:
				seen[PrimaryKeys] = struct{}{}
			case ForeignKey:
				seen[CanonicalForeignKeys] = struct{}{}
			case Check:
				seen[CanonicalChecks] = struct{}{}
			}
		}
	}
	features := make([]Feature, 0, len(seen))
	for feature := range seen {
		features = append(features, feature)
	}
	return features
}
