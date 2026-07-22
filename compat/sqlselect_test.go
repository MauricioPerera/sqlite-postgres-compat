package compat

import "testing"

func TestParseCatalogSelectCommonView(t *testing.T) {
	query, err := parseCatalogSelect(`CREATE VIEW active_products AS SELECT code AS product_code, price FROM products WHERE active = TRUE ORDER BY price DESC LIMIT 10`)
	if err != nil {
		t.Fatal(err)
	}
	if query.From.Table != "products" || len(query.Columns) != 2 || query.Columns[0].Alias != "product_code" || query.Where == nil || len(query.OrderBy) != 1 || !query.OrderBy[0].Descending || query.Limit == nil || *query.Limit != 10 {
		t.Fatalf("unexpected query: %+v", query)
	}
}

func TestParseCatalogSelectRejectsJoinUntilSemanticsAreTranslated(t *testing.T) {
	if _, err := parseCatalogSelect(`SELECT a.id FROM a JOIN b ON b.id = a.id`); err == nil {
		t.Fatal("expected join to be rejected")
	}
}
