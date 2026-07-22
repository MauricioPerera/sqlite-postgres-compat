package compat

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"unicode"
)

// CallRoutine executes a canonical routine inside one transaction. The routine
// is stored in the canonical schema and therefore has the same implementation
// on SQLite and PostgreSQL instead of relying on engine-specific languages.
func (store *Store) CallRoutine(ctx context.Context, schema Schema, name string, arguments map[string]Value) error {
	var routine *Routine
	for i := range schema.Routines {
		if schema.Routines[i].Name == name {
			routine = &schema.Routines[i]
			break
		}
	}
	if routine == nil {
		return fmt.Errorf("routine %q not found", name)
	}
	for _, parameter := range routine.Parameters {
		if _, exists := arguments[parameter.Name]; !exists {
			return fmt.Errorf("routine %q missing parameter %q", name, parameter.Name)
		}
	}

	tx, err := store.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, action := range routine.Actions {
		if action.Kind != "insert" {
			return fmt.Errorf("routine %q action %q is unsupported", name, action.Kind)
		}
		table, err := findTable(schema, action.Table)
		if err != nil {
			return err
		}
		row := make(Row, len(action.Assignments))
		for _, assignment := range action.Assignments {
			value, err := routineValue(assignment.Value, arguments)
			if err != nil {
				return err
			}
			row[assignment.Column] = value
		}
		if err := insertRow(ctx, tx, store.Target.Engine, table, row); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func findTable(schema Schema, name string) (Table, error) {
	for _, table := range schema.Tables {
		if table.Name == name {
			return table, nil
		}
	}
	return Table{}, fmt.Errorf("table %q not found", name)
}

func routineValue(expression Expression, arguments map[string]Value) (Value, error) {
	switch expression.Kind {
	case "parameter":
		value, exists := arguments[expression.Value]
		if !exists {
			return Value{}, fmt.Errorf("parameter %q not supplied", expression.Value)
		}
		return value, nil
	case "null":
		return Value{Kind: NullValue}, nil
	case "string":
		return Value{Kind: TextValue, Value: expression.Value}, nil
	case "integer":
		return Value{Kind: IntegerValue, Value: expression.Value}, nil
	case "decimal":
		return Value{Kind: DecimalValue, Value: expression.Value}, nil
	case "boolean":
		return Value{Kind: BooleanValue, Value: expression.Value}, nil
	default:
		return Value{}, fmt.Errorf("routine expression %q is unsupported", expression.Kind)
	}
}

type SearchResult struct {
	ID string `json:"id"`
}

// SearchText implements deterministic Unicode token matching in the common Go
// runtime. It does not delegate tokenization or ranking to either database.
func (store *Store) SearchText(ctx context.Context, table, idColumn string, textColumns []string, query string) ([]SearchResult, error) {
	if table == "" || idColumn == "" || len(textColumns) == 0 {
		return nil, fmt.Errorf("table, id column and text columns are required")
	}
	columns := []string{quoteIdentifier(idColumn)}
	for _, column := range textColumns {
		columns = append(columns, quoteIdentifier(column))
	}
	rows, err := store.DB.QueryContext(ctx, "SELECT "+strings.Join(columns, ", ")+" FROM "+quoteIdentifier(table))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	queryTokens := tokenize(query)
	results := make([]SearchResult, 0)
	for rows.Next() {
		values := make([]any, len(columns))
		destinations := make([]any, len(columns))
		for i := range values {
			destinations[i] = &values[i]
		}
		if err := rows.Scan(destinations...); err != nil {
			return nil, err
		}
		var content strings.Builder
		for _, value := range values[1:] {
			// A NULL column carries no searchable text; stringify(nil) would
			// otherwise emit "<nil>" and tokenize to a spurious "nil" token.
			if value == nil {
				continue
			}
			content.WriteString(stringify(value))
			content.WriteByte(' ')
		}
		available := make(map[string]struct{})
		for _, token := range tokenize(content.String()) {
			available[token] = struct{}{}
		}
		matched := true
		for _, token := range queryTokens {
			if _, exists := available[token]; !exists {
				matched = false
				break
			}
		}
		if matched {
			results = append(results, SearchResult{ID: stringify(values[0])})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(results, func(i, j int) bool { return results[i].ID < results[j].ID })
	return results, nil
}

func tokenize(value string) []string {
	return strings.FieldsFunc(strings.ToLower(value), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	})
}
