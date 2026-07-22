package compat

import "fmt"

// Schema is the engine-neutral representation used before emitting SQLite or
// PostgreSQL DDL. Every engine-specific construct must be represented as an
// explicit capability rather than hidden in a raw SQL string.
type Schema struct {
	Tables   []Table   `json:"tables"`
	Indexes  []Index   `json:"indexes,omitempty"`
	Views    []View    `json:"views,omitempty"`
	Triggers []Trigger `json:"triggers,omitempty"`
	Routines []Routine `json:"routines,omitempty"`
}

type Table struct {
	Name        string       `json:"name"`
	Columns     []Column     `json:"columns"`
	Constraints []Constraint `json:"constraints,omitempty"`
}

type Column struct {
	Name     string      `json:"name"`
	Type     Type        `json:"type"`
	Nullable bool        `json:"nullable"`
	Default  *Expression `json:"default,omitempty"`
}

type Type struct {
	Family TypeFamily `json:"family"`
	// Arguments preserve details such as precision, scale, length or array
	// dimensions without coupling the canonical schema to SQL text.
	Arguments []int `json:"arguments,omitempty"`
}

type TypeFamily string

const (
	BooleanType   TypeFamily = "boolean"
	IntegerType   TypeFamily = "integer"
	DecimalType   TypeFamily = "decimal"
	FloatType     TypeFamily = "float"
	TextType      TypeFamily = "text"
	BinaryType    TypeFamily = "binary"
	DateType      TypeFamily = "date"
	TimestampType TypeFamily = "timestamp"
	JSONType      TypeFamily = "json"
	UUIDType      TypeFamily = "uuid"
	VectorType    TypeFamily = "vector"
)

// knownTypeFamilies is the single source of truth for the type families the
// canonical schema accepts. compileType (ddl.go) maps each family to a
// concrete SQL type per engine; an unknown family (a typo such as "nope") is
// rejected here at validation time with a clear, table/column-qualified error
// instead of surfacing later as an opaque compile error after both databases
// have already been contacted.
var knownTypeFamilies = map[TypeFamily]struct{}{
	BooleanType:   {},
	IntegerType:   {},
	DecimalType:   {},
	FloatType:     {},
	TextType:      {},
	BinaryType:    {},
	DateType:      {},
	TimestampType: {},
	JSONType:      {},
	UUIDType:      {},
	VectorType:    {},
}

// knownTypeFamily reports whether family is one of the supported type
// families. The empty family is intentionally not known; callers check it
// separately to produce a distinct "no type" error.
func knownTypeFamily(family TypeFamily) bool {
	_, ok := knownTypeFamilies[family]
	return ok
}

type Constraint struct {
	Kind       ConstraintKind `json:"kind"`
	Columns    []string       `json:"columns"`
	References *Reference     `json:"references,omitempty"`
	Expression *Expression    `json:"expression,omitempty"`
}

type ConstraintKind string

const (
	PrimaryKey ConstraintKind = "primary_key"
	UniqueKey  ConstraintKind = "unique"
	ForeignKey ConstraintKind = "foreign_key"
	Check      ConstraintKind = "check"
)

type Reference struct {
	Table    string            `json:"table"`
	Columns  []string          `json:"columns"`
	OnUpdate ReferentialAction `json:"on_update,omitempty"`
	OnDelete ReferentialAction `json:"on_delete,omitempty"`
}

type ReferentialAction string

const (
	NoAction   ReferentialAction = "no_action"
	Restrict   ReferentialAction = "restrict"
	Cascade    ReferentialAction = "cascade"
	SetNull    ReferentialAction = "set_null"
	SetDefault ReferentialAction = "set_default"
)

type Index struct {
	Name    string        `json:"name"`
	Table   string        `json:"table"`
	Unique  bool          `json:"unique,omitempty"`
	Columns []IndexColumn `json:"columns"`
	Where   *Expression   `json:"where,omitempty"`
}

type IndexColumn struct {
	Column     string `json:"column"`
	Descending bool   `json:"descending,omitempty"`
}

// Expression is an AST placeholder. Raw SQL is intentionally excluded from
// this layer because exact compatibility requires the expression to be parsed
// and compiled separately for each target dialect.
type Expression struct {
	Kind  string       `json:"kind"`
	Value string       `json:"value,omitempty"`
	Args  []Expression `json:"args,omitempty"`
}

type View struct {
	Name  string      `json:"name"`
	Query SelectQuery `json:"query"`
}

type SelectQuery struct {
	Distinct bool         `json:"distinct,omitempty"`
	Columns  []Projection `json:"columns"`
	From     TableSource  `json:"from"`
	Joins    []Join       `json:"joins,omitempty"`
	Where    *Expression  `json:"where,omitempty"`
	GroupBy  []Expression `json:"group_by,omitempty"`
	Having   *Expression  `json:"having,omitempty"`
	OrderBy  []Ordering   `json:"order_by,omitempty"`
	Limit    *int         `json:"limit,omitempty"`
	Offset   *int         `json:"offset,omitempty"`
}

type Projection struct {
	Expression Expression `json:"expression"`
	Alias      string     `json:"alias,omitempty"`
}

type TableSource struct {
	Table string `json:"table"`
	Alias string `json:"alias,omitempty"`
}

type Join struct {
	Kind  string      `json:"kind"`
	Table TableSource `json:"table"`
	On    Expression  `json:"on"`
}

type Ordering struct {
	Expression Expression `json:"expression"`
	Descending bool       `json:"descending,omitempty"`
}

type Trigger struct {
	Name    string          `json:"name"`
	Table   string          `json:"table"`
	Timing  string          `json:"timing"`
	Event   string          `json:"event"`
	When    *Expression     `json:"when,omitempty"`
	Actions []TriggerAction `json:"actions"`
}

type TriggerAction struct {
	Kind        string       `json:"kind"`
	Table       string       `json:"table"`
	Assignments []Assignment `json:"assignments,omitempty"`
	Where       *Expression  `json:"where,omitempty"`
}

type Assignment struct {
	Column string     `json:"column"`
	Value  Expression `json:"value"`
}

type Routine struct {
	Name       string             `json:"name"`
	Parameters []RoutineParameter `json:"parameters,omitempty"`
	Actions    []RoutineAction    `json:"actions"`
}

type RoutineParameter struct {
	Name string `json:"name"`
	Type Type   `json:"type"`
}

type RoutineAction struct {
	Kind        string       `json:"kind"`
	Table       string       `json:"table"`
	Assignments []Assignment `json:"assignments,omitempty"`
	Where       *Expression  `json:"where,omitempty"`
}

func (s Schema) Validate() error {
	tables := make(map[string]struct{}, len(s.Tables))
	tableColumns := make(map[string]map[string]struct{}, len(s.Tables))
	for _, table := range s.Tables {
		if err := validateTable(table, tables, tableColumns); err != nil {
			return err
		}
	}
	if err := validateIndexes(s.Indexes, tables, tableColumns); err != nil {
		return err
	}
	if err := validateViews(s.Views, tables); err != nil {
		return err
	}
	if err := validateTriggers(s.Triggers); err != nil {
		return err
	}
	if err := validateRoutines(s.Routines); err != nil {
		return err
	}
	return nil
}

// validateTable checks one table and registers it (and its column set) in the
// provided maps so that later index validation can resolve references.
func validateTable(table Table, tables map[string]struct{}, tableColumns map[string]map[string]struct{}) error {
	if table.Name == "" {
		return fmt.Errorf("table name is required")
	}
	if table.Name == schemaMetadataTable || table.Name == appliedChangesTable || table.Name == captureStateTable || table.Name == changeJournalTable {
		return fmt.Errorf("table name %q is reserved", table.Name)
	}
	if _, exists := tables[table.Name]; exists {
		return fmt.Errorf("duplicate table %q", table.Name)
	}
	tables[table.Name] = struct{}{}
	columns := make(map[string]struct{}, len(table.Columns))
	for _, column := range table.Columns {
		if err := validateTableColumn(table.Name, column, columns); err != nil {
			return err
		}
	}
	for _, constraint := range table.Constraints {
		if constraint.Kind != ForeignKey || constraint.References == nil {
			continue
		}
		if !validReferentialAction(constraint.References.OnUpdate) || !validReferentialAction(constraint.References.OnDelete) {
			return fmt.Errorf("foreign key on table %q has an invalid referential action", table.Name)
		}
	}
	tableColumns[table.Name] = columns
	return nil
}

// validateTableColumn checks a single column and, when valid, registers it in
// the column set for duplicate detection.
func validateTableColumn(tableName string, column Column, columns map[string]struct{}) error {
	if column.Name == "" {
		return fmt.Errorf("table %q has a column without a name", tableName)
	}
	if column.Type.Family == "" {
		return fmt.Errorf("column %q.%q has no type", tableName, column.Name)
	}
	if !knownTypeFamily(column.Type.Family) {
		return fmt.Errorf("column %q.%q has unsupported type family %q", tableName, column.Name, column.Type.Family)
	}
	if column.Type.Family == VectorType {
		// A vector is declared as vector(N); the single argument is the
		// fixed dimension and must be positive. Without it the canonical
		// type is meaningless and the DDL/value layers cannot compile.
		if len(column.Type.Arguments) != 1 || column.Type.Arguments[0] <= 0 {
			return fmt.Errorf("column %q.%q vector type requires a single positive dimension", tableName, column.Name)
		}
	}
	if _, exists := columns[column.Name]; exists {
		return fmt.Errorf("duplicate column %q.%q", tableName, column.Name)
	}
	columns[column.Name] = struct{}{}
	return nil
}

// validateIndexes checks name uniqueness and that every indexed column
// references a known table and column.
func validateIndexes(indexes []Index, tables map[string]struct{}, tableColumns map[string]map[string]struct{}) error {
	indexNames := make(map[string]struct{}, len(indexes))
	for _, index := range indexes {
		if index.Name == "" || index.Table == "" || len(index.Columns) == 0 {
			return fmt.Errorf("index name, table and columns are required")
		}
		if _, exists := indexNames[index.Name]; exists {
			return fmt.Errorf("duplicate index %q", index.Name)
		}
		indexNames[index.Name] = struct{}{}
		if _, exists := tables[index.Table]; !exists {
			return fmt.Errorf("index %q references unknown table %q", index.Name, index.Table)
		}
		for _, column := range index.Columns {
			if column.Column == "" {
				return fmt.Errorf("index %q has an empty column", index.Name)
			}
			if _, exists := tableColumns[index.Table][column.Column]; !exists {
				return fmt.Errorf("index %q references unknown column %q.%q", index.Name, index.Table, column.Column)
			}
		}
	}
	return nil
}

// validateViews checks name uniqueness, conflicts with tables, and that each
// view has a source table and projections.
func validateViews(views []View, tables map[string]struct{}) error {
	viewNames := make(map[string]struct{}, len(views))
	for _, view := range views {
		if view.Name == "" {
			return fmt.Errorf("view name is required")
		}
		if _, exists := tables[view.Name]; exists {
			return fmt.Errorf("view %q conflicts with a table", view.Name)
		}
		if _, exists := viewNames[view.Name]; exists {
			return fmt.Errorf("duplicate view %q", view.Name)
		}
		viewNames[view.Name] = struct{}{}
		if view.Query.From.Table == "" {
			return fmt.Errorf("view %q has no source table", view.Name)
		}
		if len(view.Query.Columns) == 0 {
			return fmt.Errorf("view %q has no projections", view.Name)
		}
	}
	return nil
}

// validateTriggers checks name uniqueness and that timing/event/actions are
// well formed.
func validateTriggers(triggers []Trigger) error {
	triggerNames := make(map[string]struct{}, len(triggers))
	for _, trigger := range triggers {
		if trigger.Name == "" || trigger.Table == "" {
			return fmt.Errorf("trigger name and table are required")
		}
		if _, exists := triggerNames[trigger.Name]; exists {
			return fmt.Errorf("duplicate trigger %q", trigger.Name)
		}
		triggerNames[trigger.Name] = struct{}{}
		switch trigger.Timing {
		case "before", "after":
		default:
			return fmt.Errorf("trigger %q has invalid timing %q", trigger.Name, trigger.Timing)
		}
		switch trigger.Event {
		case "insert", "update", "delete":
		default:
			return fmt.Errorf("trigger %q has invalid event %q", trigger.Name, trigger.Event)
		}
		if len(trigger.Actions) == 0 {
			return fmt.Errorf("trigger %q has no actions", trigger.Name)
		}
	}
	return nil
}

// validateRoutines checks name uniqueness, that actions are present, and that
// every parameter has a supported type family.
func validateRoutines(routines []Routine) error {
	routineNames := make(map[string]struct{}, len(routines))
	for _, routine := range routines {
		if routine.Name == "" || len(routine.Actions) == 0 {
			return fmt.Errorf("routine name and actions are required")
		}
		if _, exists := routineNames[routine.Name]; exists {
			return fmt.Errorf("duplicate routine %q", routine.Name)
		}
		routineNames[routine.Name] = struct{}{}
		parameters := make(map[string]struct{}, len(routine.Parameters))
		for _, parameter := range routine.Parameters {
			if parameter.Name == "" || parameter.Type.Family == "" {
				return fmt.Errorf("routine %q has an invalid parameter", routine.Name)
			}
			if !knownTypeFamily(parameter.Type.Family) {
				return fmt.Errorf("routine %q parameter %q has unsupported type family %q", routine.Name, parameter.Name, parameter.Type.Family)
			}
			if _, exists := parameters[parameter.Name]; exists {
				return fmt.Errorf("routine %q has duplicate parameter %q", routine.Name, parameter.Name)
			}
			parameters[parameter.Name] = struct{}{}
		}
	}
	return nil
}

func validReferentialAction(action ReferentialAction) bool {
	switch action {
	case "", NoAction, Restrict, Cascade, SetNull, SetDefault:
		return true
	default:
		return false
	}
}
