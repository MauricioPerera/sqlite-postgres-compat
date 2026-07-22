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

func TestParseCatalogSelectWithJoinAndAggregation(t *testing.T) {
	query, err := parseCatalogSelect(`SELECT p.id, count(c.id) AS total FROM parents AS p LEFT JOIN children AS c ON c.parent_id = p.id GROUP BY p.id`)
	if err != nil {
		t.Fatal(err)
	}
	if len(query.Joins) != 1 || query.Joins[0].Kind != "left" || len(query.GroupBy) != 1 || query.Columns[1].Expression.Kind != "count" {
		t.Fatalf("unexpected joined query: %+v", query)
	}
}
