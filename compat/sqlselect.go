package compat

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
)

// catalogClause pairs a trailing SELECT keyword with the routine that applies
// its text to the query being built.
type catalogClause struct {
	keyword string
	apply   func(string) error
}

// locatedClause records where a trailing keyword was found and which clause it
// belongs to, so clauses can be applied in source order regardless of the
// order they were declared in.
type locatedClause struct {
	position int
	index    int
}

// parseCatalogSelect parses the shared, deliberately bounded SELECT grammar
// used for external views. Unsupported clauses are rejected explicitly. A view
// body may be a single SELECT or a left-associative chain of set operations
// (UNION [ALL] / INTERSECT / EXCEPT); the trailing ORDER BY / LIMIT / OFFSET, if
// present, apply to the whole compound and are hoisted onto the leading query.
func parseCatalogSelect(definition string) (SelectQuery, error) {
	selectText, err := stripCatalogViewPrefix(definition)
	if err != nil {
		return SelectQuery{}, err
	}
	with, bodyText, err := stripCatalogWith(selectText)
	if err != nil {
		return SelectQuery{}, err
	}
	head, err := parseCatalogSelectBody(bodyText)
	if err != nil {
		return SelectQuery{}, err
	}
	head.With = with
	return head, nil
}

// parseCatalogSelectBody parses the SELECT-or-compound portion of a query, after
// any leading WITH clause has been stripped by the caller. It handles a single
// SELECT or a left-associative chain of set operations (UNION [ALL] / INTERSECT /
// EXCEPT); the trailing ORDER BY / LIMIT / OFFSET, if present, apply to the whole
// compound and are hoisted onto the leading query.
func parseCatalogSelectBody(selectText string) (SelectQuery, error) {
	segments, operators := splitCompoundSelect(selectText)
	if len(operators) == 0 {
		return parseSingleCatalogSelect(selectText)
	}
	if err := rejectMixedIntersectChain(operators); err != nil {
		return SelectQuery{}, err
	}
	head, err := parseSingleCatalogSelect(strings.TrimSpace(segments[0]))
	if err != nil {
		return SelectQuery{}, err
	}
	// Only the final branch may carry ORDER BY / LIMIT / OFFSET; both engines
	// reject those clauses on a non-final branch of an unparenthesized compound.
	if branchHasTrailingClauses(head) {
		return SelectQuery{}, fmt.Errorf("ORDER BY/LIMIT/OFFSET must follow the last SELECT of a compound")
	}
	compounds := make([]CompoundSelect, 0, len(operators))
	for i, operator := range operators {
		branch, err := parseSingleCatalogSelect(strings.TrimSpace(segments[i+1]))
		if err != nil {
			return SelectQuery{}, err
		}
		if i < len(operators)-1 && branchHasTrailingClauses(branch) {
			return SelectQuery{}, fmt.Errorf("ORDER BY/LIMIT/OFFSET must follow the last SELECT of a compound")
		}
		compounds = append(compounds, CompoundSelect{Operator: operator, Query: branch})
	}
	// Hoist the whole-compound trailing clauses from the last branch onto the
	// leading query, where the model keeps them.
	last := &compounds[len(compounds)-1].Query
	head.OrderBy, last.OrderBy = last.OrderBy, nil
	head.Limit, last.Limit = last.Limit, nil
	head.Offset, last.Offset = last.Offset, nil
	head.Compounds = compounds
	return head, nil
}

// branchHasTrailingClauses reports whether a parsed branch carries any
// whole-compound trailing clause (ORDER BY / LIMIT / OFFSET).
func branchHasTrailingClauses(query SelectQuery) bool {
	return len(query.OrderBy) > 0 || query.Limit != nil || query.Offset != nil
}

// rejectMixedIntersectChain rejects a compound chain that mixes INTERSECT with
// any other set operator. UNION, UNION ALL and EXCEPT share precedence and are
// left-associative in both SQLite and PostgreSQL, so a flat left-associative
// chain of those compiles identically for either engine. INTERSECT, however,
// binds more tightly than UNION/EXCEPT in PostgreSQL while SQLite gives it equal
// (left-associative) precedence, so `a UNION b INTERSECT c` groups as
// `a UNION (b INTERSECT c)` in PostgreSQL but `(a UNION b) INTERSECT c` in
// SQLite. Rather than emit SQL that would diverge, such a chain is rejected and
// surfaces as an unresolved object (Exact = false). A homogeneous all-INTERSECT
// chain is accepted: it groups left-associatively and identically in both.
func rejectMixedIntersectChain(operators []string) error {
	hasIntersect, hasOther := false, false
	for _, operator := range operators {
		if operator == "intersect" {
			hasIntersect = true
		} else {
			hasOther = true
		}
	}
	if hasIntersect && hasOther {
		return fmt.Errorf("compound mixing INTERSECT with UNION/EXCEPT is outside the canonical grammar: INTERSECT precedence differs between SQLite and PostgreSQL")
	}
	return nil
}

// compoundOperators lists the recognized set operators in match-priority order;
// "UNION ALL" must be tested before "UNION" so the longer keyword wins.
var compoundOperators = []struct {
	keyword  string
	operator string
}{
	{"UNION ALL", "union_all"},
	{"UNION", "union"},
	{"INTERSECT", "intersect"},
	{"EXCEPT", "except"},
}

// splitCompoundSelect scans a view's SELECT text and splits it at every
// top-level set operator (outside parentheses and string/identifier quotes),
// returning the branch texts and the operator joining each branch to the one
// before it (len(operators) == len(segments)-1). Text with no top-level set
// operator yields a single segment and no operators, so the caller treats it as
// a plain SELECT with byte-identical behavior. A parenthesized branch (e.g. the
// `(...)` PostgreSQL emits around a mixed-precedence compound) leaves any set
// operator it contains at depth > 0, so it is not split here and the branch,
// which does not begin with SELECT, is rejected by the single-select parser.
func splitCompoundSelect(text string) (segments []string, operators []string) {
	depth := 0
	inSingle, inDouble := false, false
	start := 0
	for i := 0; i < len(text); i++ {
		switch text[i] {
		case '\'':
			if !inDouble {
				if inSingle && i+1 < len(text) && text[i+1] == '\'' {
					i++
					continue
				}
				inSingle = !inSingle
			}
		case '"':
			if !inSingle {
				inDouble = !inDouble
			}
		case '(':
			if !inSingle && !inDouble {
				depth++
			}
		case ')':
			if !inSingle && !inDouble {
				depth--
			}
		}
		if depth != 0 || inSingle || inDouble || !wordBoundary(text, i-1) {
			continue
		}
		for _, candidate := range compoundOperators {
			if end, ok := keywordMatchSpan(text, i, candidate.keyword); ok && wordBoundary(text, end) {
				segments = append(segments, text[start:i])
				operators = append(operators, candidate.operator)
				i = end - 1 // the loop's i++ resumes scanning just past the keyword
				start = end
				break
			}
		}
	}
	segments = append(segments, text[start:])
	return segments, operators
}

// parseSingleCatalogSelect parses one branch of the grammar: a single SELECT
// with its projections, FROM, JOINs and optional WHERE / GROUP BY / HAVING /
// ORDER BY / LIMIT / OFFSET. It expects text that begins with the SELECT
// keyword (compound splitting and the CREATE VIEW prefix are handled by the
// caller).
func parseSingleCatalogSelect(selectText string) (SelectQuery, error) {
	if !strings.HasPrefix(strings.ToUpper(selectText), "SELECT ") {
		return SelectQuery{}, fmt.Errorf("view definition is not SELECT")
	}
	body, distinct := stripCatalogSelectKeyword(selectText)
	fromPosition := topLevelKeyword(body, "FROM")
	if fromPosition < 0 {
		return SelectQuery{}, fmt.Errorf("SELECT has no FROM")
	}
	projectionText := strings.TrimSpace(body[:fromPosition])
	remainder := strings.TrimSpace(body[fromPosition+len("FROM"):])

	query := SelectQuery{Distinct: distinct}
	clauses := catalogSelectClauses(&query)
	located := locateCatalogClauses(remainder, clauses)

	sourceEnd := len(remainder)
	if len(located) > 0 {
		sourceEnd = located[0].position
	}
	source, joins, err := parseCatalogFrom(strings.TrimSpace(remainder[:sourceEnd]))
	if err != nil {
		return SelectQuery{}, err
	}
	query.From = source
	query.Joins = joins

	if err := applyCatalogClauses(&query, remainder, clauses, located); err != nil {
		return SelectQuery{}, err
	}
	if err := parseCatalogProjections(&query, projectionText); err != nil {
		return SelectQuery{}, err
	}
	if len(query.Columns) == 0 {
		return SelectQuery{}, fmt.Errorf("SELECT has no projections")
	}
	return query, nil
}

// stripCatalogViewPrefix trims the definition, drops a trailing ";", and strips
// an optional "CREATE VIEW ... AS" prefix, returning the remaining text, which
// must begin with SELECT. The SELECT keyword itself is left in place so the
// result can be split into compound branches, each of which begins with its own
// SELECT.
func stripCatalogViewPrefix(definition string) (string, error) {
	text := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(definition), ";"))
	if strings.HasPrefix(strings.ToUpper(text), "CREATE ") {
		// Find the view's "AS <body>" boundary with the same whitespace tolerance
		// the rest of the parser uses for multi-word keywords (keywordMatchSpan),
		// so "AS  SELECT"/"AS\tSELECT" is accepted just like "AS SELECT". The body
		// begins with either SELECT (a plain query) or WITH (a query preceded by
		// common table expressions); a CTE's own inner "AS (" never matches either
		// "AS SELECT" or "AS WITH", so the earliest of the two top-level matches is
		// the real boundary. Both SQLite and Postgres treat any run of whitespace
		// as a separator here.
		position, keyword := earliestKeyword(text, "AS SELECT", "AS WITH")
		if position < 0 {
			return "", fmt.Errorf("view definition has no AS SELECT")
		}
		end, _ := keywordMatchSpan(text, position, keyword)
		bodyKeyword := keyword[len("AS "):] // "SELECT" or "WITH"
		text = strings.TrimSpace(text[end-len(bodyKeyword):])
	}
	if !catalogBodyStartsAQuery(text) {
		return "", fmt.Errorf("view definition is not SELECT")
	}
	return text, nil
}

// earliestKeyword returns the position and text of whichever keyword occurs
// first at the top level of text, or (-1, "") if none is present.
func earliestKeyword(text string, keywords ...string) (int, string) {
	best := -1
	bestKeyword := ""
	for _, keyword := range keywords {
		if position := topLevelKeyword(text, keyword); position >= 0 && (best < 0 || position < best) {
			best = position
			bestKeyword = keyword
		}
	}
	return best, bestKeyword
}

// catalogBodyStartsAQuery reports whether text begins with a query keyword the
// grammar can parse: SELECT (a plain query) or WITH (CTEs then a query).
func catalogBodyStartsAQuery(text string) bool {
	upper := strings.ToUpper(text)
	return strings.HasPrefix(upper, "SELECT ") || catalogKeywordPrefix(text, "WITH")
}

// catalogKeywordPrefix reports whether text begins with keyword at a word
// boundary (case-insensitive, whitespace-tolerant inside a multi-word keyword).
func catalogKeywordPrefix(text, keyword string) bool {
	end, ok := keywordMatchSpan(text, 0, keyword)
	return ok && wordBoundary(text, end)
}

// stripCatalogWith parses and removes a leading, non-recursive WITH clause,
// returning the parsed common table expressions and the remaining query text. If
// text does not begin with WITH, it returns (nil, text, nil). WITH RECURSIVE is
// rejected explicitly: its termination and observable row ordering are hard to
// guarantee byte-identical between SQLite and PostgreSQL, so such a view becomes
// an unresolved object rather than divergent SQL. Each CTE is `name AS ( query )`;
// entries are separated by top-level commas and the main query begins after the
// last CTE's closing parenthesis.
func stripCatalogWith(text string) ([]CommonTableExpr, string, error) {
	text = strings.TrimSpace(text)
	if !catalogKeywordPrefix(text, "WITH") {
		return nil, text, nil
	}
	end, _ := keywordMatchSpan(text, 0, "WITH")
	rest := strings.TrimSpace(text[end:])
	if catalogKeywordPrefix(rest, "RECURSIVE") {
		return nil, "", fmt.Errorf("WITH RECURSIVE is outside the canonical grammar: recursive CTE termination and ordering are not guaranteed identical between SQLite and PostgreSQL")
	}
	var ctes []CommonTableExpr
	for {
		asPosition := topLevelKeyword(rest, "AS")
		if asPosition < 0 {
			return nil, "", fmt.Errorf("common table expression has no AS")
		}
		name, ok := parseCatalogIdentifier(strings.TrimSpace(rest[:asPosition]))
		if !ok || strings.Contains(name, ".") {
			return nil, "", fmt.Errorf("unsupported common table expression name %q", strings.TrimSpace(rest[:asPosition]))
		}
		asEnd, _ := keywordMatchSpan(rest, asPosition, "AS")
		body := strings.TrimSpace(rest[asEnd:])
		if len(body) == 0 || body[0] != '(' {
			return nil, "", fmt.Errorf("common table expression %q must be parenthesized", name)
		}
		close := matchingParenthesis(body, 0)
		if close < 0 {
			return nil, "", fmt.Errorf("common table expression %q has unbalanced parentheses", name)
		}
		query, err := parseCatalogSelect(strings.TrimSpace(body[1:close]))
		if err != nil {
			return nil, "", err
		}
		if cteQueryReferencesName(query, name) {
			// A non-recursive CTE that names itself in its own body is not modeled.
			// The RECURSIVE guard above catches the keyword; this catches the
			// property. Without RECURSIVE the two engines diverge at runtime on the
			// same DDL: SQLite errors ("circular reference"), PostgreSQL binds the
			// inner name to a real base table of the same name and returns rows.
			// Rejecting it here keeps that divergent view from ever compiling.
			return nil, "", fmt.Errorf("self-referential CTE %q requires RECURSIVE, which is outside the canonical grammar", name)
		}
		ctes = append(ctes, CommonTableExpr{Name: name, Query: query})
		tail := strings.TrimSpace(body[close+1:])
		if strings.HasPrefix(tail, ",") {
			rest = strings.TrimSpace(tail[1:])
			continue
		}
		return ctes, tail, nil
	}
}

// stripCatalogSelectKeyword removes the leading SELECT keyword and an optional
// DISTINCT from a single branch's text, returning the remaining body and the
// DISTINCT flag. The caller must have verified the text begins with "SELECT ".
func stripCatalogSelectKeyword(text string) (body string, distinct bool) {
	body = strings.TrimSpace(text[len("SELECT "):])
	if strings.HasPrefix(strings.ToUpper(body), "DISTINCT ") {
		distinct = true
		body = strings.TrimSpace(body[len("DISTINCT "):])
	}
	return body, distinct
}

// catalogSelectClauses returns the ordered set of trailing clauses the grammar
// recognizes, each bound to the query being assembled.
func catalogSelectClauses(query *SelectQuery) []catalogClause {
	return []catalogClause{
		{"WHERE", func(value string) error {
			expression, err := parseCatalogExpression(value)
			query.Where = &expression
			return err
		}},
		{"GROUP BY", func(value string) error {
			for _, item := range splitTopLevelComma(value) {
				expression, err := parseCatalogExpression(item)
				if err != nil {
					return err
				}
				query.GroupBy = append(query.GroupBy, expression)
			}
			return nil
		}},
		{"HAVING", func(value string) error {
			expression, err := parseCatalogExpression(value)
			query.Having = &expression
			return err
		}},
		{"ORDER BY", func(value string) error { return applyOrderByClause(query, value) }},
		{"LIMIT", func(value string) error { return applyLimitClause(query, value) }},
		{"OFFSET", func(value string) error { return applyOffsetClause(query, value) }},
	}
}

// locateCatalogClauses finds the position of each recognized keyword in the
// remainder and returns the matches sorted by position so the source is split
// left to right.
func locateCatalogClauses(remainder string, clauses []catalogClause) []locatedClause {
	var located []locatedClause
	for i, clause := range clauses {
		if position := topLevelKeyword(remainder, clause.keyword); position >= 0 {
			located = append(located, locatedClause{position: position, index: i})
		}
	}
	for i := 0; i < len(located); i++ {
		for j := i + 1; j < len(located); j++ {
			if located[j].position < located[i].position {
				located[i], located[j] = located[j], located[i]
			}
		}
	}
	return located
}

// applyCatalogClauses splits the remainder into per-clause spans (delimited by
// the located keywords, in source order) and applies each clause's handler.
func applyCatalogClauses(query *SelectQuery, remainder string, clauses []catalogClause, located []locatedClause) error {
	for i, current := range located {
		// Compute the actual end of the keyword span rather than start+len,
		// because a multi-word keyword may have matched extra internal
		// whitespace (e.g. "GROUP  BY") that len(keyword) does not count.
		start, _ := keywordMatchSpan(remainder, current.position, clauses[current.index].keyword)
		end := len(remainder)
		if i+1 < len(located) {
			end = located[i+1].position
		}
		value := strings.TrimSpace(remainder[start:end])
		// A clause keyword with no operand (e.g. "GROUP BY" at end of string) is a
		// syntax error in SQLite and Postgres; reject it explicitly rather than
		// accepting an empty clause that the emitter would silently discard.
		if value == "" {
			return fmt.Errorf("SELECT %s clause has no operand", clauses[current.index].keyword)
		}
		if err := clauses[current.index].apply(value); err != nil {
			return err
		}
	}
	return nil
}

// parseCatalogProjections splits the projection list and appends each parsed
// projection (with its alias) to the query.
func parseCatalogProjections(query *SelectQuery, projectionText string) error {
	for _, item := range splitTopLevelComma(projectionText) {
		expressionText, alias := splitProjectionAlias(item)
		expression, err := parseCatalogExpression(expressionText)
		if err != nil {
			return err
		}
		query.Columns = append(query.Columns, Projection{Expression: expression, Alias: alias})
	}
	return nil
}

// applyOrderByClause parses a comma-separated ORDER BY list, stripping the
// optional ASC/DESC suffix from each item.
func applyOrderByClause(query *SelectQuery, value string) error {
	for _, item := range splitTopLevelComma(value) {
		item = strings.TrimSpace(item)
		descending := false
		upperItem := strings.ToUpper(item)
		if strings.HasSuffix(upperItem, " DESC") {
			descending = true
			item = strings.TrimSpace(item[:len(item)-len(" DESC")])
		} else if strings.HasSuffix(upperItem, " ASC") {
			item = strings.TrimSpace(item[:len(item)-len(" ASC")])
		}
		expression, err := parseCatalogExpression(item)
		if err != nil {
			return err
		}
		query.OrderBy = append(query.OrderBy, Ordering{Expression: expression, Descending: descending})
	}
	return nil
}

// applyLimitClause parses a non-negative LIMIT value. SQLite treats a negative
// LIMIT as "no limit", a semantics this compatibility layer cannot preserve;
// reject it explicitly at parse time instead of accepting it silently.
func applyLimitClause(query *SelectQuery, value string) error {
	number, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return err
	}
	if number < 0 {
		return fmt.Errorf("unsupported negative LIMIT %d", number)
	}
	query.Limit = &number
	return nil
}

// applyOffsetClause parses a non-negative OFFSET value. A negative OFFSET has
// no portable meaning here; reject it explicitly at parse time rather than
// accepting it silently.
func applyOffsetClause(query *SelectQuery, value string) error {
	number, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return err
	}
	if number < 0 {
		return fmt.Errorf("unsupported negative OFFSET %d", number)
	}
	query.Offset = &number
	return nil
}

// keywordMatchSpan reports whether text matches keyword at position start,
// allowing any run of whitespace (spaces, tabs, newlines) wherever keyword
// has a single separating space, and returns the index just past the last
// matched character. Matching is case-insensitive. The single spaces inside
// keyword are treated as "any whitespace separator", so "GROUP  BY" or
// "ORDER\tBY" match "GROUP BY" / "ORDER BY"; single-word keywords (FROM, ON,
// AS, HAVING) match verbatim. It does not enforce word boundaries — callers
// do that around the [start, end) span.
func keywordMatchSpan(text string, start int, keyword string) (int, bool) {
	words := strings.Split(keyword, " ")
	i := start
	for w, word := range words {
		if w > 0 {
			if i >= len(text) || !unicode.IsSpace(rune(text[i])) {
				return 0, false
			}
			for i < len(text) && unicode.IsSpace(rune(text[i])) {
				i++
			}
		}
		if i+len(word) > len(text) || !strings.EqualFold(text[i:i+len(word)], word) {
			return 0, false
		}
		i += len(word)
	}
	return i, true
}

func topLevelKeyword(text, keyword string) int {
	depth := 0
	inSingle, inDouble := false, false
	for i := 0; i < len(text); i++ {
		switch text[i] {
		case '\'':
			if !inDouble {
				if inSingle && i+1 < len(text) && text[i+1] == '\'' {
					i++
					continue
				}
				inSingle = !inSingle
			}
		case '"':
			if !inSingle {
				inDouble = !inDouble
			}
		case '(':
			if !inSingle && !inDouble {
				depth++
			}
		case ')':
			if !inSingle && !inDouble {
				depth--
			}
		}
		if depth == 0 && !inSingle && !inDouble && wordBoundary(text, i-1) {
			if end, ok := keywordMatchSpan(text, i, keyword); ok && wordBoundary(text, end) {
				return i
			}
		}
	}
	return -1
}

func splitTopLevelComma(text string) []string {
	var result []string
	start := 0
	depth := 0
	inSingle, inDouble := false, false
	for i := 0; i < len(text); i++ {
		switch text[i] {
		case '\'':
			if !inDouble {
				if inSingle && i+1 < len(text) && text[i+1] == '\'' {
					i++
					continue
				}
				inSingle = !inSingle
			}
		case '"':
			if !inSingle {
				inDouble = !inDouble
			}
		case '(':
			if !inSingle && !inDouble {
				depth++
			}
		case ')':
			if !inSingle && !inDouble {
				depth--
			}
		case ',':
			if depth == 0 && !inSingle && !inDouble {
				result = append(result, strings.TrimSpace(text[start:i]))
				start = i + 1
			}
		}
	}
	if tail := strings.TrimSpace(text[start:]); tail != "" {
		result = append(result, tail)
	}
	return result
}

func splitProjectionAlias(text string) (string, string) {
	if position := topLevelKeyword(text, "AS"); position >= 0 {
		alias, ok := parseCatalogIdentifier(strings.TrimSpace(text[position+len("AS"):]))
		if ok && !strings.Contains(alias, ".") {
			return strings.TrimSpace(text[:position]), alias
		}
	}
	return strings.TrimSpace(text), ""
}

func parseCatalogTableSource(text string) (TableSource, error) {
	text = strings.TrimSpace(text)
	// A derived table: ( <select> ) [AS] alias. The leading parenthesis holds a
	// full subquery (which cannot be correlated with the enclosing query in
	// standard SQL, so both engines materialize it identically); an alias is
	// required, matching PostgreSQL's rule and giving the derived table a stable
	// canonical name.
	if strings.HasPrefix(text, "(") {
		return parseCatalogSubquerySource(text)
	}
	if strings.ContainsAny(text, ",()") {
		return TableSource{}, fmt.Errorf("compound table source is outside the canonical grammar")
	}
	fields := strings.Fields(text)
	if len(fields) == 0 || len(fields) > 3 {
		return TableSource{}, fmt.Errorf("unsupported table source %q", text)
	}
	table, ok := parseCatalogIdentifier(fields[0])
	if !ok || strings.Contains(table, ".") {
		return TableSource{}, fmt.Errorf("unsupported table source %q", text)
	}
	source := TableSource{Table: table}
	if len(fields) == 2 {
		source.Alias, ok = parseCatalogIdentifier(fields[1])
	} else if len(fields) == 3 && strings.EqualFold(fields[1], "AS") {
		source.Alias, ok = parseCatalogIdentifier(fields[2])
	} else if len(fields) > 1 {
		ok = false
	}
	if !ok && len(fields) > 1 {
		return TableSource{}, fmt.Errorf("unsupported table alias %q", text)
	}
	return source, nil
}

// parseCatalogSubquerySource parses a derived table `( <select> ) [AS] alias`.
// The parenthesized query is parsed with the same bounded grammar (recursively,
// so it may itself carry CTEs, compounds or nested derived tables). An alias is
// required and must be a simple identifier.
func parseCatalogSubquerySource(text string) (TableSource, error) {
	close := matchingParenthesis(text, 0)
	if close < 0 {
		return TableSource{}, fmt.Errorf("derived table has unbalanced parentheses")
	}
	subquery, err := parseCatalogSelect(strings.TrimSpace(text[1:close]))
	if err != nil {
		return TableSource{}, err
	}
	rest := strings.TrimSpace(text[close+1:])
	fields := strings.Fields(rest)
	var alias string
	var ok bool
	switch {
	case len(fields) == 1:
		alias, ok = parseCatalogIdentifier(fields[0])
	case len(fields) == 2 && strings.EqualFold(fields[0], "AS"):
		alias, ok = parseCatalogIdentifier(fields[1])
	}
	if !ok || alias == "" || strings.Contains(alias, ".") {
		return TableSource{}, fmt.Errorf("derived table requires a simple alias, got %q", rest)
	}
	return TableSource{Subquery: &subquery, Alias: alias}, nil
}

func parseCatalogFrom(text string) (TableSource, []Join, error) {
	text = strings.TrimSpace(text)
	for hasOuterParentheses(text) {
		text = strings.TrimSpace(text[1 : len(text)-1])
	}
	position, _, _, found := nextCatalogJoin(text)
	if !found {
		source, err := parseCatalogTableSource(text)
		return source, nil, err
	}
	base, err := parseCatalogTableSource(strings.TrimSpace(text[:position]))
	if err != nil {
		return TableSource{}, nil, err
	}
	remainder := strings.TrimSpace(text[position:])
	var joins []Join
	for remainder != "" {
		start, kind, joinKeyword, found := nextCatalogJoin(remainder)
		if !found || start != 0 {
			return TableSource{}, nil, fmt.Errorf("invalid JOIN sequence %q", remainder)
		}
		// Span the actual matched keyword so internal whitespace such as
		// "LEFT  OUTER  JOIN" consumes the right number of characters.
		end, _ := keywordMatchSpan(remainder, start, joinKeyword)
		tail := strings.TrimSpace(remainder[end:])
		next, _, _, hasNext := nextCatalogJoin(tail)
		segment := tail
		if hasNext {
			segment = strings.TrimSpace(tail[:next])
			remainder = strings.TrimSpace(tail[next:])
		} else {
			remainder = ""
		}
		join := Join{Kind: kind}
		if kind == "cross" {
			join.Table, err = parseCatalogTableSource(segment)
		} else {
			on := topLevelKeyword(segment, "ON")
			if on < 0 {
				return TableSource{}, nil, fmt.Errorf("JOIN has no ON condition")
			}
			join.Table, err = parseCatalogTableSource(strings.TrimSpace(segment[:on]))
			if err == nil {
				join.On, err = parseCatalogExpression(strings.TrimSpace(segment[on+len("ON"):]))
			}
		}
		if err != nil {
			return TableSource{}, nil, err
		}
		joins = append(joins, join)
	}
	return base, joins, nil
}

// cteQueryReferencesName reports whether a CTE's own body references the CTE's
// own name as a table source, which would make it self-referential (recursive in
// fact) without the RECURSIVE keyword. A CTE legitimately referencing a *previous*
// sibling CTE is not caught: this only inspects a query for references to the one
// name passed in (the CTE's own name).
//
// Inner shadowing is respected: if a nested scope rebinds name via its own WITH,
// references to name inside that scope resolve to the inner binding, not the outer
// CTE, so that scope is not treated as a self-reference.
func cteQueryReferencesName(query SelectQuery, name string) bool {
	// If this query rebinds name via its own WITH, every reference to name at or
	// below this query resolves to that inner binding, not to the CTE we are
	// checking, so this scope is not a self-reference.
	for _, cte := range query.With {
		if cte.Name == name {
			return false
		}
	}
	for _, cte := range query.With {
		if cteQueryReferencesName(cte.Query, name) {
			return true
		}
	}
	if tableSourceReferencesName(query.From, name) {
		return true
	}
	for _, join := range query.Joins {
		if tableSourceReferencesName(join.Table, name) {
			return true
		}
	}
	for _, compound := range query.Compounds {
		if cteQueryReferencesName(compound.Query, name) {
			return true
		}
	}
	return false
}

// tableSourceReferencesName reports whether a FROM/JOIN source is (or, for a
// derived table, contains a reference to) the named table.
func tableSourceReferencesName(source TableSource, name string) bool {
	if source.Subquery != nil {
		return cteQueryReferencesName(*source.Subquery, name)
	}
	return source.Table == name
}

func nextCatalogJoin(text string) (position int, kind string, keyword string, found bool) {
	candidates := []struct {
		keyword, kind string
	}{
		{"LEFT OUTER JOIN", "left"},
		{"LEFT JOIN", "left"},
		{"INNER JOIN", "inner"},
		{"CROSS JOIN", "cross"},
		{"JOIN", "inner"},
	}
	position = -1
	kind = ""
	keyword = ""
	for _, candidate := range candidates {
		candidateAt := topLevelKeyword(text, candidate.keyword)
		if candidateAt >= 0 && (position < 0 || candidateAt < position) {
			position = candidateAt
			kind = candidate.kind
			keyword = candidate.keyword
		}
	}
	return position, kind, keyword, position >= 0
}
