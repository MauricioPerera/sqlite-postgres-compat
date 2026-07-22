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
			want: Expression{Kind: "not", Args: []Expression{column("a")}},
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
