package compat

import (
	"reflect"
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

// TestParseCatalogSelectRejectsEmptyClauseOperand covers the BAJA-N1 fix: a
// clause keyword with no operand (e.g. "GROUP BY" at the end of the string) is a
// syntax error in SQLite and Postgres. Every clause that takes an operand must
// reject the empty form with a clear parse error rather than accepting it and
// silently dropping the clause at emit time.
func TestParseCatalogSelectRejectsEmptyClauseOperand(t *testing.T) {
	cases := []struct {
		name  string
		input string
	}{
		{"GROUP BY empty", "SELECT a FROM t GROUP BY"},
		{"GROUP BY empty extra whitespace", "SELECT a FROM t GROUP  BY "},
		{"ORDER BY empty", "SELECT a FROM t ORDER BY"},
		{"ORDER BY empty extra whitespace", "SELECT a FROM t ORDER\tBY\t"},
		{"HAVING empty", "SELECT a FROM t HAVING"},
		{"WHERE empty", "SELECT a FROM t WHERE"},
		{"LIMIT empty", "SELECT a FROM t LIMIT"},
		{"OFFSET empty", "SELECT a FROM t OFFSET"},
		{"LIMIT empty with OFFSET", "SELECT a FROM t LIMIT 10 OFFSET"},
		{"GROUP BY empty with ORDER BY operand", "SELECT a FROM t GROUP BY ORDER BY b"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseCatalogSelect(test.input)
			if err == nil {
				t.Fatalf("expected error for empty operand, got nil")
			}
			if !strings.Contains(err.Error(), "no operand") {
				t.Fatalf("expected \"no operand\" error, got %q", err.Error())
			}
		})
	}
}

// TestParseCatalogSelectToleratesKeywordInternalWhitespace covers the MEDIA-2
// fix: SQLite and Postgres treat any run of whitespace as a separator, so a
// multi-word keyword such as "GROUP BY" must still match when its words are
// separated by multiple spaces or a tab. The parsed query must be identical
// to the single-space form.
func TestParseCatalogSelectToleratesKeywordInternalWhitespace(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		single     string
		wantGroup  int
		wantOrder  int
		wantHaving bool
	}{
		{
			name:      "GROUP BY single space",
			input:     "SELECT a FROM t GROUP BY x",
			single:    "SELECT a FROM t GROUP BY x",
			wantGroup: 1,
		},
		{
			name:      "GROUP BY double space",
			input:     "SELECT a FROM t GROUP  BY x",
			single:    "SELECT a FROM t GROUP BY x",
			wantGroup: 1,
		},
		{
			name:      "GROUP BY tab separated",
			input:     "SELECT a FROM t GROUP\tBY x",
			single:    "SELECT a FROM t GROUP BY x",
			wantGroup: 1,
		},
		{
			name:      "ORDER BY double space",
			input:     "SELECT a FROM t ORDER  BY x",
			single:    "SELECT a FROM t ORDER BY x",
			wantOrder: 1,
		},
		{
			name:      "ORDER BY tab separated",
			input:     "SELECT a FROM t ORDER\tBY x",
			single:    "SELECT a FROM t ORDER BY x",
			wantOrder: 1,
		},
		{
			name:       "HAVING double space after GROUP BY",
			input:      "SELECT a FROM t GROUP BY x HAVING  count(a) > 1",
			single:     "SELECT a FROM t GROUP BY x HAVING count(a) > 1",
			wantGroup:  1,
			wantHaving: true,
		},
		{
			name:      "GROUP BY and ORDER BY both with extra whitespace",
			input:     "SELECT a FROM t GROUP  BY x ORDER  BY y",
			single:    "SELECT a FROM t GROUP BY x ORDER BY y",
			wantGroup: 1,
			wantOrder: 1,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseCatalogSelect(test.input)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got.GroupBy) != test.wantGroup {
				t.Fatalf("GroupBy got %d, want %d", len(got.GroupBy), test.wantGroup)
			}
			if len(got.OrderBy) != test.wantOrder {
				t.Fatalf("OrderBy got %d, want %d", len(got.OrderBy), test.wantOrder)
			}
			if test.wantHaving && got.Having == nil {
				t.Fatalf("expected HAVING clause, got nil")
			}
			// The whitespace-tolerant form must produce the same query as the
			// canonical single-space form.
			ref, err := parseCatalogSelect(test.single)
			if err != nil {
				t.Fatalf("canonical form errored: %v", err)
			}
			if !reflect.DeepEqual(got, ref) {
				t.Fatalf("whitespace form differs from single-space form:\ngot  %+v\nwant %+v", got, ref)
			}
		})
	}
}

// TestParseCatalogSelectKeywordWhitespaceDoesNotReachStringLiterals verifies
// that whitespace inside a multi-word keyword found inside a string literal is
// not mistaken for a clause keyword: the parser already tracks single-quote
// state, so 'GROUP  BY' embedded in a WHERE value stays part of the string.
func TestParseCatalogSelectKeywordWhitespaceDoesNotReachStringLiterals(t *testing.T) {
	query, err := parseCatalogSelect(`SELECT a FROM t WHERE x = 'GROUP  BY foo' ORDER BY a`)
	if err != nil {
		t.Fatal(err)
	}
	if query.Where == nil {
		t.Fatal("expected WHERE clause")
	}
	if len(query.OrderBy) != 1 {
		t.Fatalf("expected one ORDER BY item, got %d", len(query.OrderBy))
	}
	if query.Where.Kind != "eq" || len(query.Where.Args) != 2 {
		t.Fatalf("expected eq expression, got %+v", query.Where)
	}
	right := query.Where.Args[1]
	if right.Kind != "string" || right.Value != "GROUP  BY foo" {
		t.Fatalf("expected string literal preserved verbatim, got %+v", right)
	}
}

// TestParseCatalogSelectJoinInternalWhitespace covers the JOIN keyword path of
// the MEDIA-2 fix: "LEFT OUTER JOIN" and "INNER JOIN" with extra whitespace
// between words must parse identically to the single-space form.
func TestParseCatalogSelectJoinInternalWhitespace(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"LEFT JOIN single space", "SELECT a FROM t LEFT JOIN s ON s.id = t.id"},
		{"LEFT JOIN double space", "SELECT a FROM t LEFT  JOIN s ON s.id = t.id"},
		{"LEFT OUTER JOIN single space", "SELECT a FROM t LEFT OUTER JOIN s ON s.id = t.id"},
		{"LEFT OUTER JOIN double space", "SELECT a FROM t LEFT  OUTER  JOIN s ON s.id = t.id"},
		{"LEFT OUTER JOIN tab", "SELECT a FROM t LEFT\tOUTER\tJOIN s ON s.id = t.id"},
		{"INNER JOIN double space", "SELECT a FROM t INNER  JOIN s ON s.id = t.id"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseCatalogSelect(test.input)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got.Joins) != 1 {
				t.Fatalf("expected one JOIN, got %d", len(got.Joins))
			}
			if got.Joins[0].Table.Table != "s" {
				t.Fatalf("expected joined table s, got %q", got.Joins[0].Table.Table)
			}
			if got.Joins[0].On.Kind != "eq" {
				t.Fatalf("expected ON eq condition, got %+v", got.Joins[0].On)
			}
		})
	}
}
