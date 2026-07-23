package compat

import (
	"fmt"
	"reflect"
)

// Schema is the engine-neutral representation used before emitting SQLite or
// PostgreSQL DDL. Every engine-specific construct must be represented as an
// explicit capability rather than hidden in a raw SQL string.
type Schema struct {
	Tables  []Table `json:"tables"`
	Indexes []Index `json:"indexes,omitempty"`
	// Domains is the list of named SQL domains (a base type plus an optional
	// CHECK, NOT NULL and DEFAULT). It is additive and omitempty, so a schema
	// without domains stays byte-identical in the canonical JSON and in every
	// emitted statement. PostgreSQL has native domains (CREATE DOMAIN emitted
	// before the tables); SQLite has none, so a column that references a domain is
	// inlined with the domain's base type + CHECK (+ NOT NULL/DEFAULT). See Domain.
	Domains  []Domain  `json:"domains,omitempty"`
	Views    []View    `json:"views,omitempty"`
	Triggers []Trigger `json:"triggers,omitempty"`
	Routines []Routine `json:"routines,omitempty"`
}

// Domain is a named SQL domain: a base Type plus an optional CHECK constraint,
// NOT NULL flag and DEFAULT expression. It is asymmetric across the two engines
// by nature:
//
//   - PostgreSQL emits a native CREATE DOMAIN and columns reference it by name.
//   - SQLite has no domains, so every column that references the domain is
//     compiled INLINE with the domain's base type and the same CHECK/NOT
//     NULL/DEFAULT — semantically equivalent (same base type, same constraint),
//     the only portable rendering.
//
// The CHECK expression refers to the value under test with the placeholder node
// Expression{Kind:"domain_value"}: it compiles to the SQL keyword VALUE on
// PostgreSQL (the domain's own value) and to the referencing column's name when
// inlined on SQLite. Grammar validity of the CHECK is enforced at compile time
// by the same compileExpression path as any other expression, so an
// out-of-grammar CHECK fails with an explicit error.
//
// A domain is a schema-level constraint, not data: the stored value is identical
// whether the constraint is enforced by a native PG domain or by an inline SQLite
// CHECK, so data fidelity is preserved. Only the canonical path (schema created by
// this layer, kept in __compat_schema) round-trips the domain exactly; external
// inspection cannot rebuild a domain that never physically existed as such (see
// docs/COMPATIBILITY.md).
type Domain struct {
	Name    string      `json:"name"`
	Type    Type        `json:"type"`
	Check   *Expression `json:"check,omitempty"`
	NotNull bool        `json:"not_null,omitempty"`
	Default *Expression `json:"default,omitempty"`
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
	// DomainRef, when set, names a Schema.Domains entry this column takes its
	// CHECK/NOT NULL/DEFAULT from. It is additive and omitempty, so a column
	// without it stays byte-identical. The column MUST still carry Type equal to
	// the domain's base type: the data chain (export/import canonicalization) keys
	// off Column.Type.Family, and both engines physically store the domain value
	// as its base type, so the redundant Type keeps that path unchanged and keeps
	// PG(native domain) and SQLite(inlined) storing identical values. A
	// domain-referencing column must be otherwise neutral — Nullable true, no own
	// Default, no Generated — so the domain is the single source of the constraint
	// (validated in Schema.Validate). On PostgreSQL the column's SQL type is the
	// domain name; on SQLite it is the base type with the domain's CHECK/NOT
	// NULL/DEFAULT inlined.
	DomainRef string `json:"domain,omitempty"`
	// Generated, when set, makes this a STORED generated column whose value is
	// computed from Expression rather than supplied on INSERT/UPDATE. It is an
	// additive, omitempty field: a column without it stays byte-identical in the
	// canonical JSON and in every emitted statement. See GeneratedColumn.
	Generated *GeneratedColumn `json:"generated,omitempty"`
}

// GeneratedColumn describes a STORED generated column:
// `col TYPE GENERATED ALWAYS AS (<expression>) STORED`. Only STORED is
// supported because it is computed and physically stored identically by SQLite
// (>= 3.31) and PostgreSQL (>= 12) with this exact syntax; the value is
// recomputed on the destination from the same deterministic expression, which
// is the equivalence proof. VIRTUAL is deliberately not supported (PostgreSQL
// cannot express it), so Stored must be true — a false Stored is rejected by
// Schema.Validate rather than silently emitting divergent DDL. Expression uses
// the canonical grammar (compat/sqlparse.go, parseCatalogExpression) and is
// compiled by the same compileExpression path as any other expression, so an
// out-of-grammar expression fails at compile time with an explicit error. A
// generated column cannot also carry a Default and cannot be part of a
// canonical primary key (both engines restrict this).
type GeneratedColumn struct {
	Expression Expression `json:"expression"`
	Stored     bool       `json:"stored"`
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

// IndexColumn is one key of an index. Ordinarily the key is a plain column
// (Column). When Expression is set the key is that catalog expression (Section
// 3 grammar) compiled inside parentheses — an expression index, supported by
// SQLite (>= 3.9) and PostgreSQL with identical `(expr)` key syntax — and
// Column is left empty. Descending applies to either form. Expression is
// additive and omitted from JSON when nil, so a plain-column index stays
// byte-identical in canonical metadata and in every emitted statement.
type IndexColumn struct {
	Column     string      `json:"column,omitempty"`
	Descending bool        `json:"descending,omitempty"`
	Expression *Expression `json:"expression,omitempty"`
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
	// With is the list of non-recursive common table expressions (CTEs) that
	// precede this query: WITH name AS (SELECT ...), ... <query>. Each CTE query
	// is itself a SelectQuery in the same bounded grammar. WITH RECURSIVE is
	// deliberately not modeled and is rejected at parse time, because its
	// termination and row-ordering semantics are hard to guarantee byte-identical
	// between SQLite and PostgreSQL. Absent CTEs leave the query byte-identical,
	// and the field is omitted from JSON so existing view snapshots do not change.
	With     []CommonTableExpr `json:"with,omitempty"`
	Distinct bool              `json:"distinct,omitempty"`
	Columns  []Projection      `json:"columns"`
	From     TableSource       `json:"from"`
	Joins    []Join            `json:"joins,omitempty"`
	Where    *Expression       `json:"where,omitempty"`
	GroupBy  []Expression      `json:"group_by,omitempty"`
	Having   *Expression       `json:"having,omitempty"`
	// Compounds is the left-associative chain of set operations applied after
	// this (the leading) SELECT: q0 op1 q1 op2 q2 ... The trailing OrderBy,
	// Limit and Offset below apply to the whole compound, not to the last
	// branch, so each CompoundSelect.Query carries none of them. Absent
	// compounds leave the single-SELECT behavior byte-identical, and the field
	// is omitted from JSON so existing view snapshots do not change.
	Compounds []CompoundSelect `json:"compounds,omitempty"`
	OrderBy   []Ordering       `json:"order_by,omitempty"`
	Limit     *int             `json:"limit,omitempty"`
	Offset    *int             `json:"offset,omitempty"`
}

// CompoundSelect is one branch of a compound (set-operation) SELECT: the set
// operator that joins it to everything to its left, plus the branch query. The
// operator is one of "union", "union_all", "intersect", "except"; all four have
// identical set semantics in SQLite and PostgreSQL. A chain that mixes
// "intersect" with any other operator is rejected, because INTERSECT binds
// more tightly than UNION/EXCEPT in PostgreSQL but has equal (left-associative)
// precedence in SQLite, so a flat left-associative chain would group
// differently between the two engines.
type CompoundSelect struct {
	Operator string      `json:"operator"`
	Query    SelectQuery `json:"query"`
}

// CommonTableExpr is one named, non-recursive common table expression: the CTE
// name and the query it binds. Referenced from a FROM clause by its name like an
// ordinary table, a CTE has identical materialized-result semantics in SQLite and
// PostgreSQL. A CTE name shadows a real table of the same name for the duration of
// the query in both engines (standard SQL), so no special resolution is needed.
type CommonTableExpr struct {
	Name  string      `json:"name"`
	Query SelectQuery `json:"query"`
}

type Projection struct {
	Expression Expression `json:"expression"`
	Alias      string     `json:"alias,omitempty"`
}

// TableSource is a FROM/JOIN source: either a named table (Table) or a derived
// table (Subquery). Exactly one of Table or Subquery is set. A derived table is
// a full SelectQuery evaluated as a table; both engines give it identical
// results (it cannot be correlated with the enclosing query in standard SQL). A
// derived table requires an Alias. Subquery is omitted from JSON when absent, so
// existing table-source snapshots do not change.
type TableSource struct {
	Table    string       `json:"table,omitempty"`
	Alias    string       `json:"alias,omitempty"`
	Subquery *SelectQuery `json:"subquery,omitempty"`
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
	domains, err := validateDomains(s.Domains)
	if err != nil {
		return err
	}
	tables := make(map[string]struct{}, len(s.Tables))
	tableColumns := make(map[string]map[string]struct{}, len(s.Tables))
	for _, table := range s.Tables {
		if err := validateTable(table, domains, tables, tableColumns); err != nil {
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

// validateDomains checks that every domain has a name, is unique, and carries a
// supported base type family (the same base-type rules as a column). The CHECK
// expression's grammar is enforced at compile time by compileExpression — the
// same deferral used for generated columns and expression indexes — so an
// out-of-grammar CHECK is rejected by CompileDDL with a clear error. It returns
// the domain map so table/column validation can resolve DomainRef references.
func validateDomains(domains []Domain) (map[string]Domain, error) {
	resolved := make(map[string]Domain, len(domains))
	for _, domain := range domains {
		if domain.Name == "" {
			return nil, fmt.Errorf("domain name is required")
		}
		if _, exists := resolved[domain.Name]; exists {
			return nil, fmt.Errorf("duplicate domain %q", domain.Name)
		}
		if domain.Type.Family == "" {
			return nil, fmt.Errorf("domain %q has no base type", domain.Name)
		}
		if !knownTypeFamily(domain.Type.Family) {
			return nil, fmt.Errorf("domain %q has unsupported base type family %q", domain.Name, domain.Type.Family)
		}
		if domain.Type.Family == VectorType {
			if len(domain.Type.Arguments) != 1 || domain.Type.Arguments[0] <= 0 {
				return nil, fmt.Errorf("domain %q vector base type requires a single positive dimension", domain.Name)
			}
		}
		resolved[domain.Name] = domain
	}
	return resolved, nil
}

// validateTable checks one table and registers it (and its column set) in the
// provided maps so that later index validation can resolve references.
func validateTable(table Table, domains map[string]Domain, tables map[string]struct{}, tableColumns map[string]map[string]struct{}) error {
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
		if err := validateTableColumn(table.Name, column, domains, columns); err != nil {
			return err
		}
	}
	generated := make(map[string]struct{})
	for _, column := range table.Columns {
		if column.Generated != nil {
			generated[column.Name] = struct{}{}
		}
	}
	for _, constraint := range table.Constraints {
		if constraint.Kind == PrimaryKey {
			// A generated column cannot be part of a primary key in either engine
			// (SQLite: "STORED/VIRTUAL columns may not be part of the PRIMARY KEY";
			// PostgreSQL rejects a generated column in a primary key). Reject it here
			// rather than emitting DDL that one or both engines refuse.
			for _, name := range constraint.Columns {
				if _, ok := generated[name]; ok {
					return fmt.Errorf("table %q primary key includes generated column %q", table.Name, name)
				}
			}
		}
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
func validateTableColumn(tableName string, column Column, domains map[string]Domain, columns map[string]struct{}) error {
	if column.Name == "" {
		return fmt.Errorf("table %q has a column without a name", tableName)
	}
	if column.Type.Family == "" {
		return fmt.Errorf("column %q.%q has no type", tableName, column.Name)
	}
	if !knownTypeFamily(column.Type.Family) {
		return fmt.Errorf("column %q.%q has unsupported type family %q", tableName, column.Name, column.Type.Family)
	}
	if column.DomainRef != "" {
		// A domain-referencing column must resolve to a declared domain and be
		// otherwise neutral: its Type must equal the domain's base type (the data
		// chain keys off Column.Type, and both engines store the value as that base
		// type), it must not add its own NOT NULL/DEFAULT/GENERATED (the domain is
		// the single source of the constraint), so PG(native) and SQLite(inlined)
		// enforce exactly the same thing on identical data.
		domain, ok := domains[column.DomainRef]
		if !ok {
			return fmt.Errorf("column %q.%q references unknown domain %q", tableName, column.Name, column.DomainRef)
		}
		if !reflect.DeepEqual(column.Type, domain.Type) {
			return fmt.Errorf("column %q.%q type must match domain %q base type", tableName, column.Name, column.DomainRef)
		}
		if !column.Nullable {
			return fmt.Errorf("column %q.%q must be nullable; NOT NULL is defined by domain %q", tableName, column.Name, column.DomainRef)
		}
		if column.Default != nil {
			return fmt.Errorf("column %q.%q cannot have its own default; DEFAULT is defined by domain %q", tableName, column.Name, column.DomainRef)
		}
		if column.Generated != nil {
			return fmt.Errorf("column %q.%q cannot be generated and reference domain %q", tableName, column.Name, column.DomainRef)
		}
	}
	if column.Type.Family == VectorType {
		// A vector is declared as vector(N); the single argument is the
		// fixed dimension and must be positive. Without it the canonical
		// type is meaningless and the DDL/value layers cannot compile.
		if len(column.Type.Arguments) != 1 || column.Type.Arguments[0] <= 0 {
			return fmt.Errorf("column %q.%q vector type requires a single positive dimension", tableName, column.Name)
		}
	}
	if column.Generated != nil {
		if !column.Generated.Stored {
			// VIRTUAL is not representable across both engines, so the only valid
			// generated column is STORED. A false Stored (an attempt to model
			// VIRTUAL) is rejected rather than silently coerced.
			return fmt.Errorf("column %q.%q generated column must be STORED (VIRTUAL is not supported)", tableName, column.Name)
		}
		if column.Default != nil {
			// A column is either supplied (with an optional DEFAULT) or computed
			// (GENERATED ALWAYS AS ...); both engines reject a column that is both.
			return fmt.Errorf("column %q.%q cannot have both a default and a generated expression", tableName, column.Name)
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
			if column.Expression != nil {
				// An expression key carries no column name. Grammar validity of the
				// expression is enforced at compile time by compileExpression (the
				// same path as generated columns and CHECK constraints), which errors
				// clearly on anything outside Section 3. A key must be one or the
				// other, never both.
				if column.Column != "" {
					return fmt.Errorf("index %q key has both a column and an expression", index.Name)
				}
				continue
			}
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
		if view.Query.From.Table == "" && view.Query.From.Subquery == nil {
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
