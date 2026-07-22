package compat

import (
	"fmt"
	"strings"
)

func parsePostgresCatalogRoutine(name, body, arguments, resultType, language, kind string) (Routine, error) {
	if kind != "p" && !strings.EqualFold(strings.TrimSpace(resultType), "void") {
		return Routine{}, fmt.Errorf("routine return type %q is outside the canonical command grammar", resultType)
	}
	if language != "plpgsql" && language != "sql" {
		return Routine{}, fmt.Errorf("routine language %q is unsupported", language)
	}
	routine := Routine{Name: name}
	parameterNames := map[string]struct{}{}
	if strings.TrimSpace(arguments) != "" {
		for _, argument := range splitTopLevelComma(arguments) {
			fields := strings.Fields(argument)
			if len(fields) > 0 && strings.EqualFold(fields[0], "IN") {
				fields = fields[1:]
			}
			if len(fields) < 2 || strings.Contains(strings.ToUpper(argument), " DEFAULT ") || strings.Contains(argument, "=") {
				return Routine{}, fmt.Errorf("routine argument %q is outside the canonical grammar", argument)
			}
			parameterName := unquoteCatalogIdentifier(fields[0])
			family, ok := canonicalPostgresRoutineType(strings.Join(fields[1:], " "))
			if !ok {
				return Routine{}, fmt.Errorf("routine argument type %q is unsupported", strings.Join(fields[1:], " "))
			}
			routine.Parameters = append(routine.Parameters, RoutineParameter{Name: parameterName, Type: Type{Family: family}})
			parameterNames[parameterName] = struct{}{}
		}
	}

	body = strings.TrimSpace(body)
	body = strings.TrimSuffix(body, ";")
	if strings.HasPrefix(strings.ToUpper(body), "BEGIN") {
		body = strings.TrimSpace(body[len("BEGIN"):])
		body = strings.TrimSuffix(strings.TrimSpace(body), ";")
		if !strings.HasSuffix(strings.ToUpper(body), "END") {
			return Routine{}, fmt.Errorf("routine block has no END")
		}
		body = strings.TrimSpace(body[:len(body)-len("END")])
	}
	actions, err := parseCatalogTriggerActions(body)
	if err != nil {
		return Routine{}, err
	}
	for _, triggerAction := range actions {
		action := RoutineAction{Kind: triggerAction.Kind, Table: triggerAction.Table}
		for _, assignment := range triggerAction.Assignments {
			expression, err := routineCatalogExpression(assignment.Value, parameterNames)
			if err != nil {
				return Routine{}, err
			}
			action.Assignments = append(action.Assignments, Assignment{Column: assignment.Column, Value: expression})
		}
		routine.Actions = append(routine.Actions, action)
	}
	return routine, nil
}

func routineCatalogExpression(expression Expression, parameters map[string]struct{}) (Expression, error) {
	if expression.Kind == "column" {
		if _, exists := parameters[expression.Value]; !exists || strings.Contains(expression.Value, ".") {
			return Expression{}, fmt.Errorf("routine expression %q is not a declared parameter", expression.Value)
		}
		return Expression{Kind: "parameter", Value: expression.Value}, nil
	}
	switch expression.Kind {
	case "null", "string", "integer", "decimal", "boolean":
		return expression, nil
	default:
		return Expression{}, fmt.Errorf("routine expression %q is outside the canonical grammar", expression.Kind)
	}
}

func canonicalPostgresRoutineType(value string) (TypeFamily, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "boolean":
		return BooleanType, true
	case "smallint", "integer", "bigint":
		return IntegerType, true
	case "numeric", "decimal":
		return DecimalType, true
	case "real", "double precision":
		return FloatType, true
	case "text", "character varying", "character":
		return TextType, true
	case "bytea":
		return BinaryType, true
	case "date":
		return DateType, true
	case "timestamp with time zone", "timestamp without time zone":
		return TimestampType, true
	case "json", "jsonb":
		return JSONType, true
	case "uuid":
		return UUIDType, true
	default:
		return "", false
	}
}
