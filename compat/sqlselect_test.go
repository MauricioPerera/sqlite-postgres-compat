package compat

import (
	"strings"
	"testing"
)

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

func TestParseCatalogSelectRejectsNegativeLimit(t *testing.T) {
	_, err := parseCatalogSelect(`SELECT a FROM t LIMIT -1`)
	if err == nil {
		t.Fatalf("expected error for negative LIMIT, got nil")
	}
	if !strings.Contains(err.Error(), "unsupported") && !strings.Contains(err.Error(), "negative") {
		t.Fatalf("expected unsupported/negative error, got %q", err.Error())
	}
}

func TestParseCatalogSelectRejectsNegativeOffset(t *testing.T) {
	_, err := parseCatalogSelect(`SELECT a FROM t OFFSET -5`)
	if err == nil {
		t.Fatalf("expected error for negative OFFSET, got nil")
	}
	if !strings.Contains(err.Error(), "unsupported") && !strings.Contains(err.Error(), "negative") {
		t.Fatalf("expected unsupported/negative error, got %q", err.Error())
	}
}

func TestParseCatalogSelectAllowsZeroLimitAndOffset(t *testing.T) {
	query, err := parseCatalogSelect(`SELECT a FROM t LIMIT 0`)
	if err != nil {
		t.Fatalf("unexpected error for LIMIT 0: %v", err)
	}
	if query.Limit == nil || *query.Limit != 0 {
		t.Fatalf("expected LIMIT 0, got %+v", query.Limit)
	}

	query, err = parseCatalogSelect(`SELECT a FROM t LIMIT 10 OFFSET 0`)
	if err != nil {
		t.Fatalf("unexpected error for LIMIT 10 OFFSET 0: %v", err)
	}
	if query.Limit == nil || *query.Limit != 10 || query.Offset == nil || *query.Offset != 0 {
		t.Fatalf("expected LIMIT 10 OFFSET 0, got %+v", query)
	}
}
