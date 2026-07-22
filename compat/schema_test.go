package compat

import (
	"strings"
	"testing"
)

func TestSchemaValidateRejectsUnknownTypeFamily(t *testing.T) {
	schema := Schema{Tables: []Table{{
		Name: "accounts",
		Columns: []Column{
			{Name: "id", Type: Type{Family: IntegerType}},
			{Name: "weird", Type: Type{Family: "nope"}},
		},
	}}}
	err := schema.Validate()
	if err == nil {
		t.Fatal("expected unknown type family to be rejected")
	}
	for _, want := range []string{`accounts`, `weird`, `nope`} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q must mention %q", err.Error(), want)
		}
	}
}

func TestSchemaValidateAcceptsEveryKnownTypeFamily(t *testing.T) {
	for _, family := range []TypeFamily{
		BooleanType, IntegerType, DecimalType, FloatType, TextType,
		BinaryType, DateType, TimestampType, JSONType, UUIDType,
	} {
		schema := Schema{Tables: []Table{{
			Name:    "t",
			Columns: []Column{{Name: "c", Type: Type{Family: family}}},
		}}}
		if err := schema.Validate(); err != nil {
			t.Fatalf("known family %q must validate: %v", family, err)
		}
	}
	// vector is known but requires a dimension argument; validate the family
	// is accepted (not rejected as unknown) when the dimension is supplied.
	vectorSchema := Schema{Tables: []Table{{
		Name:    "t",
		Columns: []Column{{Name: "c", Type: Type{Family: VectorType, Arguments: []int{3}}}},
	}}}
	if err := vectorSchema.Validate(); err != nil {
		t.Fatalf("vector family must validate with a dimension: %v", err)
	}
	// vector without a dimension must still fail on the dimension, not on the
	// family being "unsupported".
	vectorBad := Schema{Tables: []Table{{
		Name:    "t",
		Columns: []Column{{Name: "c", Type: Type{Family: VectorType}}},
	}}}
	if err := vectorBad.Validate(); err == nil || strings.Contains(err.Error(), "unsupported type family") {
		t.Fatalf("vector without dimension must fail on the dimension, not the family, got %v", err)
	}
}

func TestSchemaValidateRejectsUnknownTypeFamilyOnRoutineParameter(t *testing.T) {
	schema := Schema{
		Tables: []Table{{Name: "t", Columns: []Column{{Name: "id", Type: Type{Family: IntegerType}}}}},
		Routines: []Routine{{
			Name:    "r",
			Actions: []RoutineAction{{Kind: "insert", Table: "t"}},
			Parameters: []RoutineParameter{
				{Name: "good", Type: Type{Family: TextType}},
				{Name: "bad", Type: Type{Family: "nope"}},
			},
		}}}
	err := schema.Validate()
	if err == nil {
		t.Fatal("expected unknown parameter type family to be rejected")
	}
	for _, want := range []string{`r`, `bad`, `nope`} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q must mention %q", err.Error(), want)
		}
	}
}

func TestValidReferentialActionAcceptsCanonicalActions(t *testing.T) {
	for _, action := range []ReferentialAction{"", NoAction, Restrict, Cascade, SetNull, SetDefault} {
		if !validReferentialAction(action) {
			t.Fatalf("expected action %q to be valid", action)
		}
	}
}

func TestValidReferentialActionRejectsUnknownActions(t *testing.T) {
	tests := []struct {
		name   string
		action ReferentialAction
	}{
		{name: "postgres spelled no action", action: ReferentialAction("no action")},
		{name: "upper case cascade", action: ReferentialAction("CASCADE")},
		{name: "restrict with trailing space", action: ReferentialAction("restrict ")},
		{name: "arbitrary value", action: ReferentialAction("bogus")},
		{name: "whitespace only", action: ReferentialAction(" ")},
		{name: "set null with space", action: ReferentialAction("set null")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if validReferentialAction(tt.action) {
				t.Fatalf("expected action %q to be invalid", tt.action)
			}
		})
	}
}
