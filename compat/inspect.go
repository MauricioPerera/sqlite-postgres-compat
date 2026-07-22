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
	rows, err := store.DB.QueryContext(ctx, `SELECT type, name, COALESCE(sql, '') FROM sqlite_master WHERE name NOT LIKE 'sqlite_%' AND name <> ? ORDER BY type, name`, schemaMetadataTable)
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
		if item.kind != "table" {
			inspection.Unresolved = append(inspection.Unresolved, CatalogObject{Kind: item.kind, Name: item.name, Definition: item.definition, Reason: "requires SQL parser translation"})
			continue
		}
		table, err := store.inspectSQLiteTable(ctx, item.name)
		if err != nil {
			return Inspection{}, err
		}
		inspection.Schema.Tables = append(inspection.Schema.Tables, table)
	}
	inspection.Exact = len(inspection.Unresolved) == 0
	return inspection, nil
}

func (store *Store) inspectSQLiteTable(ctx context.Context, name string) (Table, error) {
	rows, err := store.DB.QueryContext(ctx, `SELECT name, type, "notnull", pk FROM pragma_table_info(?) ORDER BY cid`, name)
	if err != nil {
		return Table{}, err
	}
	defer rows.Close()
	table := Table{Name: name}
	var primary []string
	for rows.Next() {
		var columnName, declaredType string
		var notNull, pk int
		if err := rows.Scan(&columnName, &declaredType, &notNull, &pk); err != nil {
			return Table{}, err
		}
		table.Columns = append(table.Columns, Column{Name: columnName, Type: Type{Family: sqliteTypeFamily(declaredType)}, Nullable: notNull == 0 && pk == 0})
		if pk > 0 {
			primary = append(primary, columnName)
		}
	}
	if len(primary) > 0 {
		table.Constraints = append(table.Constraints, Constraint{Kind: PrimaryKey, Columns: primary})
	}
	return table, rows.Err()
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
	rows, err := store.DB.QueryContext(ctx, `SELECT table_name, column_name, data_type, is_nullable
		FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name <> $1
		ORDER BY table_name, ordinal_position`, schemaMetadataTable)
	if err != nil {
		return Inspection{}, err
	}
	defer rows.Close()
	inspection := Inspection{Source: "postgres_catalog"}
	tables := make(map[string]*Table)
	var order []string
	for rows.Next() {
		var tableName, columnName, dataType, nullable string
		if err := rows.Scan(&tableName, &columnName, &dataType, &nullable); err != nil {
			return Inspection{}, err
		}
		table := tables[tableName]
		if table == nil {
			table = &Table{Name: tableName}
			tables[tableName] = table
			order = append(order, tableName)
		}
		table.Columns = append(table.Columns, Column{Name: columnName, Type: Type{Family: postgresTypeFamily(dataType)}, Nullable: nullable == "YES"})
	}
	if err := rows.Err(); err != nil {
		return Inspection{}, err
	}
	for _, name := range order {
		inspection.Schema.Tables = append(inspection.Schema.Tables, *tables[name])
	}
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
