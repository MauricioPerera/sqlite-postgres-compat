package compat

import (
	"fmt"
	"regexp"
	"strings"
)

const catalogIdentifierPattern = `(?:"(?:[^"]|"")+"|[A-Za-z_][A-Za-z0-9_]*)`

var (
	sqliteTriggerPattern   = regexp.MustCompile(`(?is)^CREATE\s+TRIGGER\s+(` + catalogIdentifierPattern + `)\s+(BEFORE|AFTER)\s+(INSERT|UPDATE|DELETE)\s+ON\s+(` + catalogIdentifierPattern + `)(?:\s+FOR\s+EACH\s+ROW)?(?:\s+WHEN\s+(.+?))?\s+BEGIN\s+(.+)\s+END\s*;?$`)
	postgresTriggerPattern = regexp.MustCompile(`(?is)^CREATE\s+TRIGGER\s+(` + catalogIdentifierPattern + `)\s+(BEFORE|AFTER)\s+(INSERT|UPDATE|DELETE)\s+ON\s+(?:` + catalogIdentifierPattern + `\.)?(` + catalogIdentifierPattern + `)\s+FOR\s+EACH\s+ROW(?:\s+WHEN\s+\((.+)\))?\s+EXECUTE\s+FUNCTION\s+.+$`)
	insertActionPattern    = regexp.MustCompile(`(?is)^INSERT\s+INTO\s+(` + catalogIdentifierPattern + `)\s*\((.+)\)\s*VALUES\s*\((.+)\)$`)
)

func parseSQLiteCatalogTrigger(definition string) (Trigger, error) {
	match := sqliteTriggerPattern.FindStringSubmatch(strings.TrimSpace(definition))
	if match == nil {
		return Trigger{}, fmt.Errorf("trigger definition is outside the canonical grammar")
	}
	trigger := Trigger{
		Name:   unquoteCatalogIdentifier(match[1]),
		Timing: strings.ToLower(match[2]),
		Event:  strings.ToLower(match[3]),
		Table:  unquoteCatalogIdentifier(match[4]),
	}
	if strings.TrimSpace(match[5]) != "" {
		when, err := parseCatalogExpression(match[5])
		if err != nil {
			return Trigger{}, err
		}
		trigger.When = &when
	}
	actions, err := parseCatalogTriggerActions(match[6])
	if err != nil {
		return Trigger{}, err
	}
	trigger.Actions = actions
	return trigger, nil
}

func parsePostgresCatalogTrigger(definition, functionBody string) (Trigger, error) {
	match := postgresTriggerPattern.FindStringSubmatch(strings.TrimSpace(definition))
	if match == nil {
		return Trigger{}, fmt.Errorf("trigger definition is outside the canonical grammar")
	}
	trigger := Trigger{
		Name:   unquoteCatalogIdentifier(match[1]),
		Timing: strings.ToLower(match[2]),
		Event:  strings.ToLower(match[3]),
		Table:  unquoteCatalogIdentifier(match[4]),
	}
	if strings.TrimSpace(match[5]) != "" {
		when, err := parseCatalogExpression(match[5])
		if err != nil {
			return Trigger{}, err
		}
		trigger.When = &when
	}
	body := strings.TrimSpace(functionBody)
	upper := strings.ToUpper(body)
	if strings.HasPrefix(upper, "BEGIN") {
		body = strings.TrimSpace(body[len("BEGIN"):])
	}
	returnPosition := strings.LastIndex(strings.ToUpper(body), "RETURN ")
	if returnPosition < 0 {
		return Trigger{}, fmt.Errorf("trigger function has no RETURN")
	}
	body = strings.TrimSpace(body[:returnPosition])
	body = strings.TrimSuffix(body, ";")
	actions, err := parseCatalogTriggerActions(body)
	if err != nil {
		return Trigger{}, err
	}
	trigger.Actions = actions
	return trigger, nil
}

func parseCatalogTriggerActions(body string) ([]TriggerAction, error) {
	statements := splitCatalogStatements(body)
	actions := make([]TriggerAction, 0, len(statements))
	for _, statement := range statements {
		match := insertActionPattern.FindStringSubmatch(strings.TrimSpace(statement))
		if match == nil {
			return nil, fmt.Errorf("trigger action %q is outside the canonical grammar", statement)
		}
		columns := splitTopLevelComma(match[2])
		values := splitTopLevelComma(match[3])
		if len(columns) == 0 || len(columns) != len(values) {
			return nil, fmt.Errorf("trigger INSERT columns and values differ")
		}
		action := TriggerAction{Kind: "insert", Table: unquoteCatalogIdentifier(match[1])}
		for i := range columns {
			column, ok := parseCatalogIdentifier(columns[i])
			if !ok || strings.Contains(column, ".") {
				return nil, fmt.Errorf("unsupported trigger assignment column %q", columns[i])
			}
			value, err := parseCatalogExpression(values[i])
			if err != nil {
				return nil, err
			}
			action.Assignments = append(action.Assignments, Assignment{Column: column, Value: value})
		}
		actions = append(actions, action)
	}
	if len(actions) == 0 {
		return nil, fmt.Errorf("trigger has no canonical actions")
	}
	return actions, nil
}

func splitCatalogStatements(body string) []string {
	var result []string
	start := 0
	depth := 0
	inSingle, inDouble := false, false
	for i := 0; i < len(body); i++ {
		switch body[i] {
		case '\'':
			if !inDouble {
				if inSingle && i+1 < len(body) && body[i+1] == '\'' {
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
		case ';':
			if depth == 0 && !inSingle && !inDouble {
				if statement := strings.TrimSpace(body[start:i]); statement != "" {
					result = append(result, statement)
				}
				start = i + 1
			}
		}
	}
	if statement := strings.TrimSpace(body[start:]); statement != "" {
		result = append(result, statement)
	}
	return result
}

func unquoteCatalogIdentifier(identifier string) string {
	identifier = strings.TrimSpace(identifier)
	if len(identifier) >= 2 && identifier[0] == '"' && identifier[len(identifier)-1] == '"' {
		return strings.ReplaceAll(identifier[1:len(identifier)-1], `""`, `"`)
	}
	return identifier
}
