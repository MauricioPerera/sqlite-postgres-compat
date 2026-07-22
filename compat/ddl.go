package compat

import (
	"fmt"
	"strings"
)

// CompileDDL emits the target-specific physical schema from the canonical AST.
// Callers must audit transformed features before treating the result as an
// exact migration plan.
func CompileDDL(target Target, schema Schema) ([]string, error) {
	if err := target.Validate(); err != nil {
		return nil, err
	}
	if err := schema.Validate(); err != nil {
		return nil, err
	}

	statements := make([]string, 0, len(schema.Tables))
	for _, table := range schema.Tables {
		statement, err := compileTable(target.Engine, table)
		if err != nil {
			return nil, err
		}
		statements = append(statements, statement)
	}
	return statements, nil
}

func compileTable(engine Engine, table Table) (string, error) {
	parts := make([]string, 0, len(table.Columns)+len(table.Constraints))
	for _, column := range table.Columns {
		typeSQL, err := compileType(engine, column.Type)
		if err != nil {
			return "", fmt.Errorf("%s.%s: %w", table.Name, column.Name, err)
		}
		definition := quoteIdentifier(column.Name) + " " + typeSQL
		if !column.Nullable {
			definition += " NOT NULL"
		}
		if column.Default != nil {
			defaultSQL, err := compileExpression(engine, *column.Default)
			if err != nil {
				return "", fmt.Errorf("%s.%s default: %w", table.Name, column.Name, err)
			}
			definition += " DEFAULT " + defaultSQL
		}
		parts = append(parts, definition)
	}
	for _, constraint := range table.Constraints {
		definition, err := compileConstraint(constraint)
		if err != nil {
			return "", fmt.Errorf("%s: %w", table.Name, err)
		}
		parts = append(parts, definition)
	}
	return "CREATE TABLE " + quoteIdentifier(table.Name) + " (" + strings.Join(parts, ", ") + ")", nil
}

func compileType(engine Engine, typ Type) (string, error) {
	if len(typ.Arguments) > 2 {
		return "", fmt.Errorf("at most two type arguments are supported")
	}
	args := func() string {
		if len(typ.Arguments) == 0 {
			return ""
		}
		values := make([]string, len(typ.Arguments))
		for i, value := range typ.Arguments {
			values[i] = fmt.Sprint(value)
		}
		return "(" + strings.Join(values, ",") + ")"
	}

	if engine == SQLite {
		switch typ.Family {
		case BooleanType, IntegerType:
			return "INTEGER", nil
		case DecimalType:
			// SQLite REAL is IEEE-754 and cannot preserve arbitrary precision.
			// Decimal values use their canonical textual representation instead.
			return "TEXT", nil
		case FloatType:
			return "REAL", nil
		case TextType, DateType, TimestampType, UUIDType, JSONType:
			return "TEXT", nil
		case BinaryType:
			return "BLOB", nil
		}
	}
	if engine == Postgres {
		switch typ.Family {
		case BooleanType:
			return "BOOLEAN", nil
		case IntegerType:
			return "BIGINT", nil
		case DecimalType:
			return "NUMERIC" + args(), nil
		case FloatType:
			return "DOUBLE PRECISION", nil
		case TextType:
			return "TEXT", nil
		case BinaryType:
			return "BYTEA", nil
		case DateType:
			return "DATE", nil
		case TimestampType:
			// PostgreSQL timestamps have microsecond resolution. Canonical RFC3339
			// text preserves nanoseconds and offsets without rounding.
			return "TEXT", nil
		case JSONType:
			return "JSONB", nil
		case UUIDType:
			return "UUID", nil
		}
	}
	return "", fmt.Errorf("type family %q is not supported by %s", typ.Family, engine)
}

func compileConstraint(constraint Constraint) (string, error) {
	if len(constraint.Columns) == 0 && constraint.Kind != Check {
		return "", fmt.Errorf("constraint %q has no columns", constraint.Kind)
	}
	columns := quoteIdentifiers(constraint.Columns)
	switch constraint.Kind {
	case PrimaryKey:
		return "PRIMARY KEY (" + strings.Join(columns, ", ") + ")", nil
	case UniqueKey:
		return "UNIQUE (" + strings.Join(columns, ", ") + ")", nil
	case ForeignKey:
		if constraint.References == nil || constraint.References.Table == "" || len(constraint.References.Columns) == 0 {
			return "", fmt.Errorf("foreign key requires a reference")
		}
		return "FOREIGN KEY (" + strings.Join(columns, ", ") + ") REFERENCES " +
			quoteIdentifier(constraint.References.Table) + " (" + strings.Join(quoteIdentifiers(constraint.References.Columns), ", ") + ")", nil
	case Check:
		return "", fmt.Errorf("check constraints require an expression AST")
	default:
		return "", fmt.Errorf("unknown constraint %q", constraint.Kind)
	}
}

func compileExpression(_ Engine, expression Expression) (string, error) {
	switch expression.Kind {
	case "null":
		return "NULL", nil
	case "current_timestamp":
		return "CURRENT_TIMESTAMP", nil
	case "string":
		return "'" + strings.ReplaceAll(expression.Value, "'", "''") + "'", nil
	case "integer", "decimal":
		return expression.Value, nil
	default:
		return "", fmt.Errorf("expression %q has no compiler", expression.Kind)
	}
}

func quoteIdentifier(identifier string) string {
	return `"` + strings.ReplaceAll(identifier, `"`, `""`) + `"`
}

func quoteIdentifiers(identifiers []string) []string {
	quoted := make([]string, len(identifiers))
	for i, identifier := range identifiers {
		quoted[i] = quoteIdentifier(identifier)
	}
	return quoted
}
