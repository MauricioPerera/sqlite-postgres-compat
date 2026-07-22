package compat

import (
	"reflect"
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
