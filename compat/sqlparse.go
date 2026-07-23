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

	// A searched CASE is a primary that spans from CASE to its matching END. It
	// is detected before the binary-operator levels so a whole "CASE ... END"
	// operand is treated as one unit; parseCatalogCase defers (ok=false) when the
	// text merely starts with CASE but the matching END is not the final token,
	// letting the operator splitters carve it out (e.g. "CASE ... END = 1").
	if expression, ok, err := parseCatalogCase(text); ok {
		return expression, err
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
		// BETWEEN and IN sit at comparison precedence. They are attempted only
		// after the comparison split above fails, so a bare comparison like
		// "a = b" still splits normally, while "a BETWEEN x AND y" and
		// "a IN (v, ...)" — which carry no top-level comparison symbol — are
		// recognized here. The BETWEEN delimiter AND was protected from the AND
		// level split by betweenDelimiterANDs, so the whole predicate reaches
		// this point intact.
		if index == 3 {
			if expression, ok, err := parseCatalogBetween(text); ok {
				return expression, err
			}
			if expression, ok, err := parseCatalogIn(text); ok {
				return expression, err
			}
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
		case "nullif":
			// nullif(a, b) returns NULL when a = b, else a. It is standard and
			// byte-identical in both engines (verified against real PostgreSQL in
			// docs/reports/FEAT-CUBOA-1-REPORT.md).
			args, err := parseFunctionArguments(argument)
			if err != nil {
				return Expression{}, err
			}
			if len(args) != 2 {
				return Expression{}, fmt.Errorf("nullif requires two arguments")
			}
			return Expression{Kind: "nullif", Args: args}, nil
		case "gen_random_uuid":
			// gen_random_uuid() is a nullary, NON-DETERMINISTIC generator: it
			// produces a fresh random RFC 4122 v4 UUID on every call. It takes
			// zero arguments (empty parentheses); any argument is an arity error.
			// Unlike every other node in this grammar it is NOT byte-identical
			// between engines by value (that is impossible for a random source);
			// the equivalence proof is that both engines emit a valid v4 UUID and
			// successive calls differ. See docs/reports/FEAT-RANDOMUUID-REPORT.md.
			if strings.TrimSpace(argument) != "" {
				return Expression{}, fmt.Errorf("gen_random_uuid takes no arguments")
			}
			return Expression{Kind: "gen_random_uuid"}, nil
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
	// A searched CASE ... END is opaque to operator splitting: the WHEN/THEN
	// bodies carry their own comparisons and AND/OR that must not be mistaken for
	// top-level operators. mask marks every byte inside a top-level CASE ... END
	// so those positions are skipped, the same way parenthesized and quoted
	// regions already are.
	mask := caseSpanMask(text)
	// The AND that delimits a BETWEEN ("x BETWEEN lo AND hi") is syntactically an
	// AND but must not split the predicate. When this level includes the logical
	// AND operator, precompute which AND positions are BETWEEN delimiters so they
	// can be skipped, leaving only genuine logical ANDs as split points.
	var betweenANDs map[int]bool
	for _, operator := range operators {
		if operator.kind == "and" {
			betweenANDs = betweenDelimiterANDs(text, mask)
			break
		}
	}
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
		if depth != 0 || inSingle || inDouble || mask[i] {
			continue
		}
		for _, operator := range operators {
			start, ok := catalogOperatorSpan(text, i, operator.token)
			if !ok {
				continue
			}
			if catalogWordOperator(operator.token) && (!wordBoundary(text, start-1) || !wordBoundary(text, i+1)) {
				continue
			}
			if operator.kind == "and" && betweenANDs[start] {
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

// catalogOperatorSpan reports whether an operator token matches text ending at
// index end (inclusive) and returns the index where the operator begins.
//
// Symbol operators and single-word operators ("AND", "OR", "LIKE") match
// verbatim with a fixed width. Multi-word operators ("IS NULL", "IS NOT NULL")
// allow any run of whitespace wherever the token has a single separating space,
// mirroring keywordMatchSpan (which matches left to right): SQLite and Postgres
// treat "x IS  NULL" and "x IS\tNOT\tNULL" the same as "x IS NULL". The match
// ends exactly at end, so the right-to-left scan in splitCatalogOperator still
// selects the rightmost operator and keeps left-associative splits.
func catalogOperatorSpan(text string, end int, token string) (int, bool) {
	if !strings.Contains(token, " ") {
		start := end - len(token) + 1
		if start < 0 || !strings.EqualFold(text[start:end+1], token) {
			return 0, false
		}
		return start, true
	}
	words := strings.Split(token, " ")
	i := end + 1 // exclusive end, just past the last matched character
	for w := len(words) - 1; w >= 0; w-- {
		word := words[w]
		if w < len(words)-1 {
			// A separator (>= 1 whitespace) must sit between this word and the
			// already-matched word to its right, matching keywordMatchSpan.
			if i == 0 || !unicode.IsSpace(rune(text[i-1])) {
				return 0, false
			}
			for i > 0 && unicode.IsSpace(rune(text[i-1])) {
				i--
			}
		}
		if i-len(word) < 0 || !strings.EqualFold(text[i-len(word):i], word) {
			return 0, false
		}
		i -= len(word)
	}
	return i, true
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

// isCatalogNumber reports whether text is a valid SQLite numeric literal
// (decimal or integer), matching the grammar:
//
//	[+-]? ( digits [. [digits]] | .digits ) [ ([eE] [+-]? digits) ]
//
// The mantissa must contribute at least one digit before any exponent, so a
// bare exponent such as "E5" or "e10" — which has no mantissa digits before
// the e/E — is NOT a number: SQLite treats it as an identifier (column). The
// previous "at least one digit over [0-9.+-eE]" check accepted such forms
// wrongly and emitted them unquoted, which folded to the wrong column. Hex
// literals (0x...) are handled separately by catalogHexLiteral and never reach
// here.
func isCatalogNumber(text string) bool {
	if text == "" {
		return false
	}
	i := 0
	if text[0] == '+' || text[0] == '-' {
		i++
	}
	// Mantissa: digits, an optional dot, then optional digits. ".5" (no
	// leading digits) is valid; "1." and "1.5" are valid too.
	integerStart := i
	for i < len(text) && text[i] >= '0' && text[i] <= '9' {
		i++
	}
	hasDigits := i > integerStart
	if i < len(text) && text[i] == '.' {
		i++
		fractionStart := i
		for i < len(text) && text[i] >= '0' && text[i] <= '9' {
			i++
		}
		hasDigits = hasDigits || i > fractionStart
	}
	if !hasDigits {
		// "e5" / "E10" / "." / "+" reach here: no mantissa digit -> not a number.
		return false
	}
	// Optional exponent, only after a valid mantissa. The exponent itself
	// must carry at least one digit ("1e" is not a number).
	if i < len(text) && (text[i] == 'e' || text[i] == 'E') {
		i++
		if i < len(text) && (text[i] == '+' || text[i] == '-') {
			i++
		}
		exponentStart := i
		for i < len(text) && text[i] >= '0' && text[i] <= '9' {
			i++
		}
		if i == exponentStart {
			return false
		}
	}
	return i == len(text)
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
//
// The hex digits are parsed as an unsigned 64-bit value and then reinterpreted
// as a signed two's-complement int64, matching how SQLite evaluates hex
// literals: 0xFFFFFFFFFFFFFFFF is -1, not 18446744073709551615.
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
	parsed, parseErr := strconv.ParseUint(digits, 16, 64)
	if parseErr != nil {
		return "", true, fmt.Errorf("unsupported catalog hexadecimal literal %q", text)
	}
	return strconv.FormatInt(int64(parsed), 10), true, nil
}

func isHexDigit(character rune) bool {
	return unicode.IsDigit(character) ||
		(character >= 'a' && character <= 'f') ||
		(character >= 'A' && character <= 'F')
}

// matchWordAt reports whether keyword (case-insensitive) starts at index i in
// text as a standalone word, i.e. bounded by a non-identifier character (or the
// string edge) on both sides. Keywords in this grammar are ASCII, so byte
// indexing is safe.
func matchWordAt(text string, i int, keyword string) bool {
	n := len(keyword)
	if i < 0 || i+n > len(text) {
		return false
	}
	if !strings.EqualFold(text[i:i+n], keyword) {
		return false
	}
	return wordBoundary(text, i-1) && wordBoundary(text, i+n)
}

// caseSpanMask returns a per-byte mask marking every character that lies inside
// a top-level "CASE ... END" span (the CASE, the END, and everything between).
// Spans are matched with nesting: an inner CASE ... END is absorbed by the
// enclosing one, so the whole outer construct is masked as a single opaque unit.
//
// Only CASE keywords at parenthesis depth zero and outside quotes open a span,
// and an END without a matching open CASE is ignored — so a column literally
// named "end" (unquoted) does not spuriously mask surrounding operators. Callers
// use the mask to keep the expression splitters from reaching inside a CASE.
func caseSpanMask(text string) []bool {
	mask := make([]bool, len(text))
	depth := 0
	inSingle, inDouble := false, false
	var stack []int
	for i := 0; i < len(text); i++ {
		character := text[i]
		if inSingle {
			if character == '\'' {
				if i+1 < len(text) && text[i+1] == '\'' {
					i++
					continue
				}
				inSingle = false
			}
			continue
		}
		if inDouble {
			if character == '"' {
				inDouble = false
			}
			continue
		}
		switch character {
		case '\'':
			inSingle = true
			continue
		case '"':
			inDouble = true
			continue
		case '(':
			depth++
			continue
		case ')':
			depth--
			continue
		}
		if depth != 0 {
			continue
		}
		if matchWordAt(text, i, "CASE") {
			stack = append(stack, i)
			continue
		}
		if matchWordAt(text, i, "END") && len(stack) > 0 {
			start := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if len(stack) == 0 {
				for j := start; j < i+len("END"); j++ {
					mask[j] = true
				}
			}
		}
	}
	return mask
}

// betweenDelimiterANDs returns the set of start indices of the "AND" keywords
// that delimit a BETWEEN ("x BETWEEN lo AND hi"), so the AND-level splitter can
// skip them. A left-to-right scan at parenthesis depth zero (outside quotes and
// outside CASE spans) pairs each BETWEEN with the next unpaired AND; because a
// BETWEEN bound cannot contain a bare top-level AND, this pairing is exact.
func betweenDelimiterANDs(text string, mask []bool) map[int]bool {
	result := make(map[int]bool)
	depth := 0
	inSingle, inDouble := false, false
	pending := 0
	for i := 0; i < len(text); i++ {
		character := text[i]
		if inSingle {
			if character == '\'' {
				if i+1 < len(text) && text[i+1] == '\'' {
					i++
					continue
				}
				inSingle = false
			}
			continue
		}
		if inDouble {
			if character == '"' {
				inDouble = false
			}
			continue
		}
		switch character {
		case '\'':
			inSingle = true
			continue
		case '"':
			inDouble = true
			continue
		case '(':
			depth++
			continue
		case ')':
			depth--
			continue
		}
		if depth != 0 || (mask != nil && mask[i]) {
			continue
		}
		if matchWordAt(text, i, "BETWEEN") {
			pending++
			continue
		}
		if matchWordAt(text, i, "AND") {
			if pending > 0 {
				result[i] = true
				pending--
			}
		}
	}
	return result
}

// findCatalogKeyword returns the byte range [start, end) of the leftmost
// standalone occurrence of keyword at parenthesis depth zero, outside quotes and
// outside CASE spans. It underpins BETWEEN and IN detection.
func findCatalogKeyword(text, keyword string, mask []bool) (start, end int, found bool) {
	depth := 0
	inSingle, inDouble := false, false
	for i := 0; i < len(text); i++ {
		character := text[i]
		if inSingle {
			if character == '\'' {
				if i+1 < len(text) && text[i+1] == '\'' {
					i++
					continue
				}
				inSingle = false
			}
			continue
		}
		if inDouble {
			if character == '"' {
				inDouble = false
			}
			continue
		}
		switch character {
		case '\'':
			inSingle = true
			continue
		case '"':
			inDouble = true
			continue
		case '(':
			depth++
			continue
		case ')':
			depth--
			continue
		}
		if depth != 0 || (mask != nil && mask[i]) {
			continue
		}
		if matchWordAt(text, i, keyword) {
			return i, i + len(keyword), true
		}
	}
	return 0, 0, false
}

// parseCatalogBetween recognizes "operand BETWEEN low AND high" and its negated
// form "operand NOT BETWEEN low AND high". It returns ok=false when no top-level
// BETWEEN is present so the caller can try the next rule. The construct compiles
// to a native BETWEEN on both engines, which is inclusive and evaluated as
// low <= operand <= high identically in SQLite and PostgreSQL.
func parseCatalogBetween(text string) (Expression, bool, error) {
	mask := caseSpanMask(text)
	betweenStart, betweenEnd, ok := findCatalogKeyword(text, "BETWEEN", mask)
	if !ok {
		return Expression{}, false, nil
	}
	left := strings.TrimSpace(text[:betweenStart])
	negated := false
	if stripped, stripOK := stripTrailingNot(left); stripOK {
		left = stripped
		negated = true
	}
	if left == "" {
		return Expression{}, true, fmt.Errorf("BETWEEN requires an operand")
	}
	rest := text[betweenEnd:]
	restMask := caseSpanMask(rest)
	andStart, andEnd, ok := findCatalogKeyword(rest, "AND", restMask)
	if !ok {
		return Expression{}, true, fmt.Errorf("BETWEEN requires an AND delimiter")
	}
	lowText := strings.TrimSpace(rest[:andStart])
	highText := strings.TrimSpace(rest[andEnd:])
	if lowText == "" || highText == "" {
		return Expression{}, true, fmt.Errorf("BETWEEN requires low and high bounds")
	}
	operand, err := parseCatalogExpression(left)
	if err != nil {
		return Expression{}, true, err
	}
	low, err := parseCatalogExpression(lowText)
	if err != nil {
		return Expression{}, true, err
	}
	high, err := parseCatalogExpression(highText)
	if err != nil {
		return Expression{}, true, err
	}
	kind := "between"
	if negated {
		kind = "not_between"
	}
	return Expression{Kind: kind, Args: []Expression{operand, low, high}}, true, nil
}

// parseCatalogIn recognizes "operand IN (v1, v2, ...)" and "operand NOT IN
// (...)" over an explicit value list. Subqueries are intentionally out of scope:
// any non-value list element fails to parse and the whole predicate is rejected.
// It compiles to a native IN list on both engines, whose membership semantics
// (including three-valued logic on NULL) are identical in SQLite and PostgreSQL.
func parseCatalogIn(text string) (Expression, bool, error) {
	mask := caseSpanMask(text)
	inStart, inEnd, ok := findCatalogKeyword(text, "IN", mask)
	if !ok {
		return Expression{}, false, nil
	}
	left := strings.TrimSpace(text[:inStart])
	negated := false
	if stripped, stripOK := stripTrailingNot(left); stripOK {
		left = stripped
		negated = true
	}
	if left == "" {
		return Expression{}, true, fmt.Errorf("IN requires an operand")
	}
	rest := strings.TrimSpace(text[inEnd:])
	if len(rest) < 2 || rest[0] != '(' || matchingParenthesis(rest, 0) != len(rest)-1 {
		return Expression{}, true, fmt.Errorf("IN requires a parenthesized value list")
	}
	inner := strings.TrimSpace(rest[1 : len(rest)-1])
	if inner == "" {
		return Expression{}, true, fmt.Errorf("IN requires at least one value")
	}
	operand, err := parseCatalogExpression(left)
	if err != nil {
		return Expression{}, true, err
	}
	args := []Expression{operand}
	for _, part := range splitTopLevelCommas(inner) {
		value, err := parseCatalogExpression(part)
		if err != nil {
			return Expression{}, true, err
		}
		args = append(args, value)
	}
	kind := "in"
	if negated {
		kind = "not_in"
	}
	return Expression{Kind: kind, Args: args}, true, nil
}

// parseCatalogCase recognizes a searched CASE expression:
//
//	CASE WHEN cond THEN value [WHEN cond THEN value ...] [ELSE value] END
//
// The simple form "CASE operand WHEN value ..." is rejected: only the searched
// form has identical evaluation in both engines without operand-type coercion
// surprises. Args layout is [cond1, value1, cond2, value2, ..., (elseValue)];
// an odd Args length means the trailing element is the ELSE value, an even
// length means there is no ELSE. It compiles to a native CASE on both engines.
//
// ok=false is returned when text does not begin with CASE, or begins with CASE
// but its matching END is not the final token (so a larger expression such as
// "CASE ... END = 1" is left for the operator splitters to divide first).
func parseCatalogCase(text string) (Expression, bool, error) {
	if !matchWordAt(text, 0, "CASE") {
		return Expression{}, false, nil
	}
	endStart, ok := matchingCaseEnd(text)
	if !ok {
		return Expression{}, true, fmt.Errorf("CASE has no matching END")
	}
	if strings.TrimSpace(text[endStart+len("END"):]) != "" {
		// CASE is only a prefix of a larger expression; defer to the splitters.
		return Expression{}, false, nil
	}
	interior := text[len("CASE"):endStart]
	segments, ok := splitCaseSegments(interior)
	if !ok {
		return Expression{}, true, fmt.Errorf("CASE structure is outside the canonical grammar")
	}
	if segments.leading != "" {
		return Expression{}, true, fmt.Errorf("simple CASE (operand form) is outside the canonical grammar")
	}
	if len(segments.whens) == 0 {
		return Expression{}, true, fmt.Errorf("CASE requires at least one WHEN branch")
	}
	args := make([]Expression, 0, len(segments.whens)*2+1)
	for _, branch := range segments.whens {
		cond, err := parseCatalogExpression(branch.cond)
		if err != nil {
			return Expression{}, true, err
		}
		value, err := parseCatalogExpression(branch.value)
		if err != nil {
			return Expression{}, true, err
		}
		args = append(args, cond, value)
	}
	if segments.hasElse {
		value, err := parseCatalogExpression(segments.elseValue)
		if err != nil {
			return Expression{}, true, err
		}
		args = append(args, value)
	}
	return Expression{Kind: "case", Args: args}, true, nil
}

// matchingCaseEnd returns the start index of the END that closes the CASE at
// index 0, honouring nested CASE ... END, parentheses and quotes.
func matchingCaseEnd(text string) (int, bool) {
	depth := 0
	inSingle, inDouble := false, false
	caseDepth := 0
	for i := 0; i < len(text); i++ {
		character := text[i]
		if inSingle {
			if character == '\'' {
				if i+1 < len(text) && text[i+1] == '\'' {
					i++
					continue
				}
				inSingle = false
			}
			continue
		}
		if inDouble {
			if character == '"' {
				inDouble = false
			}
			continue
		}
		switch character {
		case '\'':
			inSingle = true
			continue
		case '"':
			inDouble = true
			continue
		case '(':
			depth++
			continue
		case ')':
			depth--
			continue
		}
		if depth != 0 {
			continue
		}
		if matchWordAt(text, i, "CASE") {
			caseDepth++
			continue
		}
		if matchWordAt(text, i, "END") {
			caseDepth--
			if caseDepth == 0 {
				return i, true
			}
		}
	}
	return 0, false
}

type caseBranch struct {
	cond  string
	value string
}

type caseSegments struct {
	leading   string // text between CASE and the first WHEN (must be empty: searched form)
	whens     []caseBranch
	hasElse   bool
	elseValue string
}

// splitCaseSegments parses the interior of a CASE (the text between CASE and its
// matching END) into WHEN/THEN branches and an optional ELSE, honouring nested
// CASE spans, parentheses and quotes. It returns ok=false on any structural
// violation (missing THEN, ELSE after ELSE, empty branch, trailing tokens).
func splitCaseSegments(interior string) (caseSegments, bool) {
	mask := caseSpanMask(interior)
	// Collect the positions of this CASE's own WHEN/THEN/ELSE keywords.
	type marker struct {
		kw    string
		start int
		end   int
	}
	var markers []marker
	depth := 0
	inSingle, inDouble := false, false
	for i := 0; i < len(interior); i++ {
		character := interior[i]
		if inSingle {
			if character == '\'' {
				if i+1 < len(interior) && interior[i+1] == '\'' {
					i++
					continue
				}
				inSingle = false
			}
			continue
		}
		if inDouble {
			if character == '"' {
				inDouble = false
			}
			continue
		}
		switch character {
		case '\'':
			inSingle = true
			continue
		case '"':
			inDouble = true
			continue
		case '(':
			depth++
			continue
		case ')':
			depth--
			continue
		}
		if depth != 0 || mask[i] {
			continue
		}
		for _, kw := range []string{"WHEN", "THEN", "ELSE"} {
			if matchWordAt(interior, i, kw) {
				markers = append(markers, marker{kw: kw, start: i, end: i + len(kw)})
				break
			}
		}
	}
	if len(markers) == 0 {
		return caseSegments{}, false
	}
	result := caseSegments{leading: strings.TrimSpace(interior[:markers[0].start])}
	// Walk the markers building WHEN/THEN pairs and an optional trailing ELSE.
	i := 0
	for i < len(markers) {
		if markers[i].kw != "WHEN" {
			return caseSegments{}, false
		}
		if i+1 >= len(markers) || markers[i+1].kw != "THEN" {
			return caseSegments{}, false
		}
		cond := strings.TrimSpace(interior[markers[i].end:markers[i+1].start])
		valueEnd := len(interior)
		next := i + 2
		if next < len(markers) {
			valueEnd = markers[next].start
		}
		value := strings.TrimSpace(interior[markers[i+1].end:valueEnd])
		if cond == "" || value == "" {
			return caseSegments{}, false
		}
		result.whens = append(result.whens, caseBranch{cond: cond, value: value})
		i = next
		if i < len(markers) && markers[i].kw == "ELSE" {
			elseEnd := len(interior)
			if i+1 < len(markers) {
				// Only one ELSE is allowed and it must be the final segment.
				return caseSegments{}, false
			}
			result.hasElse = true
			result.elseValue = strings.TrimSpace(interior[markers[i].end:elseEnd])
			if result.elseValue == "" {
				return caseSegments{}, false
			}
			i++
		}
	}
	return result, true
}
