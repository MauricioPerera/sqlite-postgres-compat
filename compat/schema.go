package compat

import "fmt"

// Schema is the engine-neutral representation used before emitting SQLite or
// PostgreSQL DDL. Every engine-specific construct must be represented as an
// explicit capability rather than hidden in a raw SQL string.
type Schema struct {
	Tables []Table `json:"tables"`
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
}

type ConstraintKind string

const (
	PrimaryKey ConstraintKind = "primary_key"
	UniqueKey  ConstraintKind = "unique"
	ForeignKey ConstraintKind = "foreign_key"
	Check      ConstraintKind = "check"
)

type Reference struct {
	Table   string   `json:"table"`
	Columns []string `json:"columns"`
}

// Expression is an AST placeholder. Raw SQL is intentionally excluded from
// this layer because exact compatibility requires the expression to be parsed
// and compiled separately for each target dialect.
type Expression struct {
	Kind  string `json:"kind"`
	Value string `json:"value,omitempty"`
}

func (s Schema) Validate() error {
	tables := make(map[string]struct{}, len(s.Tables))
	for _, table := range s.Tables {
		if table.Name == "" {
			return fmt.Errorf("table name is required")
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
	}
	return nil
}
