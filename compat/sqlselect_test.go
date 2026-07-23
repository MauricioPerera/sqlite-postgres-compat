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

// TestParseCatalogSelectHeaderToleratesAsSelectWhitespace covers the AUDIT7-A
// BAJA H3 fix (same class as MEDIA H1): the view header boundary "AS SELECT"
// must tolerate any run of whitespace between AS and SELECT, exactly as the
// clause keywords tolerate it via keywordMatchSpan. SQLite and Postgres accept
// "AS  SELECT" and "AS\tSELECT"; the parser previously required a single space.
func TestParseCatalogSelectHeaderToleratesAsSelectWhitespace(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"AS SELECT single space", "CREATE VIEW v AS SELECT a FROM t"},
		{"AS SELECT double space", "CREATE VIEW v AS  SELECT a FROM t"},
		{"AS SELECT tab separator", "CREATE VIEW v AS\tSELECT a FROM t"},
		{"AS SELECT newline separator", "CREATE VIEW v AS\nSELECT a FROM t"},
		{"AS SELECT with trailing clauses", "CREATE VIEW v AS  SELECT a FROM t WHERE a IS  NOT  NULL"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseCatalogSelect(test.input)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.From.Table != "t" {
				t.Fatalf("expected FROM t, got %+v", got.From)
			}
			if len(got.Columns) != 1 {
				t.Fatalf("expected one projection, got %d", len(got.Columns))
			}
			// The whitespace-tolerant form must produce the same query as the
			// canonical single-space form.
			single := strings.ReplaceAll(test.input, "AS  SELECT", "AS SELECT")
			single = strings.ReplaceAll(single, "AS\tSELECT", "AS SELECT")
			single = strings.ReplaceAll(single, "AS\nSELECT", "AS SELECT")
			single = strings.ReplaceAll(single, "IS  NOT  NULL", "IS NOT NULL")
			ref, err := parseCatalogSelect(single)
			if err != nil {
				t.Fatalf("canonical form errored: %v", err)
			}
			if !reflect.DeepEqual(got, ref) {
				t.Fatalf("whitespace form differs from single-space form:\ngot  %+v\nwant %+v", got, ref)
			}
		})
	}
}

// TestParseCatalogSelectRejectsMissingAsSelect confirms a CREATE header
// without an AS SELECT boundary is still rejected (the flexible matcher did
// not loosen the required boundary).
func TestParseCatalogSelectRejectsMissingAsSelect(t *testing.T) {
	if _, err := parseCatalogSelect("CREATE VIEW v SELECT a FROM t"); err == nil {
		t.Fatal("expected error for missing AS SELECT, got nil")
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

// TestParseCatalogSelectCompoundOperators freezes the AST produced for each of
// the four set operations and for a homogeneous chain. UNION, UNION ALL,
// INTERSECT and EXCEPT have identical set semantics in SQLite and PostgreSQL, so
// each parses into a leading query plus a Compounds chain carrying the operator.
func TestParseCatalogSelectCompoundOperators(t *testing.T) {
	tests := []struct {
		name          string
		input         string
		wantOperators []string
	}{
		{"union", "SELECT a FROM t UNION SELECT a FROM s", []string{"union"}},
		{"union all", "SELECT a FROM t UNION ALL SELECT a FROM s", []string{"union_all"}},
		{"intersect", "SELECT a FROM t INTERSECT SELECT a FROM s", []string{"intersect"}},
		{"except", "SELECT a FROM t EXCEPT SELECT a FROM s", []string{"except"}},
		{"union chain", "SELECT a FROM t UNION SELECT a FROM s UNION SELECT a FROM u", []string{"union", "union"}},
		{"mixed union all and except", "SELECT a FROM t UNION ALL SELECT a FROM s EXCEPT SELECT a FROM u", []string{"union_all", "except"}},
		{"all intersect chain", "SELECT a FROM t INTERSECT SELECT a FROM s INTERSECT SELECT a FROM u", []string{"intersect", "intersect"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseCatalogSelect(test.input)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			// The leading query keeps its own projections and FROM.
			if got.From.Table != "t" || len(got.Columns) != 1 {
				t.Fatalf("unexpected leading query: %+v", got)
			}
			if len(got.Compounds) != len(test.wantOperators) {
				t.Fatalf("expected %d compounds, got %d: %+v", len(test.wantOperators), len(got.Compounds), got.Compounds)
			}
			for i, wantOp := range test.wantOperators {
				branch := got.Compounds[i]
				if branch.Operator != wantOp {
					t.Fatalf("compound %d operator: got %q want %q", i, branch.Operator, wantOp)
				}
				if len(branch.Query.Columns) != 1 || branch.Query.From.Table == "" {
					t.Fatalf("compound %d branch is not a full SELECT: %+v", i, branch.Query)
				}
				// A branch never carries the whole-compound trailing clauses or a
				// nested compound.
				if branchHasTrailingClauses(branch.Query) || len(branch.Query.Compounds) != 0 {
					t.Fatalf("compound %d branch carries trailing/nested state: %+v", i, branch.Query)
				}
			}
		})
	}
}

// TestParseCatalogSelectCompoundTrailingClauseAppliesToWholeCompound freezes the
// semantics that a trailing ORDER BY / LIMIT / OFFSET after the last branch
// applies to the whole compound: it is hoisted onto the leading query and no
// branch retains it.
func TestParseCatalogSelectCompoundTrailingClauseAppliesToWholeCompound(t *testing.T) {
	got, err := parseCatalogSelect("SELECT a FROM t UNION SELECT a FROM s ORDER BY a DESC LIMIT 10 OFFSET 5")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Compounds) != 1 || got.Compounds[0].Operator != "union" {
		t.Fatalf("expected one union compound, got %+v", got.Compounds)
	}
	if len(got.OrderBy) != 1 || !got.OrderBy[0].Descending || got.Limit == nil || *got.Limit != 10 || got.Offset == nil || *got.Offset != 5 {
		t.Fatalf("trailing clauses were not hoisted onto the leading query: %+v", got)
	}
	if branchHasTrailingClauses(got.Compounds[0].Query) {
		t.Fatalf("branch must not keep the trailing clauses: %+v", got.Compounds[0].Query)
	}
}

// TestParseCatalogSelectRejectsMixedIntersect freezes the honest rejection: a
// chain mixing INTERSECT with UNION or EXCEPT is refused, because INTERSECT
// binds more tightly than UNION/EXCEPT in PostgreSQL but has equal precedence in
// SQLite, so a flat left-associative chain would group differently per engine.
func TestParseCatalogSelectRejectsMixedIntersect(t *testing.T) {
	cases := []string{
		"SELECT a FROM t UNION SELECT a FROM s INTERSECT SELECT a FROM u",
		"SELECT a FROM t INTERSECT SELECT a FROM s UNION SELECT a FROM u",
		"SELECT a FROM t INTERSECT SELECT a FROM s EXCEPT SELECT a FROM u",
		"SELECT a FROM t EXCEPT SELECT a FROM s INTERSECT SELECT a FROM u",
	}
	for _, input := range cases {
		t.Run(input, func(t *testing.T) {
			_, err := parseCatalogSelect(input)
			if err == nil {
				t.Fatalf("expected rejection for mixed INTERSECT, got nil")
			}
			if !strings.Contains(err.Error(), "INTERSECT") {
				t.Fatalf("expected an INTERSECT precedence error, got %q", err.Error())
			}
		})
	}
}

// TestParseCatalogSelectRejectsParenthesizedCompoundBranch confirms a
// parenthesized branch (the shape PostgreSQL emits around a mixed-precedence
// compound) is rejected rather than silently mis-parsed: the set operator it
// contains stays inside the parentheses, so the branch does not begin with
// SELECT and the single-select parser refuses it.
func TestParseCatalogSelectRejectsParenthesizedCompoundBranch(t *testing.T) {
	cases := []string{
		"SELECT a FROM t UNION (SELECT a FROM s INTERSECT SELECT a FROM u)",
		"(SELECT a FROM t UNION SELECT a FROM s) EXCEPT SELECT a FROM u",
	}
	for _, input := range cases {
		t.Run(input, func(t *testing.T) {
			if _, err := parseCatalogSelect(input); err == nil {
				t.Fatalf("expected rejection for parenthesized compound branch, got nil")
			}
		})
	}
}

// TestParseCatalogSelectCompoundBranchInternalWhitespace confirms the compound
// split tolerates any run of whitespace inside a multi-word operator, exactly as
// the clause and JOIN keywords do, and that a string literal containing the
// word UNION is not mistaken for a set operator.
func TestParseCatalogSelectCompoundBranchInternalWhitespace(t *testing.T) {
	got, err := parseCatalogSelect("SELECT a FROM t UNION  ALL SELECT a FROM s")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Compounds) != 1 || got.Compounds[0].Operator != "union_all" {
		t.Fatalf("expected a union_all compound, got %+v", got.Compounds)
	}

	quoted, err := parseCatalogSelect("SELECT a FROM t WHERE a = 'x UNION y'")
	if err != nil {
		t.Fatal(err)
	}
	if len(quoted.Compounds) != 0 {
		t.Fatalf("UNION inside a string literal must not split a compound: %+v", quoted.Compounds)
	}
}

// TestParseCatalogSelectFromSubquery freezes the AST for a derived table
// (FROM-subquery): the leading query keeps its projections while its From holds a
// nested SelectQuery (the derived table) plus the required alias. The derived
// table cannot be correlated with the enclosing query in standard SQL, so both
// engines materialize it identically.
func TestParseCatalogSelectFromSubquery(t *testing.T) {
	got, err := parseCatalogSelect(`SELECT s.id, s.name FROM (SELECT id, name FROM products WHERE active = TRUE) AS s`)
	if err != nil {
		t.Fatal(err)
	}
	if got.From.Table != "" || got.From.Subquery == nil || got.From.Alias != "s" {
		t.Fatalf("expected a derived table aliased s, got %+v", got.From)
	}
	sub := got.From.Subquery
	if sub.From.Table != "products" || len(sub.Columns) != 2 || sub.Where == nil {
		t.Fatalf("derived table inner query not parsed: %+v", sub)
	}
	if len(got.Columns) != 2 || got.Columns[0].Expression.Value != "s.id" {
		t.Fatalf("outer projections not parsed: %+v", got.Columns)
	}
}

// TestParseCatalogSelectFromSubqueryWithoutAlias rejects a derived table with no
// alias: PostgreSQL requires one, so demanding it keeps the canonical form exact
// for both engines rather than compiling something SQLite accepts but PG refuses.
func TestParseCatalogSelectFromSubqueryWithoutAlias(t *testing.T) {
	if _, err := parseCatalogSelect(`SELECT id FROM (SELECT id FROM products)`); err == nil {
		t.Fatal("expected rejection for an unaliased derived table, got nil")
	}
}

// TestParseCatalogSelectFromSubqueryAcceptsPostgresDeparse round-trips the exact
// normalized definition PostgreSQL 17 emits for a FROM-subquery view (captured
// from information_schema.views): qualified columns, extra parentheses and
// newlines. It must parse (so the view stays resolved / Exact on inspection).
func TestParseCatalogSelectFromSubqueryAcceptsPostgresDeparse(t *testing.T) {
	deparse := " SELECT id,\n    name\n   FROM ( SELECT pt.id,\n            pt.name\n           FROM pt\n          WHERE (pt.active = true)) s;"
	got, err := parseCatalogSelect(deparse)
	if err != nil {
		t.Fatalf("PostgreSQL FROM-subquery deparse did not round-trip: %v", err)
	}
	if got.From.Subquery == nil || got.From.Alias != "s" || got.From.Subquery.Where == nil {
		t.Fatalf("unexpected round-tripped derived table: %+v", got.From)
	}
}

// TestParseCatalogSelectSingleCTE freezes the AST for a single non-recursive CTE:
// the WITH list carries the named subquery and the main query references it by
// name in FROM like an ordinary table.
func TestParseCatalogSelectSingleCTE(t *testing.T) {
	got, err := parseCatalogSelect(`WITH a AS (SELECT id, name FROM products) SELECT id, name FROM a`)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.With) != 1 || got.With[0].Name != "a" {
		t.Fatalf("expected one CTE named a, got %+v", got.With)
	}
	if got.With[0].Query.From.Table != "products" || len(got.With[0].Query.Columns) != 2 {
		t.Fatalf("CTE inner query not parsed: %+v", got.With[0].Query)
	}
	if got.From.Table != "a" || len(got.Columns) != 2 {
		t.Fatalf("main query not parsed: %+v", got)
	}
}

// TestParseCatalogSelectMultipleCTEs freezes a two-CTE chain feeding a joined
// main query, and confirms both CTE bodies are parsed independently.
func TestParseCatalogSelectMultipleCTEs(t *testing.T) {
	got, err := parseCatalogSelect(`WITH a AS (SELECT id, name FROM products), b AS (SELECT id FROM sales) SELECT a.id, a.name FROM a INNER JOIN b ON b.id = a.id`)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.With) != 2 || got.With[0].Name != "a" || got.With[1].Name != "b" {
		t.Fatalf("expected CTEs a and b, got %+v", got.With)
	}
	if got.From.Table != "a" || len(got.Joins) != 1 || got.Joins[0].Table.Table != "b" {
		t.Fatalf("main query join not parsed: %+v", got)
	}
}

// TestParseCatalogSelectCTEAcceptsPostgresDeparse round-trips the exact
// normalized definition PostgreSQL 17 emits for a CTE view (WITH preserved,
// reindented with newlines), so a native CTE view stays resolved on inspection.
func TestParseCatalogSelectCTEAcceptsPostgresDeparse(t *testing.T) {
	deparse := " WITH a AS (\n         SELECT pt.id,\n            pt.name\n           FROM pt\n        )\n SELECT id,\n    name\n   FROM a;"
	got, err := parseCatalogSelect(deparse)
	if err != nil {
		t.Fatalf("PostgreSQL CTE deparse did not round-trip: %v", err)
	}
	if len(got.With) != 1 || got.With[0].Name != "a" || got.From.Table != "a" {
		t.Fatalf("unexpected round-tripped CTE view: %+v", got)
	}
}

// TestParseCatalogSelectRejectsRecursiveCTE freezes the honest rejection of WITH
// RECURSIVE: recursive termination and observable ordering are not guaranteed
// identical between the engines, so such a view becomes unresolved, never
// divergent SQL.
func TestParseCatalogSelectRejectsRecursiveCTE(t *testing.T) {
	_, err := parseCatalogSelect(`WITH RECURSIVE a AS (SELECT id FROM products) SELECT id FROM a`)
	if err == nil {
		t.Fatal("expected rejection for WITH RECURSIVE, got nil")
	}
	if !strings.Contains(err.Error(), "RECURSIVE") {
		t.Fatalf("expected a RECURSIVE rejection, got %q", err.Error())
	}
}

// TestParseCatalogSelectCTEInCreateView confirms the CREATE VIEW header boundary
// is found when the body begins with WITH (the view's AS is followed by WITH, not
// SELECT), matching how SQLite stores a CTE view verbatim.
func TestParseCatalogSelectCTEInCreateView(t *testing.T) {
	got, err := parseCatalogSelect(`CREATE VIEW v AS WITH a AS (SELECT id FROM products) SELECT id FROM a`)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.With) != 1 || got.From.Table != "a" {
		t.Fatalf("CREATE VIEW with CTE not parsed: %+v", got)
	}
}

// TestParseCatalogSelectCTEOverCompound confirms a CTE can precede a compound
// main query: the WITH list attaches to the leading query and the set-operation
// chain is parsed as usual.
func TestParseCatalogSelectCTEOverCompound(t *testing.T) {
	got, err := parseCatalogSelect(`WITH a AS (SELECT id FROM products) SELECT id FROM a UNION SELECT id FROM sales`)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.With) != 1 || len(got.Compounds) != 1 || got.Compounds[0].Operator != "union" {
		t.Fatalf("expected a CTE over a union compound, got %+v", got)
	}
}

// TestParseCatalogSelectRejectsSelfReferentialCTE freezes AUDIT10 M2: a
// non-recursive CTE that names itself in its own body (with a real base table of
// the same name) parses and compiles byte-identically but diverges at runtime —
// SQLite errors "circular reference", PostgreSQL binds the inner name to the base
// table and returns rows. The parser rejects it by the self-reference property,
// closing the gap the RECURSIVE-keyword guard cannot see.
func TestParseCatalogSelectRejectsSelfReferentialCTE(t *testing.T) {
	_, err := parseCatalogSelect(`WITH t AS (SELECT id FROM t) SELECT id FROM t`)
	if err == nil {
		t.Fatal("expected rejection for self-referential CTE, got nil")
	}
	if !strings.Contains(err.Error(), "self-referential") || !strings.Contains(err.Error(), "RECURSIVE") {
		t.Fatalf("expected a self-referential CTE rejection, got %q", err.Error())
	}
}

// TestParseCatalogSelectRejectsSelfReferentialCTEInJoin confirms the self-
// reference is caught whether the CTE names itself in FROM or in a JOIN.
func TestParseCatalogSelectRejectsSelfReferentialCTEInJoin(t *testing.T) {
	_, err := parseCatalogSelect(`WITH t AS (SELECT s.id FROM s INNER JOIN t ON t.id = s.id) SELECT id FROM t`)
	if err == nil {
		t.Fatal("expected rejection for self-referential CTE in JOIN, got nil")
	}
	if !strings.Contains(err.Error(), "self-referential") {
		t.Fatalf("expected a self-referential CTE rejection, got %q", err.Error())
	}
}

// TestParseCatalogSelectAcceptsSiblingCTEReference is the M2 non-regression
// guard: a second CTE referencing the *first* (a legitimate, non-recursive
// pattern) must still parse. Only a CTE referencing its own name is rejected.
func TestParseCatalogSelectAcceptsSiblingCTEReference(t *testing.T) {
	got, err := parseCatalogSelect(`WITH a AS (SELECT id FROM products), b AS (SELECT id FROM a) SELECT id FROM b`)
	if err != nil {
		t.Fatalf("sibling CTE reference must still parse: %v", err)
	}
	if len(got.With) != 2 || got.With[0].Name != "a" || got.With[1].Name != "b" {
		t.Fatalf("expected CTEs a and b, got %+v", got.With)
	}
	if got.With[1].Query.From.Table != "a" {
		t.Fatalf("second CTE should read from the first: %+v", got.With[1].Query)
	}
}

// TestParseCatalogSelectRejectsCaseFoldSelfReferentialCTE freezes AUDIT11 A11-1:
// a non-recursive CTE named `T` whose body reads from `t` (unquoted identifiers
// differing only in ASCII case). Both engines fold unquoted identifiers, so `T`
// and `t` denote the same name and the CTE self-references — SQLite errors
// "circular reference" while PostgreSQL binds the inner `t` to a real base table
// of the same folded name and returns rows, from byte-identical DDL. The exact-
// case form was closed by M2; this closes the case-fold flank.
func TestParseCatalogSelectRejectsCaseFoldSelfReferentialCTE(t *testing.T) {
	_, err := parseCatalogSelect(`WITH T AS (SELECT id FROM t) SELECT id FROM T`)
	if err == nil {
		t.Fatal("expected rejection for case-fold self-referential CTE, got nil")
	}
	if !strings.Contains(err.Error(), "self-referential") || !strings.Contains(err.Error(), "RECURSIVE") {
		t.Fatalf("expected a self-referential CTE rejection, got %q", err.Error())
	}
}

// TestParseCatalogSelectAcceptsDistinctNamedCTE is the A11-1 non-regression guard:
// a WITH whose CTE and base table have genuinely different names (not merely a
// case difference) must still parse — the case-insensitive self-reference check
// must not turn into a false positive against unrelated names.
func TestParseCatalogSelectAcceptsDistinctNamedCTE(t *testing.T) {
	got, err := parseCatalogSelect(`WITH summary AS (SELECT id FROM orders) SELECT id FROM summary`)
	if err != nil {
		t.Fatalf("distinct-named CTE must still parse: %v", err)
	}
	if len(got.With) != 1 || got.With[0].Name != "summary" {
		t.Fatalf("expected CTE summary, got %+v", got.With)
	}
	if got.With[0].Query.From.Table != "orders" {
		t.Fatalf("CTE should read from the base table orders: %+v", got.With[0].Query)
	}
}
