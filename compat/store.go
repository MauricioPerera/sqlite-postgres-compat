package compat

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

// Store is the database/sql boundary for a concrete engine. All values crossing
// this boundary are converted into the canonical Value representation.
type Store struct {
	Target Target
	DB     *sql.DB
}

func OpenStore(target Target, dsn string) (*Store, error) {
	if err := target.Validate(); err != nil {
		return nil, err
	}
	switch target.Engine {
	case SQLite:
		return OpenSQLite(target.Version, dsn)
	case Postgres:
		return OpenPostgres(target.Version, dsn)
	default:
		return nil, fmt.Errorf("unsupported engine %q", target.Engine)
	}
}

func OpenSQLite(version Version, dsn string) (*Store, error) {
	db, err := sql.Open("sqlite", sqliteDSN(dsn))
	if err != nil {
		return nil, err
	}
	// SQLite pragmas are connection-scoped. A single connection guarantees that
	// every operation observes the foreign-key enforcement contract.
	db.SetMaxOpenConns(1)
	return &Store{Target: Target{Engine: SQLite, Version: version}, DB: db}, nil
}

func sqliteDSN(dsn string) string {
	separator := "?"
	if strings.Contains(dsn, "?") {
		separator = "&"
	}
	return dsn + separator + "_pragma=foreign_keys(1)"
}

func OpenPostgres(version Version, dsn string) (*Store, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, err
	}
	return &Store{Target: Target{Engine: Postgres, Version: version}, DB: db}, nil
}

func (store *Store) Close() error {
	return store.DB.Close()
}

func (store *Store) ApplySchema(ctx context.Context, schema Schema) error {
	statements, err := CompileDDL(store.Target, schema)
	if err != nil {
		return err
	}
	tx, err := store.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("apply schema: %w", err)
		}
	}
	return tx.Commit()
}

type Snapshot struct {
	Schema Schema           `json:"schema"`
	Rows   map[string][]Row `json:"rows"`
}

func (store *Store) ExportSnapshot(ctx context.Context, schema Schema) (Snapshot, error) {
	if err := schema.Validate(); err != nil {
		return Snapshot{}, err
	}
	snapshot := Snapshot{Schema: schema, Rows: make(map[string][]Row, len(schema.Tables))}
	for _, table := range schema.Tables {
		rows, err := store.exportTable(ctx, table)
		if err != nil {
			return Snapshot{}, err
		}
		snapshot.Rows[table.Name] = rows
	}
	return snapshot, nil
}

func (store *Store) exportTable(ctx context.Context, table Table) ([]Row, error) {
	columns := make([]string, len(table.Columns))
	for i, column := range table.Columns {
		columns[i] = quoteIdentifier(column.Name)
	}
	query := "SELECT " + strings.Join(columns, ", ") + " FROM " + quoteIdentifier(table.Name)
	result, err := store.DB.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("export %s: %w", table.Name, err)
	}
	defer result.Close()

	rows := make([]Row, 0)
	for result.Next() {
		values := make([]any, len(table.Columns))
		destinations := make([]any, len(table.Columns))
		for i := range values {
			destinations[i] = &values[i]
		}
		if err := result.Scan(destinations...); err != nil {
			return nil, err
		}
		row := make(Row, len(table.Columns))
		for i, column := range table.Columns {
			value, err := canonicalValue(column.Type.Family, values[i])
			if err != nil {
				return nil, fmt.Errorf("export %s.%s: %w", table.Name, column.Name, err)
			}
			row[column.Name] = value
		}
		rows = append(rows, row)
	}
	return rows, result.Err()
}

// ImportSnapshot creates the canonical schema and inserts every canonical row.
// It is intentionally append-only; replacement and synchronization policies are
// handled by higher layers so data is never deleted implicitly.
func (store *Store) ImportSnapshot(ctx context.Context, snapshot Snapshot) error {
	if err := snapshot.Schema.Validate(); err != nil {
		return err
	}
	if err := store.ApplySchema(ctx, snapshot.Schema); err != nil {
		return err
	}
	tx, err := store.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, table := range snapshot.Schema.Tables {
		for _, row := range snapshot.Rows[table.Name] {
			if err := insertRow(ctx, tx, store.Target.Engine, table, row); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

func insertRow(ctx context.Context, tx *sql.Tx, engine Engine, table Table, row Row) error {
	columns := make([]string, 0, len(table.Columns))
	placeholders := make([]string, 0, len(table.Columns))
	arguments := make([]any, 0, len(table.Columns))
	for index, column := range table.Columns {
		value, ok := row[column.Name]
		if !ok {
			return fmt.Errorf("import %s: missing column %q", table.Name, column.Name)
		}
		argument, err := driverValue(engine, value)
		if err != nil {
			return fmt.Errorf("import %s.%s: %w", table.Name, column.Name, err)
		}
		columns = append(columns, quoteIdentifier(column.Name))
		placeholders = append(placeholders, placeholder(engine, index+1))
		arguments = append(arguments, argument)
	}
	statement := "INSERT INTO " + quoteIdentifier(table.Name) + " (" + strings.Join(columns, ", ") + ") VALUES (" + strings.Join(placeholders, ", ") + ")"
	if _, err := tx.ExecContext(ctx, statement, arguments...); err != nil {
		return err
	}
	return nil
}

func placeholder(engine Engine, position int) string {
	if engine == Postgres {
		return "$" + strconv.Itoa(position)
	}
	return "?"
}

func canonicalValue(kind TypeFamily, source any) (Value, error) {
	if source == nil {
		return Value{Kind: NullValue}, nil
	}
	if kind == BinaryType {
		bytes, ok := source.([]byte)
		if !ok {
			return Value{}, fmt.Errorf("expected binary value, got %T", source)
		}
		return Value{Kind: BinaryValue, Value: base64.StdEncoding.EncodeToString(bytes)}, nil
	}
	if timestamp, ok := source.(time.Time); ok {
		return Value{Kind: TimestampValue, Value: timestamp.UTC().Format(time.RFC3339Nano)}, nil
	}
	text := stringify(source)
	switch kind {
	case BooleanType:
		return Value{Kind: BooleanValue, Value: normalizeBoolean(text)}, nil
	case IntegerType:
		return Value{Kind: IntegerValue, Value: text}, nil
	case DecimalType:
		return Value{Kind: DecimalValue, Value: text}, nil
	case FloatType:
		return Value{Kind: FloatValue, Value: text}, nil
	case DateType:
		return Value{Kind: DateValue, Value: text}, nil
	case TimestampType:
		timestamp, err := time.Parse(time.RFC3339Nano, text)
		if err != nil {
			return Value{}, fmt.Errorf("invalid timestamp %q: %w", text, err)
		}
		return Value{Kind: TimestampValue, Value: timestamp.UTC().Format(time.RFC3339Nano)}, nil
	case JSONType:
		var document any
		if err := json.Unmarshal([]byte(text), &document); err != nil {
			return Value{}, fmt.Errorf("invalid JSON: %w", err)
		}
		normalized, err := json.Marshal(document)
		if err != nil {
			return Value{}, err
		}
		return Value{Kind: JSONValue, Value: string(normalized)}, nil
	case UUIDType:
		return Value{Kind: UUIDValue, Value: text}, nil
	default:
		return Value{Kind: TextValue, Value: text}, nil
	}
}

func driverValue(engine Engine, value Value) (any, error) {
	if value.Kind == NullValue {
		return nil, nil
	}
	if value.Kind == BinaryValue {
		return base64.StdEncoding.DecodeString(value.Value)
	}
	if value.Kind == BooleanValue && engine == SQLite {
		if value.Value == "true" {
			return 1, nil
		}
		return 0, nil
	}
	return value.Value, nil
}

func stringify(value any) string {
	switch typed := value.(type) {
	case []byte:
		return string(typed)
	case bool:
		return strconv.FormatBool(typed)
	default:
		return fmt.Sprint(typed)
	}
}

func normalizeBoolean(value string) string {
	switch strings.ToLower(value) {
	case "1", "t", "true":
		return "true"
	default:
		return "false"
	}
}
