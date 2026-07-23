package compat

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseCatalogExpression(t *testing.T) {
	want := Expression{Kind: "and", Args: []Expression{
		{Kind: "gte", Args: []Expression{{Kind: "column", Value: "price"}, {Kind: "integer", Value: "0"}}},
		{Kind: "eq", Args: []Expression{{Kind: "column", Value: "active"}, {Kind: "boolean", Value: "true"}}},
	}}
	got, err := parseCatalogExpression(`(("price" >= 0) AND ("active" = TRUE))`)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestParseCatalogExpressionRejectsUnknownSQL(t *testing.T) {
	if _, err := parseCatalogExpression(`vendor_function(price)`); err == nil {
		t.Fatal("expected unsupported SQL to be rejected")
	}
}

func TestParseCatalogExpressionConcat(t *testing.T) {
	column := func(name string) Expression { return Expression{Kind: "column", Value: name} }
	str := func(v string) Expression { return Expression{Kind: "string", Value: v} }

	tests := []struct {
		name  string
		input string
		want  Expression
	}{
		{
			name:  "simple concat",
			input: "a || b",
			want:  Expression{Kind: "concat", Args: []Expression{column("a"), column("b")}},
		},
		{
			name:  "concat is left associative",
			input: "a || b || c",
			want: Expression{Kind: "concat", Args: []Expression{
				{Kind: "concat", Args: []Expression{column("a"), column("b")}},
				column("c"),
			}},
		},
		{
			name:  "concat binds tighter than comparison",
			input: "a || b = c",
			want: Expression{Kind: "eq", Args: []Expression{
				{Kind: "concat", Args: []Expression{column("a"), column("b")}},
				column("c"),
			}},
		},
		{
			name:  "concat binds looser than addition",
			input: "a || b + c",
			want: Expression{Kind: "concat", Args: []Expression{
				column("a"),
				{Kind: "add", Args: []Expression{column("b"), column("c")}},
			}},
		},
		{
			name:  "concat with string literals",
			input: "first || ' ' || last",
			want: Expression{Kind: "concat", Args: []Expression{
				{Kind: "concat", Args: []Expression{column("first"), str(" ")}},
				column("last"),
			}},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseCatalogExpression(test.input)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("got %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestParseCatalogExpressionNotLike(t *testing.T) {
	column := func(name string) Expression { return Expression{Kind: "column", Value: name} }
	str := func(v string) Expression { return Expression{Kind: "string", Value: v} }
	like := func(l, r Expression) Expression { return Expression{Kind: "like", Args: []Expression{l, r}} }

	tests := []struct {
		name  string
		input string
		want  Expression
	}{
		{
			name:  "infix not like folds to not(like)",
			input: "name NOT LIKE 'x%'",
			want:  Expression{Kind: "not", Args: []Expression{like(column("name"), str("x%"))}},
		},
		{
			name:  "prefix not like still works",
			input: "NOT name LIKE 'x%'",
			want:  Expression{Kind: "not", Args: []Expression{like(column("name"), str("x%"))}},
		},
		{
			name:  "not like combined with and",
			input: "a NOT LIKE 'x%' AND b = 1",
			want: Expression{Kind: "and", Args: []Expression{
				{Kind: "not", Args: []Expression{like(column("a"), str("x%"))}},
				{Kind: "eq", Args: []Expression{column("b"), {Kind: "integer", Value: "1"}}},
			}},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseCatalogExpression(test.input)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("got %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestParseCatalogExpressionScalarFunctions(t *testing.T) {
	column := func(name string) Expression { return Expression{Kind: "column", Value: name} }
	str := func(v string) Expression { return Expression{Kind: "string", Value: v} }

	tests := []struct {
		name  string
		input string
		want  Expression
	}{
		{
			name:  "length in comparison",
			input: "length(a) = 5",
			want: Expression{Kind: "eq", Args: []Expression{
				{Kind: "length", Args: []Expression{column("a")}},
				{Kind: "integer", Value: "5"},
			}},
		},
		{
			name:  "abs single argument",
			input: "abs(a)",
			want:  Expression{Kind: "abs", Args: []Expression{column("a")}},
		},
		{
			name:  "trim single argument",
			input: "trim(a)",
			want:  Expression{Kind: "trim", Args: []Expression{column("a")}},
		},
		{
			name:  "coalesce variadic",
			input: "coalesce(a, b, 'x')",
			want: Expression{Kind: "coalesce", Args: []Expression{
				column("a"), column("b"), str("x"),
			}},
		},
		{
			name:  "replace three arguments",
			input: "replace(a, 'x', 'y')",
			want: Expression{Kind: "replace", Args: []Expression{
				column("a"), str("x"), str("y"),
			}},
		},
		{
			name:  "nested function call in coalesce argument",
			input: "coalesce(a, trim(b))",
			want: Expression{Kind: "coalesce", Args: []Expression{
				column("a"),
				{Kind: "trim", Args: []Expression{column("b")}},
			}},
		},
		{
			name:  "coalesce with empty string literal argument",
			input: "coalesce(a, '')",
			want: Expression{Kind: "coalesce", Args: []Expression{
				column("a"), {Kind: "string", Value: ""},
			}},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseCatalogExpression(test.input)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("got %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestParseCatalogExpressionRejectsUnlistedFunction(t *testing.T) {
	if _, err := parseCatalogExpression(`substr(a, 1, 2)`); err == nil {
		t.Fatal("expected unlisted function substr to be rejected")
	}
	if _, err := parseCatalogExpression(`replace(a, 'x')`); err == nil {
		t.Fatal("expected replace with wrong arity to be rejected")
	}
}

func TestParsePostgresCatalogDefaultRemovesKnownLiteralCast(t *testing.T) {
	got, err := parsePostgresCatalogDefault(`'new'::text`)
	if err != nil {
		t.Fatal(err)
	}
	want := Expression{Kind: "string", Value: "new"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
	if _, err := parsePostgresCatalogDefault(`nextval('items_id_seq'::regclass)`); err == nil {
		t.Fatal("expected sequence default to remain unsupported")
	}
}

func TestParseCatalogExpressionNotPrecedence(t *testing.T) {
	column := func(name string) Expression { return Expression{Kind: "column", Value: name} }

	tests := []struct {
		name  string
		input string
		want  Expression
	}{
		{
			name:  "not binds looser than equality",
			input: "NOT a = b",
			want: Expression{Kind: "not", Args: []Expression{
				{Kind: "eq", Args: []Expression{column("a"), column("b")}},
			}},
		},
		{
			name:  "not binds looser than is null",
			input: "NOT a IS NULL",
			want: Expression{Kind: "not", Args: []Expression{
				{Kind: "is_null", Args: []Expression{column("a")}},
			}},
		},
		{
			name:  "not binds looser than like",
			input: "NOT a LIKE 'x%'",
			want: Expression{Kind: "not", Args: []Expression{
				{Kind: "like", Args: []Expression{column("a"), {Kind: "string", Value: "x%"}}},
			}},
		},
		{
			name:  "not binds tighter than and on the right hand side",
			input: "a AND NOT b = c",
			want: Expression{Kind: "and", Args: []Expression{
				column("a"),
				{Kind: "not", Args: []Expression{
					{Kind: "eq", Args: []Expression{column("b"), column("c")}},
				}},
			}},
		},
		{
			name:  "explicit parentheses keep not around the comparison",
			input: "NOT (a = b)",
			want: Expression{Kind: "not", Args: []Expression{
				{Kind: "eq", Args: []Expression{column("a"), column("b")}},
			}},
		},
		{
			name:  "not over a bare column does not regress",
			input: "NOT a",
			want:  Expression{Kind: "not", Args: []Expression{column("a")}},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseCatalogExpression(test.input)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("got %#v, want %#v", got, test.want)
			}
		})
	}
}

// TestParseCatalogExpressionIsNullToleratesInternalWhitespace covers the
// AUDIT7-A MEDIA H1 fix: SQLite and Postgres treat any run of whitespace as a
// separator, so the multi-word operators "IS NULL" and "IS NOT NULL" must parse
// identically whether their words are separated by a single space, multiple
// spaces or a tab. The existing comparison/AND/OR paths already tolerate this
// via keywordMatchSpan; the expression operators had been left strict.
func TestParseCatalogExpressionIsNullToleratesInternalWhitespace(t *testing.T) {
	column := func(name string) Expression { return Expression{Kind: "column", Value: name} }
	wantNull := Expression{Kind: "is_null", Args: []Expression{column("col")}}
	wantNotNull := Expression{Kind: "is_not_null", Args: []Expression{column("col")}}

	tests := []struct {
		name  string
		input string
		want  Expression
	}{
		{"IS NULL single space", "col IS NULL", wantNull},
		{"IS NULL double space", "col IS  NULL", wantNull},
		{"IS NULL tab separator", "col IS\tNULL", wantNull},
		{"IS NULL newline separator", "col IS\nNULL", wantNull},
		{"IS NOT NULL single space", "col IS NOT NULL", wantNotNull},
		{"IS NOT NULL double spaces", "col IS  NOT  NULL", wantNotNull},
		{"IS NOT NULL tab separators", "col IS\tNOT\tNULL", wantNotNull},
		{"IS NOT NULL mixed whitespace", "col IS \t NOT \t NULL", wantNotNull},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseCatalogExpression(test.input)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("got %#v, want %#v", got, test.want)
			}
			// The compiled output must be the canonical single-space form for
			// both engines regardless of the input whitespace.
			for _, engine := range []Engine{SQLite, Postgres} {
				compiled, err := compileExpression(engine, got)
				if err != nil {
					t.Fatalf("%s compile error: %v", engine, err)
				}
				wantSub := "IS NULL"
				if got.Kind == "is_not_null" {
					wantSub = "IS NOT NULL"
				}
				if !strings.Contains(compiled, `("col" `+wantSub+`)`) {
					t.Fatalf("%s compiled %q, want substring %q", engine, compiled, `("col" `+wantSub+`)`)
				}
			}
		})
	}
}

// TestParseCatalogExpressionIsNullWhitespaceInCompound verifies the flexible
// whitespace works inside larger expressions (precedence with AND/OR and
// comparisons), not only as a standalone predicate.
func TestParseCatalogExpressionIsNullWhitespaceInCompound(t *testing.T) {
	column := func(name string) Expression { return Expression{Kind: "column", Value: name} }
	want := Expression{Kind: "and", Args: []Expression{
		{Kind: "is_not_null", Args: []Expression{column("a")}},
		{Kind: "eq", Args: []Expression{column("b"), {Kind: "integer", Value: "1"}}},
	}}
	got, err := parseCatalogExpression("a IS  NOT  NULL AND b = 1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

// TestParseCatalogExpressionNumericVersusIdentifier covers the MEDIA-1 fix:
// a bare exponent such as "E5" or "e10" has no mantissa digit before the e/E,
// so SQLite treats it as an identifier (column), not a number. The previous
// "at least one digit over [0-9.+-eE]" check classified these as decimal
// literals and emitted them unquoted, which folded to the wrong column. The
// table checks both the parsed classification and the compiled DDL output:
// columns are quoted, real numeric literals are emitted verbatim.
func TestParseCatalogExpressionNumericVersusIdentifier(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		kind     string
		value    string
		compiled string
	}{
		// Bare exponents: identifiers (columns), quoted in DDL.
		{`e5 column lowercase`, "e5", "column", "e5", `"e5"`},
		{`E5 column uppercase`, "E5", "column", "E5", `"E5"`},
		{`e10 column lowercase`, "e10", "column", "e10", `"e10"`},
		{`E10 column uppercase`, "E10", "column", "E10", `"E10"`},
		{`E3 column uppercase`, "E3", "column", "E3", `"E3"`},
		// Real numeric literals: mantissa present, emitted verbatim.
		{`1e5 decimal`, "1e5", "decimal", "1e5", "1e5"},
		{`1E5 decimal uppercase exponent`, "1E5", "decimal", "1E5", "1E5"},
		{`.5e3 decimal leading dot`, ".5e3", "decimal", ".5e3", ".5e3"},
		{`0.5 decimal`, "0.5", "decimal", "0.5", "0.5"},
		{`0 integer`, "0", "integer", "0", "0"},
		{`16 integer`, "16", "integer", "16", "16"},
		// Hex literal is handled separately and reinterpreted as int64.
		{`0x1F hex`, "0x1F", "integer", "31", "31"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseCatalogExpression(test.input)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.Kind != test.kind || got.Value != test.value {
				t.Fatalf("classification got {Kind:%q Value:%q}, want {Kind:%q Value:%q}", got.Kind, got.Value, test.kind, test.value)
			}
			compiled, err := compileExpression(Postgres, got)
			if err != nil {
				t.Fatalf("compile error: %v", err)
			}
			if compiled != test.compiled {
				t.Fatalf("compiled got %q, want %q", compiled, test.compiled)
			}
		})
	}
}

// TestParseCatalogExpressionExponentColumnInComparison checks that an "E5"
// reference in a CHECK-like predicate classifies as a column and is emitted
// quoted, so a column created as "E5" resolves instead of folding to "e5".
func TestParseCatalogExpressionExponentColumnInComparison(t *testing.T) {
	got, err := parseCatalogExpression("E5 > 0")
	if err != nil {
		t.Fatal(err)
	}
	want := Expression{Kind: "gt", Args: []Expression{
		{Kind: "column", Value: "E5"},
		{Kind: "integer", Value: "0"},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
	compiled, err := compileExpression(Postgres, got)
	if err != nil {
		t.Fatal(err)
	}
	if want := `("E5" > 0)`; compiled != want {
		t.Fatalf("compiled got %q, want %q", compiled, want)
	}
}

// TestParseCatalogExpressionExponentPrefixedMixedForms documents the SQLite
// grammar's verdict on exponent-prefixed forms with a dot. "e5.5" has no
// mantissa digit before the e/E, so it is not a number; it falls through to
// the identifier rule, which treats the dot as a qualifier separator and
// yields the column reference e5.5. (A bare ".5" without an exponent stays a
// number, so ".5e3" above is decimal.)
func TestParseCatalogExpressionExponentPrefixedMixedForms(t *testing.T) {
	tests := []struct {
		name  string
		input string
		kind  string
		value string
	}{
		{`e5.5 is a qualified column, not a number`, "e5.5", "column", "e5.5"},
		{`E5.5 is a qualified column, not a number`, "E5.5", "column", "E5.5"},
		{`.5e3 with mantissa is a decimal`, ".5e3", "decimal", ".5e3"},
		{`1. is a decimal`, "1.", "decimal", "1."},
		// "1e" has a mantissa but an exponent with no digits, so it is not a
		// number; it falls through to the identifier rule as column "1e" (a
		// digit-led identifier, same edge as the existing BAJA-class behavior).
		{`1e exponent with no digits is not a number`, "1e", "column", "1e"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseCatalogExpression(test.input)
			if test.kind == "unsupported" {
				if err == nil {
					t.Fatalf("expected %q to be rejected, got %#v", test.input, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.Kind != test.kind || got.Value != test.value {
				t.Fatalf("got {Kind:%q Value:%q}, want {Kind:%q Value:%q}", got.Kind, got.Value, test.kind, test.value)
			}
		})
	}
}

func TestParseCatalogExpressionHexLiteral(t *testing.T) {
	column := func(name string) Expression { return Expression{Kind: "column", Value: name} }

	tests := []struct {
		name      string
		input     string
		want      Expression
		wantError string
	}{
		{
			name:  "lowercase hex prefix",
			input: "status = 0x10",
			want: Expression{Kind: "eq", Args: []Expression{
				column("status"),
				{Kind: "integer", Value: "16"},
			}},
		},
		{
			name:  "uppercase hex prefix and digits",
			input: "x = 0XABCDEF",
			want: Expression{Kind: "eq", Args: []Expression{
				column("x"),
				{Kind: "integer", Value: "11259375"},
			}},
		},
		{
			name:  "all bits set is negative one in two's complement",
			input: "x = 0xFFFFFFFFFFFFFFFF",
			want: Expression{Kind: "eq", Args: []Expression{
				column("x"),
				{Kind: "integer", Value: "-1"},
			}},
		},
		{
			name:  "high bit set is the minimum int64",
			input: "x = 0x8000000000000000",
			want: Expression{Kind: "eq", Args: []Expression{
				column("x"),
				{Kind: "integer", Value: "-9223372036854775808"},
			}},
		},
		{
			name:  "max positive int64 value",
			input: "x = 0x7FFFFFFFFFFFFFFF",
			want: Expression{Kind: "eq", Args: []Expression{
				column("x"),
				{Kind: "integer", Value: "9223372036854775807"},
			}},
		},
		{
			name:  "small hex value is unchanged by sign interpretation",
			input: "x = 0x10",
			want: Expression{Kind: "eq", Args: []Expression{
				column("x"),
				{Kind: "integer", Value: "16"},
			}},
		},
		{
			name:      "hex literal beyond 64 bits is rejected",
			input:     "0xFFFFFFFFFFFFFFFFF",
			wantError: "unsupported",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseCatalogExpression(test.input)
			if test.wantError != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", test.wantError)
				}
				if !strings.Contains(err.Error(), test.wantError) {
					t.Fatalf("expected error containing %q, got %q", test.wantError, err.Error())
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("got %#v, want %#v", got, test.want)
			}
		})
	}
}
