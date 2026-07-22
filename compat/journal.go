package compat

import (
	"fmt"
	"sort"
	"time"
)

// Value keeps data independent from either driver's Go representation. Values
// are encoded canonically before persistence in a migration artifact or change
// journal, preserving null and type information across both engines.
type Value struct {
	Kind  ValueKind `json:"kind"`
	Value string    `json:"value,omitempty"`
}

type ValueKind string

const (
	NullValue      ValueKind = "null"
	BooleanValue   ValueKind = "boolean"
	IntegerValue   ValueKind = "integer"
	DecimalValue   ValueKind = "decimal"
	FloatValue     ValueKind = "float"
	TextValue      ValueKind = "text"
	BinaryValue    ValueKind = "binary"
	DateValue      ValueKind = "date"
	TimestampValue ValueKind = "timestamp"
	JSONValue      ValueKind = "json"
	UUIDValue      ValueKind = "uuid"
)

type Row map[string]Value

type ChangeKind string

const (
	Insert ChangeKind = "insert"
	Update ChangeKind = "update"
	Delete ChangeKind = "delete"
)

// Change is an engine-neutral mutation. Sequence is monotonically increasing
// per source, which lets a destination apply the same ordered history.
type Change struct {
	Source        Target     `json:"source"`
	Sequence      uint64     `json:"sequence"`
	CommittedAt   time.Time  `json:"committed_at"`
	Kind          ChangeKind `json:"kind"`
	Table         string     `json:"table"`
	PrimaryKey    Row        `json:"primary_key"`
	Before        Row        `json:"before,omitempty"`
	After         Row        `json:"after,omitempty"`
	TransactionID string     `json:"transaction_id"`
}

func (change Change) Validate() error {
	if err := change.Source.Validate(); err != nil {
		return err
	}
	if change.Sequence == 0 {
		return fmt.Errorf("change sequence must be positive")
	}
	if change.Table == "" {
		return fmt.Errorf("change table is required")
	}
	if len(change.PrimaryKey) == 0 {
		return fmt.Errorf("change primary key is required")
	}
	switch change.Kind {
	case Insert:
		if len(change.After) == 0 {
			return fmt.Errorf("insert requires an after row")
		}
	case Update:
		if len(change.Before) == 0 || len(change.After) == 0 {
			return fmt.Errorf("update requires before and after rows")
		}
	case Delete:
		if len(change.Before) == 0 {
			return fmt.Errorf("delete requires a before row")
		}
	default:
		return fmt.Errorf("unknown change kind %q", change.Kind)
	}
	return nil
}

// OrderedChanges rejects ambiguous or out-of-order histories before applying
// them to the other engine.
func OrderedChanges(changes []Change) ([]Change, error) {
	ordered := append([]Change(nil), changes...)
	sort.SliceStable(ordered, func(i, j int) bool {
		return ordered[i].Sequence < ordered[j].Sequence
	})
	for i, change := range ordered {
		if err := change.Validate(); err != nil {
			return nil, fmt.Errorf("change %d: %w", i, err)
		}
		if i > 0 && ordered[i-1].Source == change.Source && ordered[i-1].Sequence == change.Sequence {
			return nil, fmt.Errorf("duplicate sequence %d for %s", change.Sequence, change.Source.Engine)
		}
	}
	return ordered, nil
}
