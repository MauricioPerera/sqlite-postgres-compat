package compat

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

const appliedChangesTable = "__compat_applied_changes"
const captureStateTable = "__compat_capture_state"

type ConflictError struct {
	Table      string
	PrimaryKey Row
	Expected   Row
	Actual     Row
}

func (err *ConflictError) Error() string {
	// Render Expected/Actual as compact, key-sorted JSON so the diagnostic
	// shows exactly which values differ, not just the table and primary key.
	expected, _ := json.Marshal(err.Expected)
	actual, _ := json.Marshal(err.Actual)
	return fmt.Sprintf("replication conflict on %s primary key %v: expected %s, actual %s",
		err.Table, err.PrimaryKey, expected, actual)
}

// ApplyChanges applies one ordered source stream atomically. Reapplying the
// same stream is safe: committed source sequences are recorded in the target
// transaction and skipped on subsequent attempts.
func (store *Store) ApplyChanges(ctx context.Context, schema Schema, changes []Change) error {
	ordered, err := OrderedChanges(changes)
	if err != nil {
		return err
	}
	if len(ordered) == 0 {
		return nil
	}
	for i := 1; i < len(ordered); i++ {
		if ordered[i].Source != ordered[0].Source {
			return fmt.Errorf("one ApplyChanges call must contain a single source stream")
		}
	}
	tx, err := store.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := createAppliedChangesTable(ctx, tx); err != nil {
		return err
	}
	if err := setCaptureSuppressed(ctx, tx, true); err != nil {
		return err
	}
	for _, change := range ordered {
		applied, err := changeWasApplied(ctx, tx, store.Target.Engine, change)
		if err != nil {
			return err
		}
		if applied {
			continue
		}
		table, err := findTable(schema, change.Table)
		if err != nil {
			return err
		}
		if err := applyChange(ctx, tx, store.Target.Engine, table, change); err != nil {
			return err
		}
		if err := markChangeApplied(ctx, tx, store.Target.Engine, change); err != nil {
			return err
		}
	}
	if err := setCaptureSuppressed(ctx, tx, false); err != nil {
		return err
	}
	return tx.Commit()
}

func createAppliedChangesTable(ctx context.Context, tx *sql.Tx) error {
	statement := "CREATE TABLE IF NOT EXISTS " + quoteIdentifier(appliedChangesTable) + " (" +
		quoteIdentifier("source_engine") + " TEXT NOT NULL, " +
		quoteIdentifier("source_version") + " TEXT NOT NULL, " +
		quoteIdentifier("sequence") + " TEXT NOT NULL, PRIMARY KEY (" +
		quoteIdentifier("source_engine") + ", " + quoteIdentifier("source_version") + ", " + quoteIdentifier("sequence") + "))"
	if _, err := tx.ExecContext(ctx, statement); err != nil {
		return err
	}
	state := "CREATE TABLE IF NOT EXISTS " + quoteIdentifier(captureStateTable) + " (" + quoteIdentifier("id") + " INTEGER PRIMARY KEY, " + quoteIdentifier("suppress") + " INTEGER NOT NULL)"
	if _, err := tx.ExecContext(ctx, state); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO "+quoteIdentifier(captureStateTable)+" ("+quoteIdentifier("id")+", "+quoteIdentifier("suppress")+") VALUES (1, 0) ON CONFLICT ("+quoteIdentifier("id")+") DO NOTHING"); err != nil {
		return err
	}
	return nil
}

func setCaptureSuppressed(ctx context.Context, tx *sql.Tx, suppressed bool) error {
	value := 0
	if suppressed {
		value = 1
	}
	_, err := tx.ExecContext(ctx, "UPDATE "+quoteIdentifier(captureStateTable)+" SET "+quoteIdentifier("suppress")+" = "+fmt.Sprint(value)+" WHERE "+quoteIdentifier("id")+" = 1")
	return err
}

func changeWasApplied(ctx context.Context, tx *sql.Tx, engine Engine, change Change) (bool, error) {
	query := "SELECT 1 FROM " + quoteIdentifier(appliedChangesTable) + " WHERE " +
		quoteIdentifier("source_engine") + " = " + placeholder(engine, 1) + " AND " +
		quoteIdentifier("source_version") + " = " + placeholder(engine, 2) + " AND " +
		quoteIdentifier("sequence") + " = " + placeholder(engine, 3)
	var one int
	err := tx.QueryRowContext(ctx, query, change.Source.Engine, change.Source.Version.String(), strconv.FormatUint(change.Sequence, 10)).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return err == nil, err
}

func markChangeApplied(ctx context.Context, tx *sql.Tx, engine Engine, change Change) error {
	statement := "INSERT INTO " + quoteIdentifier(appliedChangesTable) + " (" +
		quoteIdentifier("source_engine") + ", " + quoteIdentifier("source_version") + ", " + quoteIdentifier("sequence") + ") VALUES (" +
		placeholder(engine, 1) + ", " + placeholder(engine, 2) + ", " + placeholder(engine, 3) + ")"
	_, err := tx.ExecContext(ctx, statement, change.Source.Engine, change.Source.Version.String(), strconv.FormatUint(change.Sequence, 10))
	return err
}

func applyChange(ctx context.Context, tx *sql.Tx, engine Engine, table Table, change Change) error {
	switch change.Kind {
	case Insert:
		return insertRow(ctx, tx, engine, table, change.After)
	case Update, Delete:
		actual, found, err := loadRow(ctx, tx, engine, table, change.PrimaryKey)
		if err != nil {
			return err
		}
		if !found || !rowsEqual(change.Before, actual) {
			return &ConflictError{Table: table.Name, PrimaryKey: change.PrimaryKey, Expected: change.Before, Actual: actual}
		}
		if change.Kind == Update {
			return updateRow(ctx, tx, engine, table, change.PrimaryKey, change.After)
		}
		return deleteRow(ctx, tx, engine, table, change.PrimaryKey)
	default:
		return fmt.Errorf("unsupported change kind %q", change.Kind)
	}
}

func loadRow(ctx context.Context, tx *sql.Tx, engine Engine, table Table, primaryKey Row) (Row, bool, error) {
	columns := make([]string, len(table.Columns))
	for i, column := range table.Columns {
		columns[i] = quoteIdentifier(column.Name)
	}
	where, arguments, err := rowPredicate(engine, table, primaryKey, 1)
	if err != nil {
		return nil, false, err
	}
	query := "SELECT " + strings.Join(columns, ", ") + " FROM " + quoteIdentifier(table.Name) + " WHERE " + where
	values := make([]any, len(table.Columns))
	destinations := make([]any, len(table.Columns))
	for i := range values {
		destinations[i] = &values[i]
	}
	if err := tx.QueryRowContext(ctx, query, arguments...).Scan(destinations...); err != nil {
		if err == sql.ErrNoRows {
			return nil, false, nil
		}
		return nil, false, err
	}
	row := make(Row, len(table.Columns))
	for i, column := range table.Columns {
		// Vector values carry a declared dimension in Type.Arguments; pass it so
		// canonicalValue rejects a value whose component count differs from the
		// declared dimension when reconstructing the current destination row for
		// conflict checks. Other families do not need it and omit the variadic
		// argument.
		var value Value
		var err error
		if column.Type.Family == VectorType {
			value, err = canonicalValue(column.Type.Family, values[i], column.Type.Arguments...)
		} else {
			value, err = canonicalValue(column.Type.Family, values[i])
		}
		if err != nil {
			return nil, false, err
		}
		row[column.Name] = value
	}
	return row, true, nil
}

func updateRow(ctx context.Context, tx *sql.Tx, engine Engine, table Table, primaryKey, after Row) error {
	sets := make([]string, 0, len(table.Columns))
	arguments := make([]any, 0, len(table.Columns)+len(primaryKey))
	position := 1
	for _, column := range table.Columns {
		value, exists := after[column.Name]
		if !exists {
			return fmt.Errorf("update %s missing column %q", table.Name, column.Name)
		}
		argument, err := driverValue(engine, value)
		if err != nil {
			return err
		}
		sets = append(sets, quoteIdentifier(column.Name)+" = "+placeholder(engine, position))
		arguments = append(arguments, argument)
		position++
	}
	where, whereArguments, err := rowPredicate(engine, table, primaryKey, position)
	if err != nil {
		return err
	}
	arguments = append(arguments, whereArguments...)
	result, err := tx.ExecContext(ctx, "UPDATE "+quoteIdentifier(table.Name)+" SET "+strings.Join(sets, ", ")+" WHERE "+where, arguments...)
	if err != nil {
		return err
	}
	return requireOneAffected(result, "update")
}

func deleteRow(ctx context.Context, tx *sql.Tx, engine Engine, table Table, primaryKey Row) error {
	where, arguments, err := rowPredicate(engine, table, primaryKey, 1)
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, "DELETE FROM "+quoteIdentifier(table.Name)+" WHERE "+where, arguments...)
	if err != nil {
		return err
	}
	return requireOneAffected(result, "delete")
}

func rowPredicate(engine Engine, table Table, values Row, start int) (string, []any, error) {
	var predicates []string
	var arguments []any
	position := start
	for _, column := range table.Columns {
		value, exists := values[column.Name]
		if !exists {
			continue
		}
		argument, err := driverValue(engine, value)
		if err != nil {
			return "", nil, err
		}
		if value.Kind == NullValue {
			predicates = append(predicates, quoteIdentifier(column.Name)+" IS NULL")
			continue
		}
		predicates = append(predicates, quoteIdentifier(column.Name)+" = "+placeholder(engine, position))
		arguments = append(arguments, argument)
		position++
	}
	if len(predicates) == 0 {
		return "", nil, fmt.Errorf("row predicate for %s is empty", table.Name)
	}
	return strings.Join(predicates, " AND "), arguments, nil
}

func rowsEqual(expected, actual Row) bool {
	left, err := json.Marshal(expected)
	if err != nil {
		return false
	}
	right, err := json.Marshal(actual)
	return err == nil && string(left) == string(right)
}

func requireOneAffected(result sql.Result, operation string) error {
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return fmt.Errorf("%s affected %d rows", operation, count)
	}
	return nil
}
