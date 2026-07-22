package compat

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

type CatalogObject struct {
	Kind       string `json:"kind"`
	Name       string `json:"name"`
	Definition string `json:"definition,omitempty"`
	Reason     string `json:"reason"`
}

type Inspection struct {
	Schema     Schema          `json:"schema"`
	Exact      bool            `json:"exact"`
	Source     string          `json:"source"`
	Unresolved []CatalogObject `json:"unresolved,omitempty"`
}

// InspectSchema reconstructs the exact canonical schema when the database was
// managed by this compatibility layer. For external databases it falls back to
// catalog inspection and explicitly reports objects not yet translated.
func (store *Store) InspectSchema(ctx context.Context) (Inspection, error) {
	var payload string
	query := "SELECT " + quoteIdentifier("value") + " FROM " + quoteIdentifier(schemaMetadataTable) + " WHERE " + quoteIdentifier("key") + " = " + placeholder(store.Target.Engine, 1)
	if err := store.DB.QueryRowContext(ctx, query, "canonical_schema").Scan(&payload); err == nil {
		var schema Schema
		if err := json.Unmarshal([]byte(payload), &schema); err != nil {
			return Inspection{}, fmt.Errorf("decode canonical schema metadata: %w", err)
		}
		if err := schema.Validate(); err != nil {
			return Inspection{}, fmt.Errorf("invalid canonical schema metadata: %w", err)
		}
		return Inspection{Schema: schema, Exact: true, Source: "canonical_metadata"}, nil
	}

	switch store.Target.Engine {
	case SQLite:
		return store.inspectSQLite(ctx)
	case Postgres:
		return store.inspectPostgres(ctx)
	default:
		return Inspection{}, fmt.Errorf("unsupported engine %q", store.Target.Engine)
	}
}

func (store *Store) inspectSQLite(ctx context.Context) (Inspection, error) {
	rows, err := store.DB.QueryContext(ctx, `SELECT type, name, COALESCE(sql, '') FROM sqlite_master WHERE name NOT LIKE 'sqlite_%' AND name NOT IN (?, ?, ?, ?) AND name NOT LIKE '__compat_capture_%' ORDER BY type, name`, schemaMetadataTable, appliedChangesTable, captureStateTable, changeJournalTable)
	if err != nil {
		return Inspection{}, err
	}
	inspection := Inspection{Source: "sqlite_catalog"}
	type object struct {
		kind       string
		name       string
		definition string
	}
	var objects []object
	for rows.Next() {
		var item object
		if err := rows.Scan(&item.kind, &item.name, &item.definition); err != nil {
			rows.Close()
			return Inspection{}, err
		}
		objects = append(objects, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return Inspection{}, err
	}
	if err := rows.Close(); err != nil {
		return Inspection{}, err
	}
	for _, item := range objects {
		if item.kind == "index" {
			continue
		}
		if item.kind == "view" {
			query, err := parseCatalogSelect(item.definition)
			if err != nil {
				inspection.Unresolved = append(inspection.Unresolved, CatalogObject{Kind: item.kind, Name: item.name, Definition: item.definition, Reason: err.Error()})
			} else {
				inspection.Schema.Views = append(inspection.Schema.Views, View{Name: item.name, Query: query})
			}
			continue
		}
		if item.kind == "trigger" {
			trigger, err := parseSQLiteCatalogTrigger(item.definition)
			if err != nil {
				inspection.Unresolved = append(inspection.Unresolved, CatalogObject{Kind: item.kind, Name: item.name, Definition: item.definition, Reason: err.Error()})
			} else {
				inspection.Schema.Triggers = append(inspection.Schema.Triggers, trigger)
			}
			continue
		}
		if item.kind != "table" {
			inspection.Unresolved = append(inspection.Unresolved, CatalogObject{Kind: item.kind, Name: item.name, Definition: item.definition, Reason: "requires SQL parser translation"})
			continue
		}
		table, checks, err := store.inspectSQLiteTable(ctx, item.name, item.definition)
		if err != nil {
			return Inspection{}, err
		}
		inspection.Unresolved = append(inspection.Unresolved, checks...)
		indexes, uniqueConstraints, unresolved, err := store.inspectSQLiteIndexes(ctx, item.name)
		if err != nil {
			return Inspection{}, err
		}
		table.Constraints = append(table.Constraints, uniqueConstraints...)
		inspection.Schema.Tables = append(inspection.Schema.Tables, table)
		inspection.Schema.Indexes = append(inspection.Schema.Indexes, indexes...)
		inspection.Unresolved = append(inspection.Unresolved, unresolved...)
	}
	inspection.Exact = len(inspection.Unresolved) == 0
	return inspection, nil
}

func (store *Store) inspectSQLiteTable(ctx context.Context, name, definition string) (Table, []CatalogObject, error) {
	rows, err := store.DB.QueryContext(ctx, `SELECT name, type, "notnull", dflt_value, pk, hidden FROM pragma_table_xinfo(?) ORDER BY cid`, name)
	if err != nil {
		return Table{}, nil, err
	}
	table := Table{Name: name}
	var primary []string
	var unresolved []CatalogObject
	for rows.Next() {
		var columnName, declaredType string
		var defaultSQL sql.NullString
		var notNull, pk, hidden int
		if err := rows.Scan(&columnName, &declaredType, &notNull, &defaultSQL, &pk, &hidden); err != nil {
			return Table{}, nil, err
		}
		declaredFamily := sqliteTypeFamily(declaredType)
		declaredTypeValue := Type{Family: declaredFamily}
		if declaredFamily == VectorType {
			// F32_BLOB(N) carries the dimension inside the declared type text;
			// extract it so the canonical Type.Arguments matches the declaration.
			declaredTypeValue.Arguments = parseTypeArguments(declaredType)
		}
		column := Column{Name: columnName, Type: declaredTypeValue, Nullable: notNull == 0 && pk == 0}
		if defaultSQL.Valid {
			expression, err := parseCatalogExpression(defaultSQL.String)
			if err != nil {
				unresolved = append(unresolved, CatalogObject{Kind: "default", Name: name + "." + columnName, Definition: defaultSQL.String, Reason: err.Error()})
			} else {
				column.Default = &expression
			}
		}
		if hidden != 0 {
			unresolved = append(unresolved, CatalogObject{Kind: "generated_column", Name: name + "." + columnName, Reason: "generated and hidden columns require canonical generation semantics"})
		}
		table.Columns = append(table.Columns, column)
		if pk > 0 {
			primary = append(primary, columnName)
		}
	}
	if len(primary) > 0 {
		table.Constraints = append(table.Constraints, Constraint{Kind: PrimaryKey, Columns: primary})
	}
	if err := rows.Err(); err != nil {
		return Table{}, nil, err
	}
	if err := rows.Close(); err != nil {
		return Table{}, nil, err
	}
	for _, source := range extractCheckExpressions(definition) {
		expression, err := parseCatalogExpression(source)
		if err != nil {
			unresolved = append(unresolved, CatalogObject{Kind: "check", Name: name, Definition: source, Reason: err.Error()})
			continue
		}
		table.Constraints = append(table.Constraints, Constraint{Kind: Check, Expression: &expression})
	}
	foreignRows, err := store.DB.QueryContext(ctx, `SELECT id, seq, "table", "from", "to", on_update, on_delete, "match" FROM pragma_foreign_key_list(?) ORDER BY id, seq`, name)
	if err != nil {
		return Table{}, nil, err
	}
	type foreignKey struct {
		columns, referenceColumns []string
		referenceTable            string
		onUpdate, onDelete        ReferentialAction
		valid                     bool
	}
	foreign := map[int]*foreignKey{}
	var foreignOrder []int
	for foreignRows.Next() {
		var id, sequence int
		var referenceTable, column, referenceColumn, onUpdate, onDelete, match string
		if err := foreignRows.Scan(&id, &sequence, &referenceTable, &column, &referenceColumn, &onUpdate, &onDelete, &match); err != nil {
			foreignRows.Close()
			return Table{}, nil, err
		}
		key := foreign[id]
		if key == nil {
			key = &foreignKey{referenceTable: referenceTable, valid: true}
			foreign[id] = key
			foreignOrder = append(foreignOrder, id)
		}
		key.columns = append(key.columns, column)
		key.referenceColumns = append(key.referenceColumns, referenceColumn)
		var actionOK bool
		key.onUpdate, actionOK = sqliteReferentialAction(onUpdate)
		if !actionOK {
			key.valid = false
		}
		key.onDelete, actionOK = sqliteReferentialAction(onDelete)
		if !actionOK || match != "NONE" {
			key.valid = false
		}
	}
	if err := foreignRows.Close(); err != nil {
		return Table{}, nil, err
	}
	for _, id := range foreignOrder {
		key := foreign[id]
		if !key.valid {
			unresolved = append(unresolved, CatalogObject{Kind: "foreign_key", Name: fmt.Sprintf("%s#%d", name, id), Reason: "referential actions or match mode are outside the canonical foreign-key grammar"})
			continue
		}
		table.Constraints = append(table.Constraints, Constraint{Kind: ForeignKey, Columns: key.columns, References: &Reference{Table: key.referenceTable, Columns: key.referenceColumns, OnUpdate: key.onUpdate, OnDelete: key.onDelete}})
	}
	return table, unresolved, nil
}

func (store *Store) inspectSQLiteIndexes(ctx context.Context, tableName string) ([]Index, []Constraint, []CatalogObject, error) {
	rows, err := store.DB.QueryContext(ctx, `SELECT name, "unique", origin, partial FROM pragma_index_list(?) ORDER BY seq`, tableName)
	if err != nil {
		return nil, nil, nil, err
	}
	type catalogIndex struct {
		name    string
		unique  int
		origin  string
		partial int
	}
	var catalog []catalogIndex
	for rows.Next() {
		var item catalogIndex
		if err := rows.Scan(&item.name, &item.unique, &item.origin, &item.partial); err != nil {
			rows.Close()
			return nil, nil, nil, err
		}
		catalog = append(catalog, item)
	}
	if err := rows.Close(); err != nil {
		return nil, nil, nil, err
	}
	var indexes []Index
	var constraints []Constraint
	var unresolved []CatalogObject
	for _, item := range catalog {
		if item.origin == "pk" {
			continue
		}
		index := Index{Name: item.name, Table: tableName, Unique: item.unique != 0}
		columns, err := store.DB.QueryContext(ctx, `SELECT cid, name, "desc", key FROM pragma_index_xinfo(?) ORDER BY seqno`, item.name)
		if err != nil {
			return nil, nil, nil, err
		}
		valid := true
		for columns.Next() {
			var cid, descending, key int
			var name sql.NullString
			if err := columns.Scan(&cid, &name, &descending, &key); err != nil {
				columns.Close()
				return nil, nil, nil, err
			}
			if key == 0 {
				continue
			}
			if cid < 0 || !name.Valid {
				valid = false
				continue
			}
			index.Columns = append(index.Columns, IndexColumn{Column: name.String, Descending: descending != 0})
		}
		if err := columns.Close(); err != nil {
			return nil, nil, nil, err
		}
		var definition string
		if err := store.DB.QueryRowContext(ctx, `SELECT COALESCE(sql, '') FROM sqlite_master WHERE type = 'index' AND name = ?`, item.name).Scan(&definition); err != nil {
			return nil, nil, nil, err
		}
		if item.partial != 0 {
			predicate := extractIndexPredicate(definition)
			expression, err := parseCatalogExpression(predicate)
			if err != nil {
				valid = false
			} else {
				index.Where = &expression
			}
		}
		if !valid || len(index.Columns) == 0 {
			unresolved = append(unresolved, CatalogObject{Kind: "index", Name: item.name, Definition: definition, Reason: "expression, collation or predicate is outside the canonical index grammar"})
			continue
		}
		if item.origin == "u" {
			columns := make([]string, len(index.Columns))
			for i, column := range index.Columns {
				columns[i] = column.Column
			}
			constraints = append(constraints, Constraint{Kind: UniqueKey, Columns: columns})
			continue
		}
		indexes = append(indexes, index)
	}
	return indexes, constraints, unresolved, nil
}

func sqliteTypeFamily(declared string) TypeFamily {
	typ := strings.ToUpper(declared)
	switch {
	case strings.Contains(typ, "F32_BLOB"):
		// libSQL/sqld declares native vectors as F32_BLOB(N). It must be matched
		// before the generic BLOB affinity below, otherwise a vector column is
		// silently misread as binary (see docs/reports/VECTOR-COMPAT-REPORT.md §B).
		return VectorType
	case strings.Contains(typ, "BOOL"):
		return BooleanType
	case strings.Contains(typ, "INT"):
		return IntegerType
	case strings.Contains(typ, "DEC"), strings.Contains(typ, "NUM"):
		return DecimalType
	case strings.Contains(typ, "REAL"), strings.Contains(typ, "FLOA"), strings.Contains(typ, "DOUB"):
		return FloatType
	case strings.Contains(typ, "BLOB") || typ == "":
		return BinaryType
	case strings.Contains(typ, "JSON"):
		return JSONType
	case strings.Contains(typ, "UUID"):
		return UUIDType
	case strings.Contains(typ, "TIMESTAMP"), strings.Contains(typ, "DATETIME"):
		return TimestampType
	case typ == "DATE":
		return DateType
	default:
		return TextType
	}
}

// parseTypeArguments extracts the parenthesized integer arguments from a declared
// type such as "F32_BLOB(3)" or "NUMERIC(38, 18)". It returns nil when the
// declaration has no argument list or a non-integer component, so callers can
// treat the dimension as genuinely absent rather than zero.
func parseTypeArguments(declared string) []int {
	start := strings.Index(declared, "(")
	end := strings.LastIndex(declared, ")")
	if start < 0 || end <= start {
		return nil
	}
	fields := strings.Split(declared[start+1:end], ",")
	arguments := make([]int, 0, len(fields))
	for _, field := range fields {
		value, err := strconv.Atoi(strings.TrimSpace(field))
		if err != nil {
			return nil
		}
		arguments = append(arguments, value)
	}
	return arguments
}

func (store *Store) inspectPostgres(ctx context.Context) (Inspection, error) {
	// atttypmod is NOT a column of information_schema.columns in PostgreSQL 17
	// (selecting it raises 42703), so it cannot be read straight from that view.
	// pgvector stores the vector dimension verbatim in pg_attribute.atttypmod, so
	// for udt_name='vector' columns we fetch it via a scalar subquery correlated
	// on (schema, table, column) against pg_attribute joined to pg_class and
	// pg_namespace. The correlation keys on c.table_schema (not a bare
	// current_schema()) so homonym tables in other schemas cannot leak in, and
	// NOT a.attisdropped excludes dropped columns (a dropped column keeps its
	// attname but is marked dropped; only the live attribute is selected). For
	// non-vector columns atttypmod is unused downstream, so it defaults to -1 and
	// the subquery is not evaluated at all.
	rows, err := store.DB.QueryContext(ctx, `SELECT c.table_name, c.column_name, c.data_type, c.udt_name,
		CASE WHEN c.udt_name = 'vector'
			THEN COALESCE((SELECT a.atttypmod
				FROM pg_attribute a
				JOIN pg_class cl ON cl.oid = a.attrelid
				JOIN pg_namespace n ON n.oid = cl.relnamespace
				WHERE n.nspname = c.table_schema AND cl.relname = c.table_name AND a.attname = c.column_name AND NOT a.attisdropped), -1)
			ELSE -1 END AS atttypmod,
		c.is_nullable, c.column_default, c.is_identity, c.is_generated
		FROM information_schema.columns c
		WHERE c.table_schema = current_schema() AND c.table_name NOT IN ($1, $2, $3, $4)
		AND c.table_name IN (SELECT table_name FROM information_schema.tables WHERE table_schema = current_schema() AND table_type = 'BASE TABLE')
		ORDER BY c.table_name, c.ordinal_position`, schemaMetadataTable, appliedChangesTable, captureStateTable, changeJournalTable)
	if err != nil {
		return Inspection{}, err
	}
	inspection := Inspection{Source: "postgres_catalog"}
	tables := make(map[string]*Table)
	var order []string
	for rows.Next() {
		var tableName, columnName, dataType, udtName, nullable, identity, generated string
		var defaultSQL sql.NullString
		var atttypmod int
		if err := rows.Scan(&tableName, &columnName, &dataType, &udtName, &atttypmod, &nullable, &defaultSQL, &identity, &generated); err != nil {
			return Inspection{}, err
		}
		table := tables[tableName]
		if table == nil {
			table = &Table{Name: tableName}
			tables[tableName] = table
			order = append(order, tableName)
		}
		columnType := postgresType(dataType, udtName, atttypmod)
		if columnType.Family == VectorType && len(columnType.Arguments) == 0 {
			// pgvector allows a dimensionless vector column (atttypmod <= 0); the
			// dimension is genuinely unobtainable there, so surface it explicitly
			// instead of degrading to a silent binary or text mapping.
			inspection.Unresolved = append(inspection.Unresolved, CatalogObject{Kind: "column", Name: tableName + "." + columnName, Reason: "pgvector vector column has no declared dimension"})
		}
		column := Column{Name: columnName, Type: columnType, Nullable: nullable == "YES"}
		if defaultSQL.Valid {
			expression, err := parsePostgresCatalogDefault(defaultSQL.String)
			if err != nil {
				inspection.Unresolved = append(inspection.Unresolved, CatalogObject{Kind: "default", Name: tableName + "." + columnName, Definition: defaultSQL.String, Reason: err.Error()})
			} else {
				column.Default = &expression
			}
		}
		if identity == "YES" || generated != "NEVER" {
			inspection.Unresolved = append(inspection.Unresolved, CatalogObject{Kind: "generated_column", Name: tableName + "." + columnName, Reason: "identity and generated columns require canonical generation semantics"})
		}
		table.Columns = append(table.Columns, column)
	}
	if err := rows.Err(); err != nil {
		return Inspection{}, err
	}
	if err := rows.Close(); err != nil {
		return Inspection{}, err
	}
	for _, name := range order {
		inspection.Schema.Tables = append(inspection.Schema.Tables, *tables[name])
	}
	constraints, err := store.DB.QueryContext(ctx, `SELECT tbl.relname, con.conname, con.contype::text, COALESCE(pg_get_expr(con.conbin, con.conrelid), ''),
		COALESCE((SELECT json_agg(att.attname ORDER BY key.ord)::text FROM unnest(con.conkey) WITH ORDINALITY key(attnum, ord) JOIN pg_attribute att ON att.attrelid = con.conrelid AND att.attnum = key.attnum), '[]'),
		COALESCE(ref.relname, ''),
		COALESCE((SELECT json_agg(att.attname ORDER BY key.ord)::text FROM unnest(con.confkey) WITH ORDINALITY key(attnum, ord) JOIN pg_attribute att ON att.attrelid = con.confrelid AND att.attnum = key.attnum), '[]'),
		con.confupdtype::text, con.confdeltype::text, con.confmatchtype::text, con.condeferrable, con.condeferred
		FROM pg_constraint con
		JOIN pg_class tbl ON tbl.oid = con.conrelid
		JOIN pg_namespace ns ON ns.oid = tbl.relnamespace
		LEFT JOIN pg_class ref ON ref.oid = con.confrelid
		WHERE ns.nspname = current_schema()
		ORDER BY tbl.relname, con.conname`)
	if err != nil {
		return Inspection{}, err
	}
	for constraints.Next() {
		var tableName, name, kind, definition, columnsJSON, referenceTable, referenceColumnsJSON string
		var onUpdate, onDelete, match string
		var deferrable, deferred bool
		if err := constraints.Scan(&tableName, &name, &kind, &definition, &columnsJSON, &referenceTable, &referenceColumnsJSON, &onUpdate, &onDelete, &match, &deferrable, &deferred); err != nil {
			constraints.Close()
			return Inspection{}, err
		}
		var columns, referenceColumns []string
		if err := json.Unmarshal([]byte(columnsJSON), &columns); err != nil {
			constraints.Close()
			return Inspection{}, err
		}
		if err := json.Unmarshal([]byte(referenceColumnsJSON), &referenceColumns); err != nil {
			constraints.Close()
			return Inspection{}, err
		}
		table := tables[tableName]
		if table == nil {
			continue
		}
		switch kind {
		case "p":
			table.Constraints = append(table.Constraints, Constraint{Kind: PrimaryKey, Columns: columns})
		case "u":
			table.Constraints = append(table.Constraints, Constraint{Kind: UniqueKey, Columns: columns})
		case "f":
			updateAction, updateOK := postgresReferentialAction(onUpdate)
			deleteAction, deleteOK := postgresReferentialAction(onDelete)
			if !updateOK || !deleteOK || match != "s" || deferrable || deferred {
				inspection.Unresolved = append(inspection.Unresolved, CatalogObject{Kind: "foreign_key", Name: name, Reason: "referential actions, match mode or deferral are outside the canonical foreign-key grammar"})
				continue
			}
			table.Constraints = append(table.Constraints, Constraint{Kind: ForeignKey, Columns: columns, References: &Reference{Table: referenceTable, Columns: referenceColumns, OnUpdate: updateAction, OnDelete: deleteAction}})
		case "c":
			expression, err := parseCatalogExpression(definition)
			if err != nil {
				inspection.Unresolved = append(inspection.Unresolved, CatalogObject{Kind: "check", Name: name, Definition: definition, Reason: err.Error()})
				continue
			}
			table.Constraints = append(table.Constraints, Constraint{Kind: Check, Expression: &expression})
		default:
			inspection.Unresolved = append(inspection.Unresolved, CatalogObject{Kind: "constraint", Name: name, Definition: definition, Reason: "constraint kind is outside the canonical grammar"})
		}
	}
	if err := constraints.Close(); err != nil {
		return Inspection{}, err
	}
	for i, name := range order {
		inspection.Schema.Tables[i] = *tables[name]
	}

	indexes, err := store.inspectPostgresIndexes(ctx)
	if err != nil {
		return Inspection{}, err
	}
	inspection.Schema.Indexes = append(inspection.Schema.Indexes, indexes.indexes...)
	inspection.Unresolved = append(inspection.Unresolved, indexes.unresolved...)
	objects, err := store.DB.QueryContext(ctx, `SELECT 'view', table_name, view_definition FROM information_schema.views WHERE table_schema = current_schema()`)
	if err != nil {
		return Inspection{}, err
	}
	for objects.Next() {
		var kind, name string
		var definition sql.NullString
		if err := objects.Scan(&kind, &name, &definition); err != nil {
			return Inspection{}, err
		}
		query, err := parseCatalogSelect(definition.String)
		if err != nil {
			inspection.Unresolved = append(inspection.Unresolved, CatalogObject{Kind: kind, Name: name, Definition: definition.String, Reason: err.Error()})
		} else {
			inspection.Schema.Views = append(inspection.Schema.Views, View{Name: name, Query: query})
		}
	}
	if err := objects.Close(); err != nil {
		return Inspection{}, err
	}
	triggers, err := store.DB.QueryContext(ctx, `SELECT trg.tgname, pg_get_triggerdef(trg.oid, true), proc.prosrc
		FROM pg_trigger trg
		JOIN pg_class tbl ON tbl.oid = trg.tgrelid
		JOIN pg_namespace ns ON ns.oid = tbl.relnamespace
		JOIN pg_proc proc ON proc.oid = trg.tgfoid
		WHERE ns.nspname = current_schema() AND NOT trg.tgisinternal AND trg.tgname NOT LIKE '__compat_capture_%'
		ORDER BY trg.tgname`)
	if err != nil {
		return Inspection{}, err
	}
	for triggers.Next() {
		var name, definition, functionBody string
		if err := triggers.Scan(&name, &definition, &functionBody); err != nil {
			triggers.Close()
			return Inspection{}, err
		}
		trigger, err := parsePostgresCatalogTrigger(definition, functionBody)
		if err != nil {
			inspection.Unresolved = append(inspection.Unresolved, CatalogObject{Kind: "trigger", Name: name, Definition: definition, Reason: err.Error()})
		} else {
			inspection.Schema.Triggers = append(inspection.Schema.Triggers, trigger)
		}
	}
	if err := triggers.Close(); err != nil {
		return Inspection{}, err
	}
	// Only plain functions ('f') and procedures ('p') are modeled as routines;
	// aggregates ('a') and window functions ('w') are not. Restricting prokind
	// also keeps pg_get_function_result/pg_get_functiondef off aggregates: those
	// helpers raise 42809 on an aggregate (e.g. pgvector installs an avg(vector)
	// aggregate in the public schema), which would abort the whole inspection.
	routines, err := store.DB.QueryContext(ctx, `SELECT proc.proname, proc.prosrc, pg_get_function_arguments(proc.oid), COALESCE(pg_get_function_result(proc.oid), ''), lang.lanname, proc.prokind::text, pg_get_functiondef(proc.oid)
		FROM pg_proc proc
		JOIN pg_namespace ns ON ns.oid = proc.pronamespace
		JOIN pg_language lang ON lang.oid = proc.prolang
		WHERE ns.nspname = current_schema()
		AND proc.prokind IN ('f', 'p')
		AND NOT EXISTS (SELECT 1 FROM pg_trigger trg WHERE trg.tgfoid = proc.oid)
		ORDER BY proc.proname`)
	if err != nil {
		return Inspection{}, err
	}
	for routines.Next() {
		var name, body, arguments, resultType, language, kind, definition string
		if err := routines.Scan(&name, &body, &arguments, &resultType, &language, &kind, &definition); err != nil {
			routines.Close()
			return Inspection{}, err
		}
		routine, err := parsePostgresCatalogRoutine(name, body, arguments, resultType, language, kind)
		if err != nil {
			inspection.Unresolved = append(inspection.Unresolved, CatalogObject{Kind: "routine", Name: name, Definition: definition, Reason: err.Error()})
		} else {
			inspection.Schema.Routines = append(inspection.Schema.Routines, routine)
		}
	}
	if err := routines.Close(); err != nil {
		return Inspection{}, err
	}
	inspection.Exact = len(inspection.Unresolved) == 0
	return inspection, nil
}

func sqliteReferentialAction(action string) (ReferentialAction, bool) {
	switch action {
	case "NO ACTION":
		return "", true
	case "RESTRICT":
		return Restrict, true
	case "CASCADE":
		return Cascade, true
	case "SET NULL":
		return SetNull, true
	case "SET DEFAULT":
		return SetDefault, true
	default:
		return "", false
	}
}

func postgresReferentialAction(action string) (ReferentialAction, bool) {
	switch action {
	case "a":
		return "", true
	case "r":
		return Restrict, true
	case "c":
		return Cascade, true
	case "n":
		return SetNull, true
	case "d":
		return SetDefault, true
	default:
		return "", false
	}
}

type postgresIndexInspection struct {
	indexes    []Index
	unresolved []CatalogObject
}

func (store *Store) inspectPostgresIndexes(ctx context.Context) (postgresIndexInspection, error) {
	rows, err := store.DB.QueryContext(ctx, `SELECT tbl.relname, idx.relname, ind.indisunique, COALESCE(pg_get_expr(ind.indpred, ind.indrelid), ''), ind.indexrelid::bigint, ind.indnkeyatts, am.amname
		FROM pg_index ind
		JOIN pg_class idx ON idx.oid = ind.indexrelid
		JOIN pg_class tbl ON tbl.oid = ind.indrelid
		JOIN pg_namespace ns ON ns.oid = tbl.relnamespace
		JOIN pg_am am ON am.oid = idx.relam
		LEFT JOIN pg_constraint con ON con.conindid = ind.indexrelid
		WHERE ns.nspname = current_schema() AND con.oid IS NULL
		ORDER BY tbl.relname, idx.relname`)
	if err != nil {
		return postgresIndexInspection{}, err
	}
	type catalogIndex struct {
		table, name, predicate, method string
		unique                         bool
		keyCount                       int
		oid                            int64
	}
	var catalog []catalogIndex
	for rows.Next() {
		var item catalogIndex
		if err := rows.Scan(&item.table, &item.name, &item.unique, &item.predicate, &item.oid, &item.keyCount, &item.method); err != nil {
			rows.Close()
			return postgresIndexInspection{}, err
		}
		catalog = append(catalog, item)
	}
	if err := rows.Close(); err != nil {
		return postgresIndexInspection{}, err
	}
	var result postgresIndexInspection
	for _, item := range catalog {
		index := Index{Name: item.name, Table: item.table, Unique: item.unique}
		valid := item.method == "btree"
		for position := 1; position <= item.keyCount; position++ {
			var definition string
			var descending bool
			if err := store.DB.QueryRowContext(ctx, `SELECT pg_get_indexdef($1::oid, $2, true), (indoption[$2 - 1] & 1) <> 0 FROM pg_index WHERE indexrelid = $1::oid`, item.oid, position).Scan(&definition, &descending); err != nil {
				return postgresIndexInspection{}, err
			}
			definition = strings.TrimSpace(definition)
			upper := strings.ToUpper(definition)
			if strings.HasSuffix(upper, " DESC") {
				definition = strings.TrimSpace(definition[:len(definition)-len(" DESC")])
			} else if strings.HasSuffix(upper, " ASC") {
				definition = strings.TrimSpace(definition[:len(definition)-len(" ASC")])
			}
			column, ok := parseCatalogIdentifier(definition)
			if !ok || strings.Contains(column, ".") {
				valid = false
				continue
			}
			index.Columns = append(index.Columns, IndexColumn{Column: column, Descending: descending})
		}
		if item.predicate != "" {
			expression, err := parseCatalogExpression(item.predicate)
			if err != nil {
				valid = false
			} else {
				index.Where = &expression
			}
		}
		if !valid || len(index.Columns) != item.keyCount {
			result.unresolved = append(result.unresolved, CatalogObject{Kind: "index", Name: item.name, Definition: item.predicate, Reason: "index method, expression, collation, operator class or predicate is outside the canonical grammar"})
			continue
		}
		result.indexes = append(result.indexes, index)
	}
	return result, nil
}

func postgresTypeFamily(dataType string) TypeFamily {
	switch dataType {
	case "boolean":
		return BooleanType
	case "smallint", "integer", "bigint":
		return IntegerType
	case "numeric", "decimal":
		return DecimalType
	case "real", "double precision":
		return FloatType
	case "bytea":
		return BinaryType
	case "date":
		return DateType
	case "timestamp with time zone", "timestamp without time zone":
		return TimestampType
	case "json", "jsonb":
		return JSONType
	case "uuid":
		return UUIDType
	default:
		return TextType
	}
}

// postgresType maps an information_schema column to a canonical Type. pgvector
// columns surface as udt_name='vector'; their dimension is pgvector's typmod
// (atttypmod), which stores the dimension directly. When the dimension is
// unavailable (dimensionless vector column) the family is still vector so the
// caller can emit an explicit unresolved object rather than a silent fallback.
func postgresType(dataType, udtName string, atttypmod int) Type {
	if udtName == "vector" {
		if atttypmod > 0 {
			return Type{Family: VectorType, Arguments: []int{atttypmod}}
		}
		return Type{Family: VectorType}
	}
	return Type{Family: postgresTypeFamily(dataType)}
}

func extractCheckExpressions(definition string) []string {
	var expressions []string
	upper := strings.ToUpper(definition)
	for offset := 0; offset < len(definition); {
		position := strings.Index(upper[offset:], "CHECK")
		if position < 0 {
			break
		}
		position += offset
		before := position - 1
		after := position + len("CHECK")
		if !wordBoundary(definition, before) || !wordBoundary(definition, after) {
			offset = after
			continue
		}
		start := after
		for start < len(definition) && (definition[start] == ' ' || definition[start] == '\t' || definition[start] == '\r' || definition[start] == '\n') {
			start++
		}
		if start >= len(definition) || definition[start] != '(' {
			offset = after
			continue
		}
		end := matchingParenthesis(definition, start)
		if end < 0 {
			break
		}
		expressions = append(expressions, definition[start+1:end])
		offset = end + 1
	}
	return expressions
}

func extractIndexPredicate(definition string) string {
	upper := strings.ToUpper(definition)
	position := strings.LastIndex(upper, " WHERE ")
	if position < 0 {
		return ""
	}
	return strings.TrimSpace(definition[position+len(" WHERE "):])
}

func matchingParenthesis(text string, start int) int {
	depth := 0
	inSingle, inDouble := false, false
	for i := start; i < len(text); i++ {
		switch text[i] {
		case '\'':
			if !inDouble {
				if inSingle && i+1 < len(text) && text[i+1] == '\'' {
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
				if depth == 0 {
					return i
				}
			}
		}
	}
	return -1
}
