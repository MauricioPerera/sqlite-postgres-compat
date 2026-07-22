package compat

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
)

// parseCatalogExpression translates the deliberately small, shared SQL
// expression grammar into the canonical AST. It rejects everything outside
// that grammar so catalog inspection cannot silently claim equivalence.
func parseCatalogExpression(input string) (Expression, error) {
	text := strings.TrimSpace(input)
	for hasOuterParentheses(text) {
		text = strings.TrimSpace(text[1 : len(text)-1])
	}
	if text == "" {
		return Expression{}, fmt.Errorf("empty expression")
	}

	levels := [][]catalogOperator{
		{{"OR", "or"}},
		{{"AND", "and"}},
		{{"IS NOT NULL", "is_not_null"}, {"IS NULL", "is_null"}},
		{{"<=", "lte"}, {">=", "gte"}, {"<>", "ne"}, {"!=", "ne"}, {"=", "eq"}, {"<", "lt"}, {">", "gt"}, {"LIKE", "like"}},
		{{"||", "concat"}},
		{{"+", "add"}, {"-", "sub"}},
		{{"*", "mul"}, {"/", "div"}},
	}
	for index, level := range levels {
		// NOT sits between AND and the IS NULL / comparison levels: it binds
		// looser than IS NULL, LIKE and the comparison operators, but tighter
		// than AND/OR. Handling it here (instead of after every binary level)
		// makes "NOT a = b" parse as not(eq(a, b)) rather than eq(not(a), b).
		if index == 2 && hasKeywordPrefix(text, "NOT") {
			argument, err := parseCatalogExpression(strings.TrimSpace(text[3:]))
			if err != nil {
				return Expression{}, err
			}
			return Expression{Kind: "not", Args: []Expression{argument}}, nil
		}
		if left, operator, right, found := splitCatalogOperator(text, level); found {
			// "a NOT LIKE b" leaves a trailing NOT keyword on the left side of the
			// LIKE split. Fold it into not(like(...)) so the infix form matches the
			// already-working prefix form "NOT a LIKE b".
			negateLike := false
			if operator.kind == "like" {
				if stripped, ok := stripTrailingNot(left); ok {
					left = stripped
					negateLike = true
				}
			}
			leftExpression, err := parseCatalogExpression(left)
			if err != nil {
				return Expression{}, err
			}
			if operator.kind == "is_null" || operator.kind == "is_not_null" {
				return Expression{Kind: operator.kind, Args: []Expression{leftExpression}}, nil
			}
			rightExpression, err := parseCatalogExpression(right)
			if err != nil {
				return Expression{}, err
			}
			like := Expression{Kind: operator.kind, Args: []Expression{leftExpression, rightExpression}}
			if negateLike {
				return Expression{Kind: "not", Args: []Expression{like}}, nil
			}
			return like, nil
		}
	}

	upper := strings.ToUpper(text)
	switch upper {
	case "NULL":
		return Expression{Kind: "null"}, nil
	case "TRUE", "FALSE":
		return Expression{Kind: "boolean", Value: strings.ToLower(upper)}, nil
	case "CURRENT_TIMESTAMP":
		return Expression{Kind: "current_timestamp"}, nil
	}
	if text[0] == '\'' && text[len(text)-1] == '\'' {
		return Expression{Kind: "string", Value: strings.ReplaceAll(text[1:len(text)-1], "''", "'")}, nil
	}
	if value, ok, err := catalogHexLiteral(text); ok {
		if err != nil {
			return Expression{}, err
		}
		return Expression{Kind: "integer", Value: value}, nil
	}
	if isCatalogNumber(text) {
		kind := "integer"
		if strings.ContainsAny(text, ".eE") {
			kind = "decimal"
		}
		return Expression{Kind: kind, Value: text}, nil
	}
	if function, argument, ok := catalogFunctionCall(text); ok {
		switch function {
		case "count", "sum", "avg", "min", "max", "lower", "upper":
			if strings.TrimSpace(argument) == "*" {
				return Expression{Kind: function, Args: []Expression{{Kind: "star"}}}, nil
			}
			parsed, err := parseCatalogExpression(argument)
			if err != nil {
				return Expression{}, err
			}
			return Expression{Kind: function, Args: []Expression{parsed}}, nil
		case "length", "abs", "trim":
			parsed, err := parseCatalogExpression(argument)
			if err != nil {
				return Expression{}, err
			}
			return Expression{Kind: function, Args: []Expression{parsed}}, nil
		case "coalesce":
			args, err := parseFunctionArguments(argument)
			if err != nil {
				return Expression{}, err
			}
			if len(args) < 1 {
				return Expression{}, fmt.Errorf("coalesce requires at least one argument")
			}
			return Expression{Kind: "coalesce", Args: args}, nil
		case "replace":
			args, err := parseFunctionArguments(argument)
			if err != nil {
				return Expression{}, err
			}
			if len(args) != 3 {
				return Expression{}, fmt.Errorf("replace requires three arguments")
			}
			return Expression{Kind: "replace", Args: args}, nil
		default:
			return Expression{}, fmt.Errorf("unsupported catalog function %q", function)
		}
	}
	identifier, ok := parseCatalogIdentifier(text)
	if ok {
		return Expression{Kind: "column", Value: identifier}, nil
	}
	return Expression{}, fmt.Errorf("unsupported catalog expression %q", input)
}

func catalogFunctionCall(text string) (string, string, bool) {
	position := strings.IndexByte(text, '(')
	if position <= 0 || matchingParenthesis(text, position) != len(text)-1 {
		return "", "", false
	}
	name := strings.ToLower(strings.TrimSpace(text[:position]))
	for _, character := range name {
		if !(unicode.IsLetter(character) || character == '_') {
			return "", "", false
		}
	}
	return name, text[position+1 : len(text)-1], true
}

func parsePostgresCatalogDefault(input string) (Expression, error) {
	text := strings.TrimSpace(input)
	for hasOuterParentheses(text) {
		text = strings.TrimSpace(text[1 : len(text)-1])
	}
	if position := topLevelPostgresCast(text); position >= 0 {
		cast := strings.ToLower(strings.TrimSpace(text[position+2:]))
		switch cast {
		case "text", "character varying", "character", "boolean", "smallint", "integer", "bigint", "numeric", "real", "double precision", "date", "timestamp without time zone", "timestamp with time zone", "uuid", "json", "jsonb":
			text = strings.TrimSpace(text[:position])
		default:
			return Expression{}, fmt.Errorf("unsupported PostgreSQL default cast %q", cast)
		}
	}
	return parseCatalogExpression(text)
}

func topLevelPostgresCast(text string) int {
	depth := 0
	inSingle, inDouble := false, false
	for i := 0; i+1 < len(text); i++ {
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
		case ':':
			if !inSingle && !inDouble && depth == 0 && text[i+1] == ':' {
				return i
			}
		}
	}
	return -1
}

type catalogOperator struct {
	token string
	kind  string
}

func splitCatalogOperator(text string, operators []catalogOperator) (string, catalogOperator, string, bool) {
	depth := 0
	inSingle := false
	inDouble := false
	for i := len(text) - 1; i >= 0; i-- {
		switch text[i] {
		case '\'':
			if !inDouble {
				if i > 0 && text[i-1] == '\'' {
					i--
					continue
				}
				inSingle = !inSingle
			}
		case '"':
			if !inSingle {
				inDouble = !inDouble
			}
		case ')':
			if !inSingle && !inDouble {
				depth++
			}
		case '(':
			if !inSingle && !inDouble {
				depth--
			}
		}
		if depth != 0 || inSingle || inDouble {
			continue
		}
		for _, operator := range operators {
			start := i - len(operator.token) + 1
			if start < 0 || !strings.EqualFold(text[start:i+1], operator.token) {
				continue
			}
			if catalogWordOperator(operator.token) && (!wordBoundary(text, start-1) || !wordBoundary(text, i+1)) {
				continue
			}
			left := strings.TrimSpace(text[:start])
			right := strings.TrimSpace(text[i+1:])
			if left == "" || (right == "" && operator.kind != "is_null" && operator.kind != "is_not_null") {
				continue
			}
			return left, operator, right, true
		}
	}
	return "", catalogOperator{}, "", false
}

func hasOuterParentheses(text string) bool {
	if len(text) < 2 || text[0] != '(' || text[len(text)-1] != ')' {
		return false
	}
	depth := 0
	inSingle, inDouble := false, false
	for i, character := range text {
		switch character {
		case '\'':
			if !inDouble {
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
				if depth == 0 && i != len(text)-1 {
					return false
				}
			}
		}
	}
	return depth == 0 && !inSingle && !inDouble
}

func hasKeywordPrefix(text, keyword string) bool {
	return len(text) > len(keyword) && strings.EqualFold(text[:len(keyword)], keyword) && unicode.IsSpace(rune(text[len(keyword)]))
}

// hasKeywordSuffix reports whether text ends with keyword as a standalone
// word (a whitespace separator precedes it). It mirrors hasKeywordPrefix for
// the right edge, used to detect the trailing NOT in "a NOT LIKE b".
func hasKeywordSuffix(text, keyword string) bool {
	return len(text) > len(keyword) &&
		strings.EqualFold(text[len(text)-len(keyword):], keyword) &&
		unicode.IsSpace(rune(text[len(text)-len(keyword)-1]))
}

// stripTrailingNot removes a trailing NOT keyword from the left side of a
// LIKE split. It returns the stripped left side and ok=true when a trailing
// NOT keyword is present, leaving left untouched otherwise.
func stripTrailingNot(left string) (string, bool) {
	trimmed := strings.TrimSpace(left)
	if !hasKeywordSuffix(trimmed, "NOT") {
		return left, false
	}
	rest := strings.TrimSpace(trimmed[:len(trimmed)-3])
	if rest == "" {
		return left, false
	}
	return rest, true
}

// splitTopLevelCommas splits text on commas that sit at parenthesis depth zero
// and outside string/identifier quotes, so nested calls and string literals
// such as replace(a, ',', b) survive intact.
func splitTopLevelCommas(text string) []string {
	var parts []string
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
		case ',':
			if depth == 0 && !inSingle && !inDouble {
				parts = append(parts, text[start:i])
				start = i + 1
			}
		}
	}
	return append(parts, text[start:])
}

// parseFunctionArguments splits a function argument list on top-level commas
// and parses each argument as a catalog expression.
func parseFunctionArguments(argument string) ([]Expression, error) {
	parts := splitTopLevelCommas(argument)
	args := make([]Expression, 0, len(parts))
	for _, part := range parts {
		parsed, err := parseCatalogExpression(part)
		if err != nil {
			return nil, err
		}
		args = append(args, parsed)
	}
	return args, nil
}

func catalogWordOperator(token string) bool {
	return unicode.IsLetter(rune(token[0]))
}

func wordBoundary(text string, index int) bool {
	return index < 0 || index >= len(text) || !(unicode.IsLetter(rune(text[index])) || unicode.IsDigit(rune(text[index])) || text[index] == '_')
}

func isCatalogNumber(text string) bool {
	for _, character := range text {
		if unicode.IsDigit(character) || strings.ContainsRune(".+-eE", character) {
			continue
		}
		return false
	}
	return text != "" && strings.ContainsAny(text, "0123456789")
}

func parseCatalogIdentifier(text string) (string, bool) {
	parts := strings.Split(text, ".")
	for i, part := range parts {
		part = strings.TrimSpace(part)
		if len(part) >= 2 && part[0] == '"' && part[len(part)-1] == '"' {
			parts[i] = strings.ReplaceAll(part[1:len(part)-1], `""`, `"`)
			continue
		}
		if part == "" {
			return "", false
		}
		for _, character := range part {
			if !(unicode.IsLetter(character) || unicode.IsDigit(character) || character == '_') {
				return "", false
			}
		}
		parts[i] = part
	}
	return strings.Join(parts, "."), true
}

// catalogHexLiteral recognizes a SQLite hexadecimal integer literal such as
// 0x10 or 0XABCDEF and returns its decimal value. It returns handled=false for
// input that is not a hex literal (so callers fall through to other grammar
// rules), and an error when the literal exceeds the 64-bit range that SQLite
// supports. The decimal value is emitted so the expression compiles to
// PostgreSQL as a plain integer instead of a quoted identifier.
func catalogHexLiteral(text string) (value string, handled bool, err error) {
	if len(text) < 3 || text[0] != '0' || (text[1] != 'x' && text[1] != 'X') {
		return "", false, nil
	}
	digits := text[2:]
	if digits == "" {
		return "", false, nil
	}
	for _, character := range digits {
		if !isHexDigit(character) {
			return "", false, nil
		}
	}
	decimal, parseErr := strconv.ParseUint(digits, 16, 64)
	if parseErr != nil {
		return "", true, fmt.Errorf("unsupported catalog hexadecimal literal %q", text)
	}
	return strconv.FormatUint(decimal, 10), true, nil
}

func isHexDigit(character rune) bool {
	return unicode.IsDigit(character) ||
		(character >= 'a' && character <= 'f') ||
		(character >= 'A' && character <= 'F')
}
