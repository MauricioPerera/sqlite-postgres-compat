package compat

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
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
	rows, err := store.DB.QueryContext(ctx, `SELECT type, name, COALESCE(sql, '') FROM sqlite_master WHERE name NOT LIKE 'sqlite_%' AND name NOT IN (?, ?) ORDER BY type, name`, schemaMetadataTable, appliedChangesTable)
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
		column := Column{Name: columnName, Type: Type{Family: sqliteTypeFamily(declaredType)}, Nullable: notNull == 0 && pk == 0}
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
		if onUpdate != "NO ACTION" || onDelete != "NO ACTION" || match != "NONE" {
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
		table.Constraints = append(table.Constraints, Constraint{Kind: ForeignKey, Columns: key.columns, References: &Reference{Table: key.referenceTable, Columns: key.referenceColumns}})
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

func (store *Store) inspectPostgres(ctx context.Context) (Inspection, error) {
	rows, err := store.DB.QueryContext(ctx, `SELECT table_name, column_name, data_type, is_nullable, column_default, is_identity, is_generated
		FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name NOT IN ($1, $2)
		AND table_name IN (SELECT table_name FROM information_schema.tables WHERE table_schema = current_schema() AND table_type = 'BASE TABLE')
		ORDER BY table_name, ordinal_position`, schemaMetadataTable, appliedChangesTable)
	if err != nil {
		return Inspection{}, err
	}
	inspection := Inspection{Source: "postgres_catalog"}
	tables := make(map[string]*Table)
	var order []string
	for rows.Next() {
		var tableName, columnName, dataType, nullable, identity, generated string
		var defaultSQL sql.NullString
		if err := rows.Scan(&tableName, &columnName, &dataType, &nullable, &defaultSQL, &identity, &generated); err != nil {
			return Inspection{}, err
		}
		table := tables[tableName]
		if table == nil {
			table = &Table{Name: tableName}
			tables[tableName] = table
			order = append(order, tableName)
		}
		column := Column{Name: columnName, Type: Type{Family: postgresTypeFamily(dataType)}, Nullable: nullable == "YES"}
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
			if onUpdate != "a" || onDelete != "a" || match != "s" || deferrable || deferred {
				inspection.Unresolved = append(inspection.Unresolved, CatalogObject{Kind: "foreign_key", Name: name, Reason: "referential actions, match mode or deferral are outside the canonical foreign-key grammar"})
				continue
			}
			table.Constraints = append(table.Constraints, Constraint{Kind: ForeignKey, Columns: columns, References: &Reference{Table: referenceTable, Columns: referenceColumns}})
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
	defer objects.Close()
	for objects.Next() {
		var kind, name string
		var definition sql.NullString
		if err := objects.Scan(&kind, &name, &definition); err != nil {
			return Inspection{}, err
		}
		inspection.Unresolved = append(inspection.Unresolved, CatalogObject{Kind: kind, Name: name, Definition: definition.String, Reason: "requires SQL parser translation"})
	}
	inspection.Exact = len(inspection.Unresolved) == 0
	return inspection, objects.Err()
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
