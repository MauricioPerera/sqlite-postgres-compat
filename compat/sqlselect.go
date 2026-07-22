package compat

import (
	"fmt"
	"strconv"
	"strings"
)

// parseCatalogSelect parses the shared, deliberately bounded SELECT grammar
// used for external views. Unsupported clauses are rejected explicitly.
func parseCatalogSelect(definition string) (SelectQuery, error) {
	text := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(definition), ";"))
	upper := strings.ToUpper(text)
	if strings.HasPrefix(upper, "CREATE ") {
		position := strings.Index(upper, " AS SELECT ")
		if position < 0 {
			return SelectQuery{}, fmt.Errorf("view definition has no AS SELECT")
		}
		text = strings.TrimSpace(text[position+len(" AS "):])
	}
	if !strings.HasPrefix(strings.ToUpper(text), "SELECT ") {
		return SelectQuery{}, fmt.Errorf("view definition is not SELECT")
	}
	body := strings.TrimSpace(text[len("SELECT "):])
	query := SelectQuery{}
	if strings.HasPrefix(strings.ToUpper(body), "DISTINCT ") {
		query.Distinct = true
		body = strings.TrimSpace(body[len("DISTINCT "):])
	}
	fromPosition := topLevelKeyword(body, "FROM")
	if fromPosition < 0 {
		return SelectQuery{}, fmt.Errorf("SELECT has no FROM")
	}
	projectionText := strings.TrimSpace(body[:fromPosition])
	remainder := strings.TrimSpace(body[fromPosition+len("FROM"):])

	clauses := []struct {
		keyword string
		apply   func(string) error
	}{
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
		{"ORDER BY", func(value string) error {
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
		}},
		{"LIMIT", func(value string) error {
			number, err := strconv.Atoi(strings.TrimSpace(value))
			if err != nil {
				return err
			}
			// SQLite treats a negative LIMIT as "no limit", a semantics this
			// compatibility layer cannot preserve. Reject it explicitly at
			// parse time instead of accepting it silently.
			if number < 0 {
				return fmt.Errorf("unsupported negative LIMIT %d", number)
			}
			query.Limit = &number
			return nil
		}},
		{"OFFSET", func(value string) error {
			number, err := strconv.Atoi(strings.TrimSpace(value))
			if err != nil {
				return err
			}
			// A negative OFFSET has no portable meaning here; reject it
			// explicitly at parse time rather than accepting it silently.
			if number < 0 {
				return fmt.Errorf("unsupported negative OFFSET %d", number)
			}
			query.Offset = &number
			return nil
		}},
	}

	type locatedClause struct {
		position int
		index    int
	}
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
	for i, current := range located {
		start := current.position + len(clauses[current.index].keyword)
		end := len(remainder)
		if i+1 < len(located) {
			end = located[i+1].position
		}
		if err := clauses[current.index].apply(strings.TrimSpace(remainder[start:end])); err != nil {
			return SelectQuery{}, err
		}
	}

	for _, item := range splitTopLevelComma(projectionText) {
		expressionText, alias := splitProjectionAlias(item)
		expression, err := parseCatalogExpression(expressionText)
		if err != nil {
			return SelectQuery{}, err
		}
		query.Columns = append(query.Columns, Projection{Expression: expression, Alias: alias})
	}
	if len(query.Columns) == 0 {
		return SelectQuery{}, fmt.Errorf("SELECT has no projections")
	}
	return query, nil
}

func topLevelKeyword(text, keyword string) int {
	depth := 0
	inSingle, inDouble := false, false
	upper := strings.ToUpper(text)
	for i := 0; i+len(keyword) <= len(text); i++ {
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
		if depth == 0 && !inSingle && !inDouble && strings.HasPrefix(upper[i:], keyword) && wordBoundary(text, i-1) && wordBoundary(text, i+len(keyword)) {
			return i
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

func parseCatalogFrom(text string) (TableSource, []Join, error) {
	text = strings.TrimSpace(text)
	for hasOuterParentheses(text) {
		text = strings.TrimSpace(text[1 : len(text)-1])
	}
	position, _, found := nextCatalogJoin(text)
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
		start, kind, found := nextCatalogJoin(remainder)
		if !found || start != 0 {
			return TableSource{}, nil, fmt.Errorf("invalid JOIN sequence %q", remainder)
		}
		keywordLength := len("JOIN")
		switch kind {
		case "left":
			if strings.HasPrefix(strings.ToUpper(remainder), "LEFT OUTER JOIN") {
				keywordLength = len("LEFT OUTER JOIN")
			} else {
				keywordLength = len("LEFT JOIN")
			}
		case "inner":
			if strings.HasPrefix(strings.ToUpper(remainder), "INNER JOIN") {
				keywordLength = len("INNER JOIN")
			}
		case "cross":
			keywordLength = len("CROSS JOIN")
		}
		tail := strings.TrimSpace(remainder[keywordLength:])
		next, _, hasNext := nextCatalogJoin(tail)
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

func nextCatalogJoin(text string) (int, string, bool) {
	candidates := []struct {
		keyword, kind string
	}{
		{"LEFT OUTER JOIN", "left"},
		{"LEFT JOIN", "left"},
		{"INNER JOIN", "inner"},
		{"CROSS JOIN", "cross"},
		{"JOIN", "inner"},
	}
	position := -1
	kind := ""
	for _, candidate := range candidates {
		found := topLevelKeyword(text, candidate.keyword)
		if found >= 0 && (position < 0 || found < position) {
			position = found
			kind = candidate.kind
		}
	}
	return position, kind, position >= 0
}
