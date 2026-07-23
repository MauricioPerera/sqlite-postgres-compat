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

// TestSchemaValidateAcceptsGeneratedStoredColumn confirms a well-formed STORED
// generated column (no default, not in the primary key) validates.
func TestSchemaValidateAcceptsGeneratedStoredColumn(t *testing.T) {
	if err := generatedColumnTestSchema("gen_ok").Validate(); err != nil {
		t.Fatalf("valid STORED generated column must pass validation: %v", err)
	}
}

// TestSchemaValidateRejectsInvalidGeneratedColumns freezes the three canonical
// rejections: VIRTUAL (Stored=false), generated + default together, and a
// generated column inside a primary key. All three are refused by both engines.
func TestSchemaValidateRejectsInvalidGeneratedColumns(t *testing.T) {
	genExpr := func() *GeneratedColumn {
		return &GeneratedColumn{Stored: true, Expression: Expression{Kind: "mul", Args: []Expression{
			{Kind: "column", Value: "price"},
			{Kind: "column", Value: "quantity"},
		}}}
	}
	baseColumns := func() []Column {
		return []Column{
			{Name: "id", Type: Type{Family: IntegerType}},
			{Name: "price", Type: Type{Family: IntegerType}},
			{Name: "quantity", Type: Type{Family: IntegerType}},
		}
	}
	cases := []struct {
		name  string
		table Table
		want  string
	}{
		{
			name: "virtual",
			table: Table{
				Name:        "gen_virtual",
				Columns:     append(baseColumns(), Column{Name: "total", Type: Type{Family: IntegerType}, Generated: &GeneratedColumn{Stored: false, Expression: Expression{Kind: "column", Value: "price"}}}),
				Constraints: []Constraint{{Kind: PrimaryKey, Columns: []string{"id"}}},
			},
			want: "STORED",
		},
		{
			name: "generated_with_default",
			table: Table{
				Name: "gen_default",
				Columns: append(baseColumns(), Column{Name: "total", Type: Type{Family: IntegerType},
					Default:   &Expression{Kind: "integer", Value: "0"},
					Generated: genExpr()}),
				Constraints: []Constraint{{Kind: PrimaryKey, Columns: []string{"id"}}},
			},
			want: "default",
		},
		{
			name: "generated_in_primary_key",
			table: Table{
				Name:        "gen_pk",
				Columns:     append(baseColumns(), Column{Name: "total", Type: Type{Family: IntegerType}, Generated: genExpr()}),
				Constraints: []Constraint{{Kind: PrimaryKey, Columns: []string{"total"}}},
			},
			want: "primary key",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := (Schema{Tables: []Table{tc.table}}).Validate()
			if err == nil {
				t.Fatalf("expected %s to be rejected", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q must mention %q", err.Error(), tc.want)
			}
		})
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

// TestSchemaValidateExpressionIndex freezes the validation rules for expression
// index keys: a valid expression key passes (grammar validity is enforced later
// at compile time), and a key that carries both a column name and an expression
// is rejected.
func TestSchemaValidateExpressionIndex(t *testing.T) {
	lowerEmail := Expression{Kind: "lower", Args: []Expression{{Kind: "column", Value: "email"}}}
	base := func(cols []IndexColumn) Schema {
		return Schema{
			Tables: []Table{{
				Name:    "users",
				Columns: []Column{{Name: "email", Type: Type{Family: TextType}}},
			}},
			Indexes: []Index{{Name: "users_idx", Table: "users", Columns: cols}},
		}
	}
	if err := base([]IndexColumn{{Expression: &lowerEmail}}).Validate(); err != nil {
		t.Fatalf("valid expression index must pass validation: %v", err)
	}
	err := base([]IndexColumn{{Column: "email", Expression: &lowerEmail}}).Validate()
	if err == nil || !strings.Contains(err.Error(), "both a column and an expression") {
		t.Fatalf("index key with both a column and an expression must be rejected, got: %v", err)
	}
}

// TestSchemaValidateAcceptsDomain confirms a well-formed schema with a domain
// used by a neutral column validates.
func TestSchemaValidateAcceptsDomain(t *testing.T) {
	check := Expression{Kind: "gt", Args: []Expression{{Kind: "domain_value"}, {Kind: "integer", Value: "0"}}}
	schema := Schema{
		Domains: []Domain{{Name: "pos", Type: Type{Family: IntegerType}, Check: &check, NotNull: true}},
		Tables: []Table{{
			Name:    "t",
			Columns: []Column{{Name: "n", Type: Type{Family: IntegerType}, Nullable: true, DomainRef: "pos"}},
		}},
	}
	if err := schema.Validate(); err != nil {
		t.Fatalf("valid domain schema must pass validation: %v", err)
	}
}

// TestSchemaValidateRejectsInvalidDomains freezes the canonical domain
// rejections: a duplicate/unnamed/untyped domain, a column referencing an unknown
// domain, a column whose type does not match the domain base type, and a
// domain-referencing column that is not neutral (NOT NULL, own default, or
// generated).
func TestSchemaValidateRejectsInvalidDomains(t *testing.T) {
	posCheck := Expression{Kind: "gt", Args: []Expression{{Kind: "domain_value"}, {Kind: "integer", Value: "0"}}}
	def := Expression{Kind: "integer", Value: "1"}
	gen := &GeneratedColumn{Stored: true, Expression: Expression{Kind: "integer", Value: "1"}}
	cases := []struct {
		name   string
		schema Schema
		want   string
	}{
		{
			name:   "unnamed domain",
			schema: Schema{Domains: []Domain{{Type: Type{Family: IntegerType}}}},
			want:   "domain name is required",
		},
		{
			name:   "duplicate domain",
			schema: Schema{Domains: []Domain{{Name: "d", Type: Type{Family: IntegerType}}, {Name: "d", Type: Type{Family: TextType}}}},
			want:   "duplicate domain",
		},
		{
			name:   "domain without base type",
			schema: Schema{Domains: []Domain{{Name: "d"}}},
			want:   "has no base type",
		},
		{
			name:   "domain unknown base type",
			schema: Schema{Domains: []Domain{{Name: "d", Type: Type{Family: "nope"}}}},
			want:   "unsupported base type",
		},
		{
			name: "unknown domain reference",
			schema: Schema{Tables: []Table{{
				Name:    "t",
				Columns: []Column{{Name: "c", Type: Type{Family: IntegerType}, Nullable: true, DomainRef: "missing"}},
			}}},
			want: "references unknown domain",
		},
		{
			name: "type mismatch",
			schema: Schema{
				Domains: []Domain{{Name: "pos", Type: Type{Family: IntegerType}, Check: &posCheck}},
				Tables: []Table{{
					Name:    "t",
					Columns: []Column{{Name: "c", Type: Type{Family: TextType}, Nullable: true, DomainRef: "pos"}},
				}},
			},
			want: "must match domain",
		},
		{
			name: "not neutral not null",
			schema: Schema{
				Domains: []Domain{{Name: "pos", Type: Type{Family: IntegerType}}},
				Tables: []Table{{
					Name:    "t",
					Columns: []Column{{Name: "c", Type: Type{Family: IntegerType}, DomainRef: "pos"}},
				}},
			},
			want: "must be nullable",
		},
		{
			name: "not neutral own default",
			schema: Schema{
				Domains: []Domain{{Name: "pos", Type: Type{Family: IntegerType}}},
				Tables: []Table{{
					Name:    "t",
					Columns: []Column{{Name: "c", Type: Type{Family: IntegerType}, Nullable: true, Default: &def, DomainRef: "pos"}},
				}},
			},
			want: "cannot have its own default",
		},
		{
			name: "not neutral generated",
			schema: Schema{
				Domains: []Domain{{Name: "pos", Type: Type{Family: IntegerType}}},
				Tables: []Table{{
					Name:    "t",
					Columns: []Column{{Name: "c", Type: Type{Family: IntegerType}, Nullable: true, Generated: gen, DomainRef: "pos"}},
				}},
			},
			want: "cannot be generated",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.schema.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected error containing %q, got: %v", tc.want, err)
			}
		})
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
