package compat

import "fmt"

type Feature string

const (
	Tables            Feature = "tables"
	PrimaryKeys       Feature = "primary_keys"
	ForeignKeys       Feature = "foreign_keys"
	CheckRules        Feature = "check_constraints"
	Transactions      Feature = "transactions"
	Indexes           Feature = "indexes"
	JSONValues        Feature = "json"
	UUIDValues        Feature = "uuid"
	Triggers          Feature = "triggers"
	CanonicalTriggers Feature = "canonical_triggers"
	Views             Feature = "views"
	CanonicalViews    Feature = "canonical_views"
	StoredRoutines    Feature = "stored_routines"
	CanonicalRoutines Feature = "canonical_routines"
	FullText          Feature = "full_text"
	CanonicalFullText Feature = "canonical_full_text"
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
	case Tables, PrimaryKeys, ForeignKeys, CheckRules, Transactions, Indexes, CanonicalViews, CanonicalTriggers, CanonicalRoutines, CanonicalFullText:
		return Finding{Feature: feature, Status: Exact}
	case JSONValues, UUIDValues:
		return Finding{Feature: feature, Status: Exact, Reason: "lossless canonical text representation"}
	case Triggers, Views, StoredRoutines, FullText:
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
	seen := map[Feature]struct{}{Tables: {}}
	if len(schema.Views) > 0 {
		seen[CanonicalViews] = struct{}{}
	}
	if len(schema.Triggers) > 0 {
		seen[CanonicalTriggers] = struct{}{}
	}
	if len(schema.Routines) > 0 {
		seen[CanonicalRoutines] = struct{}{}
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
				seen[ForeignKeys] = struct{}{}
			case Check:
				seen[CheckRules] = struct{}{}
			}
		}
	}
	features := make([]Feature, 0, len(seen))
	for feature := range seen {
		features = append(features, feature)
	}
	return features
}
