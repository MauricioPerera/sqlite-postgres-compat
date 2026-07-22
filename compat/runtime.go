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
	engine := store.Target.Engine
	for _, action := range routine.Actions {
		switch action.Kind {
		case "insert":
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
			if err := insertRow(ctx, tx, engine, table, row); err != nil {
				return err
			}
		case "update":
			if len(action.Assignments) == 0 || action.Where == nil {
				return fmt.Errorf("routine %q UPDATE action requires assignments and WHERE", name)
			}
			table, err := findTable(schema, action.Table)
			if err != nil {
				return err
			}
			sets := make([]string, 0, len(action.Assignments))
			args := make([]any, 0, len(action.Assignments))
			position := 1
			for _, assignment := range action.Assignments {
				value, err := routineValue(assignment.Value, arguments)
				if err != nil {
					return err
				}
				argument, err := driverValue(engine, value)
				if err != nil {
					return err
				}
				sets = append(sets, quoteIdentifier(assignment.Column)+" = "+placeholder(engine, position))
				args = append(args, argument)
				position++
			}
			where, _, err := compileRoutineWhere(engine, *action.Where, arguments, &args, position)
			if err != nil {
				return err
			}
			statement := "UPDATE " + quoteIdentifier(table.Name) + " SET " + strings.Join(sets, ", ") + " WHERE " + where
			if _, err := tx.ExecContext(ctx, statement, args...); err != nil {
				return err
			}
		case "delete":
			if action.Where == nil {
				return fmt.Errorf("routine %q DELETE action requires WHERE", name)
			}
			table, err := findTable(schema, action.Table)
			if err != nil {
				return err
			}
			args := make([]any, 0)
			where, _, err := compileRoutineWhere(engine, *action.Where, arguments, &args, 1)
			if err != nil {
				return err
			}
			statement := "DELETE FROM " + quoteIdentifier(table.Name) + " WHERE " + where
			if _, err := tx.ExecContext(ctx, statement, args...); err != nil {
				return err
			}
		default:
			return fmt.Errorf("routine %q action %q is unsupported", name, action.Kind)
		}
	}
	return tx.Commit()
}

// compileRoutineWhere compiles a routine WHERE expression into a SQL fragment
// bound with engine placeholders, appending the resolved argument values to
// args. Columns compile to quoted identifiers; parameters and literals resolve
// to values via routineValue and are bound as placeholders so the routine never
// inlines caller data. position is the next placeholder index (1-based for
// PostgreSQL, ignored by SQLite which uses "?"). The returned position is the
// index following the last placeholder emitted.
func compileRoutineWhere(engine Engine, expression Expression, arguments map[string]Value, args *[]any, position int) (string, int, error) {
	emitPlaceholder := func(value Value) (string, int, error) {
		argument, err := driverValue(engine, value)
		if err != nil {
			return "", position, err
		}
		*args = append(*args, argument)
		return placeholder(engine, position), position + 1, nil
	}
	switch expression.Kind {
	case "parameter", "string", "integer", "decimal", "boolean":
		value, err := routineValue(expression, arguments)
		if err != nil {
			return "", position, err
		}
		return emitPlaceholder(value)
	case "null":
		return "NULL", position, nil
	case "column":
		if strings.Contains(expression.Value, ".") {
			return "", position, fmt.Errorf("routine WHERE column %q is unsupported", expression.Value)
		}
		return quoteIdentifier(expression.Value), position, nil
	case "and", "or", "eq", "ne", "lt", "lte", "gt", "gte", "like":
		if len(expression.Args) != 2 {
			return "", position, fmt.Errorf("routine WHERE expression %q requires two arguments", expression.Kind)
		}
		left, position, err := compileRoutineWhere(engine, expression.Args[0], arguments, args, position)
		if err != nil {
			return "", position, err
		}
		right, position, err := compileRoutineWhere(engine, expression.Args[1], arguments, args, position)
		if err != nil {
			return "", position, err
		}
		operator := map[string]string{
			"and": "AND", "or": "OR", "eq": "=", "ne": "<>", "lt": "<", "lte": "<=", "gt": ">", "gte": ">=",
		}[expression.Kind]
		if expression.Kind == "like" {
			operator = "LIKE"
			if engine == Postgres {
				operator = "ILIKE"
			}
		}
		return "(" + left + " " + operator + " " + right + ")", position, nil
	case "not":
		if len(expression.Args) != 1 {
			return "", position, fmt.Errorf("routine WHERE expression %q requires one argument", expression.Kind)
		}
		argument, position, err := compileRoutineWhere(engine, expression.Args[0], arguments, args, position)
		if err != nil {
			return "", position, err
		}
		return "(NOT " + argument + ")", position, nil
	case "is_null", "is_not_null":
		if len(expression.Args) != 1 {
			return "", position, fmt.Errorf("routine WHERE expression %q requires one argument", expression.Kind)
		}
		argument, position, err := compileRoutineWhere(engine, expression.Args[0], arguments, args, position)
		if err != nil {
			return "", position, err
		}
		if expression.Kind == "is_null" {
			return "(" + argument + " IS NULL)", position, nil
		}
		return "(" + argument + " IS NOT NULL)", position, nil
	default:
		return "", position, fmt.Errorf("routine WHERE expression %q is unsupported at runtime", expression.Kind)
	}
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
