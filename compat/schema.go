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
)

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
	Assignments []Assignment `json:"assignments"`
}

func (s Schema) Validate() error {
	tables := make(map[string]struct{}, len(s.Tables))
	tableColumns := make(map[string]map[string]struct{}, len(s.Tables))
	for _, table := range s.Tables {
		if table.Name == "" {
			return fmt.Errorf("table name is required")
		}
		if table.Name == schemaMetadataTable || table.Name == appliedChangesTable {
			return fmt.Errorf("table name %q is reserved", table.Name)
		}
		if _, exists := tables[table.Name]; exists {
			return fmt.Errorf("duplicate table %q", table.Name)
		}
		tables[table.Name] = struct{}{}
		columns := make(map[string]struct{}, len(table.Columns))
		for _, column := range table.Columns {
			if column.Name == "" {
				return fmt.Errorf("table %q has a column without a name", table.Name)
			}
			if column.Type.Family == "" {
				return fmt.Errorf("column %q.%q has no type", table.Name, column.Name)
			}
			if _, exists := columns[column.Name]; exists {
				return fmt.Errorf("duplicate column %q.%q", table.Name, column.Name)
			}
			columns[column.Name] = struct{}{}
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
	}
	indexes := make(map[string]struct{}, len(s.Indexes))
	for _, index := range s.Indexes {
		if index.Name == "" || index.Table == "" || len(index.Columns) == 0 {
			return fmt.Errorf("index name, table and columns are required")
		}
		if _, exists := indexes[index.Name]; exists {
			return fmt.Errorf("duplicate index %q", index.Name)
		}
		indexes[index.Name] = struct{}{}
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
	views := make(map[string]struct{}, len(s.Views))
	for _, view := range s.Views {
		if view.Name == "" {
			return fmt.Errorf("view name is required")
		}
		if _, exists := tables[view.Name]; exists {
			return fmt.Errorf("view %q conflicts with a table", view.Name)
		}
		if _, exists := views[view.Name]; exists {
			return fmt.Errorf("duplicate view %q", view.Name)
		}
		views[view.Name] = struct{}{}
		if view.Query.From.Table == "" {
			return fmt.Errorf("view %q has no source table", view.Name)
		}
		if len(view.Query.Columns) == 0 {
			return fmt.Errorf("view %q has no projections", view.Name)
		}
	}
	triggers := make(map[string]struct{}, len(s.Triggers))
	for _, trigger := range s.Triggers {
		if trigger.Name == "" || trigger.Table == "" {
			return fmt.Errorf("trigger name and table are required")
		}
		if _, exists := triggers[trigger.Name]; exists {
			return fmt.Errorf("duplicate trigger %q", trigger.Name)
		}
		triggers[trigger.Name] = struct{}{}
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
	routines := make(map[string]struct{}, len(s.Routines))
	for _, routine := range s.Routines {
		if routine.Name == "" || len(routine.Actions) == 0 {
			return fmt.Errorf("routine name and actions are required")
		}
		if _, exists := routines[routine.Name]; exists {
			return fmt.Errorf("duplicate routine %q", routine.Name)
		}
		routines[routine.Name] = struct{}{}
		parameters := make(map[string]struct{}, len(routine.Parameters))
		for _, parameter := range routine.Parameters {
			if parameter.Name == "" || parameter.Type.Family == "" {
				return fmt.Errorf("routine %q has an invalid parameter", routine.Name)
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
