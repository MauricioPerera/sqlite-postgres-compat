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

	statements := make([]string, 0, len(schema.Tables)+len(schema.Indexes)+len(schema.Views)+len(schema.Triggers)*2)
	for _, table := range schema.Tables {
		statement, err := compileTable(target.Engine, table)
		if err != nil {
			return nil, err
		}
		statements = append(statements, statement)
	}
	for _, index := range schema.Indexes {
		statement, err := compileIndex(target.Engine, index)
		if err != nil {
			return nil, fmt.Errorf("index %s: %w", index.Name, err)
		}
		statements = append(statements, statement)
	}
	for _, view := range schema.Views {
		query, err := compileSelect(target.Engine, view.Query)
		if err != nil {
			return nil, fmt.Errorf("view %s: %w", view.Name, err)
		}
		statements = append(statements, "CREATE VIEW "+quoteIdentifier(view.Name)+" AS "+query)
	}
	for _, trigger := range schema.Triggers {
		compiled, err := compileTrigger(target.Engine, trigger)
		if err != nil {
			return nil, fmt.Errorf("trigger %s: %w", trigger.Name, err)
		}
		statements = append(statements, compiled...)
	}
	return statements, nil
}

func compileIndex(engine Engine, index Index) (string, error) {
	columns := make([]string, len(index.Columns))
	for i, column := range index.Columns {
		columns[i] = quoteIdentifier(column.Column)
		if column.Descending {
			columns[i] += " DESC"
		} else {
			columns[i] += " ASC"
		}
	}
	statement := "CREATE "
	if index.Unique {
		statement += "UNIQUE "
	}
	statement += "INDEX " + quoteIdentifier(index.Name) + " ON " + quoteIdentifier(index.Table) + " (" + strings.Join(columns, ", ") + ")"
	if index.Where != nil {
		where, err := compileExpression(engine, *index.Where)
		if err != nil {
			return "", err
		}
		statement += " WHERE " + where
	}
	return statement, nil
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
		definition, err := compileConstraint(engine, constraint)
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
		case VectorType:
			// The default SQLite engine (modernc) has no native vector functions,
			// so the interoperable carrier is canonical text '[1,2,3]'. This was
			// validated against libSQL/sqld and pgvector in docs/reports/VECTOR-COMPAT-REPORT.md:
			// text crosses both engines, while the native F32_BLOB/bytea binary
			// route is not usable as a pgvector vector. TEXT preserves the
			// canonical value byte-for-byte without requiring a vector extension.
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
			// pgx returns a native DATE column as a time.Time, which the generic
			// time.Time branch in canonicalValue would fold to a TimestampValue
			// ("2020-01-01T00:00:00Z") and diverge from the SQLite TEXT source
			// ("2020-01-01"). TEXT preserves the exact canonical date value used by
			// both engines, mirroring the timestamp/json/uuid protective mapping.
			return "TEXT", nil
		case TimestampType:
			// PostgreSQL timestamps have microsecond resolution. Canonical RFC3339
			// text preserves nanoseconds and offsets without rounding.
			return "TEXT", nil
		case JSONType:
			// JSONB rewrites key order, whitespace, duplicate keys and number
			// representations. TEXT preserves the canonical payload byte-for-byte.
			return "TEXT", nil
		case UUIDType:
			// Native UUID normalizes textual representation. TEXT preserves the
			// exact canonical value used by both engines.
			return "TEXT", nil
		case VectorType:
			// Requires the pgvector extension in the destination. If it is not
			// installed the CREATE TABLE fails with a clear engine error, which is
			// acceptable: the canonical schema declares the capability explicitly
			// rather than silently degrading to text. The dimension is Arguments[0]
			// and is guaranteed to be a single positive value by Schema.Validate.
			return "vector" + args(), nil
		}
	}
	return "", fmt.Errorf("type family %q is not supported by %s", typ.Family, engine)
}

func compileConstraint(engine Engine, constraint Constraint) (string, error) {
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
		definition := "FOREIGN KEY (" + strings.Join(columns, ", ") + ") REFERENCES " +
			quoteIdentifier(constraint.References.Table) + " (" + strings.Join(quoteIdentifiers(constraint.References.Columns), ", ") + ")"
		if constraint.References.OnUpdate != "" && constraint.References.OnUpdate != NoAction {
			definition += " ON UPDATE " + compileReferentialAction(constraint.References.OnUpdate)
		}
		if constraint.References.OnDelete != "" && constraint.References.OnDelete != NoAction {
			definition += " ON DELETE " + compileReferentialAction(constraint.References.OnDelete)
		}
		return definition, nil
	case Check:
		if constraint.Expression == nil {
			return "", fmt.Errorf("check constraint requires an expression")
		}
		expression, err := compileExpression(engine, *constraint.Expression)
		if err != nil {
			return "", err
		}
		return "CHECK (" + expression + ")", nil
	default:
		return "", fmt.Errorf("unknown constraint %q", constraint.Kind)
	}
}

func compileReferentialAction(action ReferentialAction) string {
	switch action {
	case Restrict:
		return "RESTRICT"
	case Cascade:
		return "CASCADE"
	case SetNull:
		return "SET NULL"
	case SetDefault:
		return "SET DEFAULT"
	default:
		return "NO ACTION"
	}
}

// compileExpression compiles an expression outside a trigger context, where
// the identifiers new/old are ordinary columns and must be quoted like any
// other. Trigger bodies (WHEN clauses and action statements) call
// compileExpressionIn with inTrigger=true so that a leading new/old segment
// resolves to the trigger's NEW/OLD transition variable instead.
func compileExpression(engine Engine, expression Expression) (string, error) {
	return compileExpressionIn(engine, expression, false)
}

func compileExpressionIn(engine Engine, expression Expression, inTrigger bool) (string, error) {
	switch expression.Kind {
	case "null":
		return "NULL", nil
	case "current_timestamp":
		return "CURRENT_TIMESTAMP", nil
	case "string":
		return "'" + strings.ReplaceAll(expression.Value, "'", "''") + "'", nil
	case "integer", "decimal":
		return expression.Value, nil
	case "boolean":
		if expression.Value == "true" {
			return "TRUE", nil
		}
		if expression.Value == "false" {
			return "FALSE", nil
		}
		return "", fmt.Errorf("invalid boolean %q", expression.Value)
	case "column":
		return compileColumnExpression(expression.Value, inTrigger)
	case "star":
		return "*", nil
	case "and", "or", "eq", "ne", "lt", "lte", "gt", "gte", "add", "sub", "mul", "div", "like", "concat":
		return compileBinaryExpression(engine, expression, inTrigger)
	case "not", "is_null", "is_not_null":
		return compileUnaryExpression(engine, expression, inTrigger)
	case "count", "sum", "avg", "min", "max", "lower", "upper", "length", "abs", "trim":
		return compileScalarFunction(engine, expression, inTrigger)
	case "between", "not_between":
		return compileBetween(engine, expression, inTrigger)
	case "in", "not_in":
		return compileIn(engine, expression, inTrigger)
	case "case":
		return compileCase(engine, expression, inTrigger)
	case "nullif":
		if len(expression.Args) != 2 {
			return "", fmt.Errorf("function %q requires two arguments", expression.Kind)
		}
		compiled, err := compileExpressionArgs(engine, expression.Args, inTrigger)
		if err != nil {
			return "", err
		}
		return "NULLIF(" + compiled[0] + ", " + compiled[1] + ")", nil
	case "coalesce":
		if len(expression.Args) < 1 {
			return "", fmt.Errorf("function %q requires at least one argument", expression.Kind)
		}
		compiled, err := compileExpressionArgs(engine, expression.Args, inTrigger)
		if err != nil {
			return "", err
		}
		return "COALESCE(" + strings.Join(compiled, ", ") + ")", nil
	case "replace":
		if len(expression.Args) != 3 {
			return "", fmt.Errorf("function %q requires three arguments", expression.Kind)
		}
		compiled, err := compileExpressionArgs(engine, expression.Args, inTrigger)
		if err != nil {
			return "", err
		}
		return "REPLACE(" + strings.Join(compiled, ", ") + ")", nil
	default:
		return "", fmt.Errorf("expression %q has no compiler", expression.Kind)
	}
}

// compileColumnExpression compiles a column reference, splitting on dots and
// quoting each segment.
//
// Only inside a trigger does a leading new/old segment denote the NEW/OLD
// transition variable, emitted unquoted and uppercased as the dialect
// expects. Everywhere else (CHECK, index WHERE, column DEFAULT, views) new/old
// are ordinary column names and must be quoted, so a column literally named
// "New" is not folded to the nonexistent "new" by PostgreSQL's identifier
// casing rules.
func compileColumnExpression(value string, inTrigger bool) (string, error) {
	parts := strings.Split(value, ".")
	for i, part := range parts {
		if part == "" {
			return "", fmt.Errorf("invalid column %q", value)
		}
		if inTrigger && i == 0 && (strings.EqualFold(part, "new") || strings.EqualFold(part, "old")) {
			parts[i] = strings.ToUpper(part)
		} else {
			parts[i] = quoteIdentifier(part)
		}
	}
	return strings.Join(parts, "."), nil
}

// compileBinaryExpression compiles a two-argument operator expression.
//
// SQLite's LIKE is case-insensitive (ASCII) by default, while Postgres's LIKE
// is case-sensitive. Compile to ILIKE on Postgres to preserve the SQLite
// semantics. Note: ILIKE is case-insensitive across the full Unicode range,
// whereas SQLite only folds ASCII — this is the standard pragmatic mapping,
// accepted as a known trade-off.
func compileBinaryExpression(engine Engine, expression Expression, inTrigger bool) (string, error) {
	if len(expression.Args) != 2 {
		return "", fmt.Errorf("expression %q requires two arguments", expression.Kind)
	}
	left, err := compileExpressionIn(engine, expression.Args[0], inTrigger)
	if err != nil {
		return "", err
	}
	right, err := compileExpressionIn(engine, expression.Args[1], inTrigger)
	if err != nil {
		return "", err
	}
	operators := map[string]string{
		"and": "AND", "or": "OR", "eq": "=", "ne": "<>", "lt": "<", "lte": "<=", "gt": ">", "gte": ">=",
		"add": "+", "sub": "-", "mul": "*", "div": "/", "concat": "||",
	}
	if expression.Kind == "like" {
		operator := "LIKE"
		if engine == Postgres {
			operator = "ILIKE"
		}
		return "(" + left + " " + operator + " " + right + ")", nil
	}
	return "(" + left + " " + operators[expression.Kind] + " " + right + ")", nil
}

// compileUnaryExpression compiles a one-argument prefix expression (NOT,
// IS NULL, IS NOT NULL).
func compileUnaryExpression(engine Engine, expression Expression, inTrigger bool) (string, error) {
	if len(expression.Args) != 1 {
		return "", fmt.Errorf("expression %q requires one argument", expression.Kind)
	}
	argument, err := compileExpressionIn(engine, expression.Args[0], inTrigger)
	if err != nil {
		return "", err
	}
	switch expression.Kind {
	case "not":
		return "(NOT " + argument + ")", nil
	case "is_null":
		return "(" + argument + " IS NULL)", nil
	default:
		return "(" + argument + " IS NOT NULL)", nil
	}
}

// compileScalarFunction compiles a single-argument scalar function such as
// COUNT or LOWER, rendering the kind uppercased with its compiled argument.
func compileScalarFunction(engine Engine, expression Expression, inTrigger bool) (string, error) {
	if len(expression.Args) != 1 {
		return "", fmt.Errorf("function %q requires one argument", expression.Kind)
	}
	argument, err := compileExpressionIn(engine, expression.Args[0], inTrigger)
	if err != nil {
		return "", err
	}
	return strings.ToUpper(expression.Kind) + "(" + argument + ")", nil
}

// compileBetween compiles a BETWEEN / NOT BETWEEN predicate. Both engines
// implement the SQL-standard, inclusive form (low <= operand <= high) with
// identical evaluation, so the native operator is emitted on both rather than
// desugaring to a pair of comparisons — the output stays close to the source
// intent while remaining exactly equivalent. Args are [operand, low, high].
func compileBetween(engine Engine, expression Expression, inTrigger bool) (string, error) {
	if len(expression.Args) != 3 {
		return "", fmt.Errorf("expression %q requires three arguments", expression.Kind)
	}
	compiled, err := compileExpressionArgs(engine, expression.Args, inTrigger)
	if err != nil {
		return "", err
	}
	operator := "BETWEEN"
	if expression.Kind == "not_between" {
		operator = "NOT BETWEEN"
	}
	return "(" + compiled[0] + " " + operator + " " + compiled[1] + " AND " + compiled[2] + ")", nil
}

// compileIn compiles an IN / NOT IN predicate over an explicit value list. The
// membership test — including three-valued logic when a value or the operand is
// NULL — is identical in SQLite and PostgreSQL. Args are [operand, v1, v2, ...]
// with at least one value.
func compileIn(engine Engine, expression Expression, inTrigger bool) (string, error) {
	if len(expression.Args) < 2 {
		return "", fmt.Errorf("expression %q requires an operand and at least one value", expression.Kind)
	}
	compiled, err := compileExpressionArgs(engine, expression.Args, inTrigger)
	if err != nil {
		return "", err
	}
	operator := "IN"
	if expression.Kind == "not_in" {
		operator = "NOT IN"
	}
	return "(" + compiled[0] + " " + operator + " (" + strings.Join(compiled[1:], ", ") + "))", nil
}

// compileCase compiles a searched CASE expression. Args are laid out as pairs
// [cond1, value1, cond2, value2, ...] with an optional trailing ELSE value: an
// odd Args length carries an ELSE, an even length does not. The native CASE
// syntax and its first-match evaluation are identical in both engines. The
// explicit END makes the construct self-delimiting, so no extra wrapping parens
// are needed for correct precedence when it is nested in a larger expression.
func compileCase(engine Engine, expression Expression, inTrigger bool) (string, error) {
	if len(expression.Args) < 2 {
		return "", fmt.Errorf("expression %q requires at least one WHEN/THEN pair", expression.Kind)
	}
	compiled, err := compileExpressionArgs(engine, expression.Args, inTrigger)
	if err != nil {
		return "", err
	}
	hasElse := len(compiled)%2 == 1
	pairEnd := len(compiled)
	if hasElse {
		pairEnd--
	}
	var builder strings.Builder
	builder.WriteString("CASE")
	for i := 0; i < pairEnd; i += 2 {
		builder.WriteString(" WHEN " + compiled[i] + " THEN " + compiled[i+1])
	}
	if hasElse {
		builder.WriteString(" ELSE " + compiled[len(compiled)-1])
	}
	builder.WriteString(" END")
	return builder.String(), nil
}

// compileExpressionArgs compiles each argument expression, returning the
// compiled fragments in order. Variadic scalar functions share it. The
// inTrigger flag is propagated so a nested new/old column resolves to the
// trigger transition variable when the enclosing expression is a trigger body.
func compileExpressionArgs(engine Engine, args []Expression, inTrigger bool) ([]string, error) {
	compiled := make([]string, len(args))
	for i, argument := range args {
		value, err := compileExpressionIn(engine, argument, inTrigger)
		if err != nil {
			return nil, err
		}
		compiled[i] = value
	}
	return compiled, nil
}

// compileSelect emits a single SELECT or a left-associative compound (set
// operation) chain. The whole-compound ORDER BY / LIMIT / OFFSET live on the
// leading query and are emitted once, after every branch.
func compileSelect(engine Engine, query SelectQuery) (string, error) {
	statement, err := compileSelectCore(engine, query)
	if err != nil {
		return "", err
	}
	if len(query.Compounds) > 0 {
		if err := validateCompoundChain(query.Compounds); err != nil {
			return "", err
		}
		for _, compound := range query.Compounds {
			operator, err := compoundOperatorSQL(compound.Operator)
			if err != nil {
				return "", err
			}
			if branchHasTrailingClauses(compound.Query) || len(compound.Query.Compounds) > 0 {
				return "", fmt.Errorf("compound branch must not carry ORDER BY/LIMIT/OFFSET or nested compounds")
			}
			branch, err := compileSelectCore(engine, compound.Query)
			if err != nil {
				return "", err
			}
			statement += " " + operator + " " + branch
		}
	}
	trailing, err := compileSelectTrailing(engine, query)
	if err != nil {
		return "", err
	}
	return statement + trailing, nil
}

// validateCompoundChain applies the same INTERSECT-mixing rule the parser uses,
// so a hand-built AST cannot smuggle a chain whose grouping would diverge
// between SQLite and PostgreSQL past compilation.
func validateCompoundChain(compounds []CompoundSelect) error {
	operators := make([]string, len(compounds))
	for i, compound := range compounds {
		operators[i] = compound.Operator
	}
	return rejectMixedIntersectChain(operators)
}

// compoundOperatorSQL maps a canonical set-operator name to the SQL keyword,
// which is identical for SQLite and PostgreSQL.
func compoundOperatorSQL(operator string) (string, error) {
	switch operator {
	case "union":
		return "UNION", nil
	case "union_all":
		return "UNION ALL", nil
	case "intersect":
		return "INTERSECT", nil
	case "except":
		return "EXCEPT", nil
	default:
		return "", fmt.Errorf("unsupported compound operator %q", operator)
	}
}

// compileSelectCore emits the SELECT ... FROM ... [JOIN ...] [WHERE] [GROUP BY]
// [HAVING] portion of a query, without the trailing ORDER BY / LIMIT / OFFSET,
// so it can be reused for every branch of a compound.
func compileSelectCore(engine Engine, query SelectQuery) (string, error) {
	if len(query.Columns) == 0 || query.From.Table == "" {
		return "", fmt.Errorf("select requires projections and a source table")
	}
	projections := make([]string, len(query.Columns))
	for i, projection := range query.Columns {
		expression, err := compileExpression(engine, projection.Expression)
		if err != nil {
			return "", err
		}
		if projection.Alias != "" {
			expression += " AS " + quoteIdentifier(projection.Alias)
		}
		projections[i] = expression
	}
	statement := "SELECT "
	if query.Distinct {
		statement += "DISTINCT "
	}
	statement += strings.Join(projections, ", ") + " FROM " + compileTableSource(query.From)
	for _, join := range query.Joins {
		kind := strings.ToUpper(join.Kind)
		switch kind {
		case "INNER", "LEFT", "CROSS":
		default:
			return "", fmt.Errorf("unsupported join kind %q", join.Kind)
		}
		statement += " " + kind + " JOIN " + compileTableSource(join.Table)
		if kind != "CROSS" {
			on, err := compileExpression(engine, join.On)
			if err != nil {
				return "", err
			}
			statement += " ON " + on
		}
	}
	if query.Where != nil {
		where, err := compileExpression(engine, *query.Where)
		if err != nil {
			return "", err
		}
		statement += " WHERE " + where
	}
	if len(query.GroupBy) > 0 {
		group := make([]string, len(query.GroupBy))
		for i, expression := range query.GroupBy {
			compiled, err := compileExpression(engine, expression)
			if err != nil {
				return "", err
			}
			group[i] = compiled
		}
		statement += " GROUP BY " + strings.Join(group, ", ")
	}
	if query.Having != nil {
		having, err := compileExpression(engine, *query.Having)
		if err != nil {
			return "", err
		}
		statement += " HAVING " + having
	}
	return statement, nil
}

// compileSelectTrailing emits the ORDER BY / LIMIT / OFFSET clauses that apply
// to a whole query (or whole compound). It returns a leading-space-prefixed
// string so callers append it directly to the core statement.
func compileSelectTrailing(engine Engine, query SelectQuery) (string, error) {
	trailing := ""
	if len(query.OrderBy) > 0 {
		order := make([]string, len(query.OrderBy))
		for i, ordering := range query.OrderBy {
			compiled, err := compileExpression(engine, ordering.Expression)
			if err != nil {
				return "", err
			}
			if ordering.Descending {
				compiled += " DESC"
			} else {
				compiled += " ASC"
			}
			order[i] = compiled
		}
		trailing += " ORDER BY " + strings.Join(order, ", ")
	}
	if query.Limit != nil {
		if *query.Limit < 0 {
			return "", fmt.Errorf("limit must be non-negative")
		}
		trailing += " LIMIT " + fmt.Sprint(*query.Limit)
	}
	if query.Offset != nil {
		if *query.Offset < 0 {
			return "", fmt.Errorf("offset must be non-negative")
		}
		trailing += " OFFSET " + fmt.Sprint(*query.Offset)
	}
	return trailing, nil
}

func compileTableSource(source TableSource) string {
	compiled := quoteIdentifier(source.Table)
	if source.Alias != "" {
		compiled += " AS " + quoteIdentifier(source.Alias)
	}
	return compiled
}

func compileTrigger(engine Engine, trigger Trigger) ([]string, error) {
	actions := make([]string, len(trigger.Actions))
	for i, action := range trigger.Actions {
		compiled, err := compileTriggerAction(engine, action)
		if err != nil {
			return nil, err
		}
		actions[i] = compiled + ";"
	}
	timing := strings.ToUpper(trigger.Timing)
	event := strings.ToUpper(trigger.Event)

	if engine == SQLite {
		statement := "CREATE TRIGGER " + quoteIdentifier(trigger.Name) + " " + timing + " " + event + " ON " + quoteIdentifier(trigger.Table) + " FOR EACH ROW"
		if trigger.When != nil {
			condition, err := compileExpressionIn(engine, *trigger.When, true)
			if err != nil {
				return nil, err
			}
			statement += " WHEN " + condition
		}
		statement += " BEGIN " + strings.Join(actions, " ") + " END"
		return []string{statement}, nil
	}

	functionName := "__compat_trigger_" + trigger.Name
	returnValue := "NEW"
	if trigger.Event == "delete" {
		returnValue = "OLD"
	}
	body := "BEGIN " + strings.Join(actions, " ") + " RETURN " + returnValue + "; END"
	function := "CREATE FUNCTION " + quoteIdentifier(functionName) + "() RETURNS TRIGGER LANGUAGE plpgsql AS '" + strings.ReplaceAll(body, "'", "''") + "'"
	statement := "CREATE TRIGGER " + quoteIdentifier(trigger.Name) + " " + timing + " " + event + " ON " + quoteIdentifier(trigger.Table) + " FOR EACH ROW"
	if trigger.When != nil {
		condition, err := compileExpressionIn(engine, *trigger.When, true)
		if err != nil {
			return nil, err
		}
		statement += " WHEN (" + condition + ")"
	}
	statement += " EXECUTE FUNCTION " + quoteIdentifier(functionName) + "()"
	return []string{function, statement}, nil
}

func compileTriggerAction(engine Engine, action TriggerAction) (string, error) {
	if action.Table == "" {
		return "", fmt.Errorf("trigger action table is required")
	}
	compileAssignments := func() ([]string, []string, error) {
		columns := make([]string, len(action.Assignments))
		values := make([]string, len(action.Assignments))
		for i, assignment := range action.Assignments {
			if assignment.Column == "" {
				return nil, nil, fmt.Errorf("trigger assignment column is required")
			}
			value, err := compileExpressionIn(engine, assignment.Value, true)
			if err != nil {
				return nil, nil, err
			}
			columns[i] = quoteIdentifier(assignment.Column)
			values[i] = value
		}
		return columns, values, nil
	}
	switch action.Kind {
	case "insert":
		if len(action.Assignments) == 0 {
			return "", fmt.Errorf("insert trigger action requires assignments")
		}
		columns, values, err := compileAssignments()
		if err != nil {
			return "", err
		}
		return "INSERT INTO " + quoteIdentifier(action.Table) + " (" + strings.Join(columns, ", ") + ") VALUES (" + strings.Join(values, ", ") + ")", nil
	case "update":
		if len(action.Assignments) == 0 || action.Where == nil {
			return "", fmt.Errorf("update trigger action requires assignments and predicate")
		}
		columns, values, err := compileAssignments()
		if err != nil {
			return "", err
		}
		sets := make([]string, len(columns))
		for i := range columns {
			sets[i] = columns[i] + " = " + values[i]
		}
		where, err := compileExpressionIn(engine, *action.Where, true)
		if err != nil {
			return "", err
		}
		return "UPDATE " + quoteIdentifier(action.Table) + " SET " + strings.Join(sets, ", ") + " WHERE " + where, nil
	case "delete":
		if action.Where == nil {
			return "", fmt.Errorf("delete trigger action requires predicate")
		}
		where, err := compileExpressionIn(engine, *action.Where, true)
		if err != nil {
			return "", err
		}
		return "DELETE FROM " + quoteIdentifier(action.Table) + " WHERE " + where, nil
	default:
		return "", fmt.Errorf("unsupported trigger action %q", action.Kind)
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
