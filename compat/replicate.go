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
	return store.applyChanges(ctx, schema, changes, false)
}

// ApplyChangesTolerant applies one ordered source stream atomically with an
// opt-in, catch-up conflict policy. It exists for zero-window migrations whose
// capture-install → snapshot → catch-up sequence inherently overlaps: a change
// journaled after capture was installed may already have traveled inside the
// snapshot, so re-applying it would trip a spurious ConflictError even though
// the destination already reflects the change's final state.
//
// The only difference from ApplyChanges is conflict resolution. A change is
// treated as already applied — and recorded in __compat_applied_changes as if
// it had just been applied — when the destination's CURRENT state already
// equals the change's FINAL state:
//
//   - insert: the row already exists and rowsEqual(after, actual);
//   - update: the row exists and rowsEqual(after, actual) even though Before no
//     longer matches (the snapshot already carried the after state);
//   - delete: the row no longer exists.
//
// Any other divergence remains a strict ConflictError: the tolerant mode is a
// catch-up convenience, not a bypass. ApplyChanges is unchanged.
func (store *Store) ApplyChangesTolerant(ctx context.Context, schema Schema, changes []Change) error {
	return store.applyChanges(ctx, schema, changes, true)
}

func (store *Store) applyChanges(ctx context.Context, schema Schema, changes []Change, tolerant bool) error {
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
	if err := setCaptureSuppressed(ctx, tx, store.Target.Engine, true); err != nil {
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
		if err := applyChange(ctx, tx, store.Target.Engine, table, change, tolerant); err != nil {
			return err
		}
		if err := markChangeApplied(ctx, tx, store.Target.Engine, change); err != nil {
			return err
		}
	}
	if err := setCaptureSuppressed(ctx, tx, store.Target.Engine, false); err != nil {
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
	// The single-row capture-state table is the suppression flag on SQLite: its
	// triggers consult suppress=0/1 in their WHEN clause. On Postgres the
	// triggers no longer read this table (they read the transaction-local GUC
	// compat.suppress instead), but the table is still created on both engines to
	// keep this helper engine-agnostic and the diff minimal. It is simply unused
	// on the Postgres branch.
	state := "CREATE TABLE IF NOT EXISTS " + quoteIdentifier(captureStateTable) + " (" + quoteIdentifier("id") + " INTEGER PRIMARY KEY, " + quoteIdentifier("suppress") + " INTEGER NOT NULL)"
	if _, err := tx.ExecContext(ctx, state); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO "+quoteIdentifier(captureStateTable)+" ("+quoteIdentifier("id")+", "+quoteIdentifier("suppress")+") VALUES (1, 0) ON CONFLICT ("+quoteIdentifier("id")+") DO NOTHING"); err != nil {
		return err
	}
	return nil
}

// setCaptureSuppressed arms or disarms the anti-echo suppression that stops the
// destination's capture triggers from journaling the rows a replication writes.
//
// On SQLite the flag lives in the single-row __compat_capture_state table, which
// is safe because SQLite is constrained to a single connection
// (OpenStore.SetMaxOpenConns(1)), so no second ApplyChanges transaction can run
// concurrently. On Postgres that constraint does not hold: under MVCC a second
// concurrent ApplyChanges transaction would not see the uncommitted suppress=1
// of the first and its triggers would journal the echo, breaking idempotency.
// The Postgres branch therefore sets a transaction-local GUC instead —
// set_config('compat.suppress','1',true) is invisible to other transactions and
// resets itself on COMMIT/ROLLBACK. Clearing it is a no-op on Postgres: the
// reset is automatic at transaction end, and no writes happen between the clear
// and the commit, so leaving the GUC set until commit is harmless.
func setCaptureSuppressed(ctx context.Context, tx *sql.Tx, engine Engine, suppressed bool) error {
	if engine == Postgres {
		if !suppressed {
			return nil
		}
		_, err := tx.ExecContext(ctx, "SELECT set_config('compat.suppress', '1', true)")
		return err
	}
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

func applyChange(ctx context.Context, tx *sql.Tx, engine Engine, table Table, change Change, tolerant bool) error {
	switch change.Kind {
	case Insert:
		// In tolerant mode a row that already matches the change's final state
		// was carried inside the snapshot (the overlap window): treat it as
		// already applied. A row that exists but diverges is a genuine conflict.
		if tolerant {
			actual, found, err := loadRow(ctx, tx, engine, table, change.PrimaryKey)
			if err != nil {
				return err
			}
			if found {
				if rowsEqual(change.After, actual) {
					return nil
				}
				return &ConflictError{Table: table.Name, PrimaryKey: change.PrimaryKey, Expected: change.After, Actual: actual}
			}
		}
		// Strict mode inserts directly: a row whose primary key already exists
		// trips a raw driver uniqueness violation, which is opaque to callers.
		// Wrap that case as a typed ConflictError naming the table and primary key
		// (and the divergent rows when the collision is observable) so it can be
		// distinguished from other driver errors; it remains a hard error, so the
		// strict semantics are preserved. Any other insert failure is surfaced as-is.
		if err := insertRow(ctx, tx, engine, table, change.After); err != nil {
			actual, found, lookupErr := loadRow(ctx, tx, engine, table, change.PrimaryKey)
			if lookupErr == nil && found {
				return &ConflictError{Table: table.Name, PrimaryKey: change.PrimaryKey, Expected: change.After, Actual: actual}
			}
			return err
		}
		return nil
	case Update, Delete:
		actual, found, err := loadRow(ctx, tx, engine, table, change.PrimaryKey)
		if err != nil {
			return err
		}
		if tolerant {
			// Update whose final state already matches the destination was
			// carried inside the snapshot even though Before no longer matches.
			if change.Kind == Update && found && rowsEqual(change.After, actual) {
				return nil
			}
			// Delete whose row is already gone was carried inside the snapshot.
			if change.Kind == Delete && !found {
				return nil
			}
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
