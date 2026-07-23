package compat

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const changeJournalTable = "__compat_change_journal"

// realDecimalMarker prefixes a DECIMAL value that the SQLite capture trigger
// journaled from REAL storage (typeof(col) = 'real'). The trigger emits the
// round-trip printf('%!.17g') form ONLY on that branch and prepends this
// sentinel; every other branch (TEXT/INTEGER storage, and Postgres, whose
// numeric/float8 text already round-trips) emits the raw CAST verbatim with no
// marker. This lets canonicalValue distinguish a genuine float64 storage
// representation — which must be reconciled through normalizeFloat so a
// REAL-stored DECIMAL does not raise a spurious ConflictError — from an
// arbitrary-precision TEXT decimal, which must be preserved byte-for-byte.
//
// The marker begins with a control byte (SOH, \x01) that no legitimate decimal
// text can start with — decimal numbers begin with a digit, sign, or dot — so a
// TEXT-stored value never collides with it. It is a reserved internal sentinel
// of the capture layer, not valid user data in a DECIMAL column.
const realDecimalMarker = "\x01real"

// InstallChangeCapture installs engine-native triggers which journal every
// committed row mutation in canonical, ordered form.
func (store *Store) InstallChangeCapture(ctx context.Context, schema Schema) error {
	if err := schema.Validate(); err != nil {
		return err
	}
	tx, err := store.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := createAppliedChangesTable(ctx, tx); err != nil {
		return err
	}
	journalDDL := "CREATE TABLE IF NOT EXISTS " + quoteIdentifier(changeJournalTable) + " ("
	if store.Target.Engine == SQLite {
		journalDDL += quoteIdentifier("sequence") + " INTEGER PRIMARY KEY AUTOINCREMENT, "
	} else {
		journalDDL += quoteIdentifier("sequence") + " BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY, "
	}
	journalDDL += quoteIdentifier("committed_at") + " TEXT NOT NULL, " + quoteIdentifier("transaction_id") + " TEXT NOT NULL, " +
		quoteIdentifier("kind") + " TEXT NOT NULL, " + quoteIdentifier("table_name") + " TEXT NOT NULL, " +
		quoteIdentifier("primary_key") + " TEXT NOT NULL, " + quoteIdentifier("before_row") + " TEXT, " + quoteIdentifier("after_row") + " TEXT)"
	if _, err := tx.ExecContext(ctx, journalDDL); err != nil {
		return err
	}
	for _, table := range schema.Tables {
		primary, err := primaryKeyColumns(table)
		if err != nil {
			return err
		}
		statements := compileCaptureTriggers(store.Target.Engine, table, primary)
		for _, statement := range statements {
			if _, err := tx.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("install capture for %s: %w", table.Name, err)
			}
		}
	}
	return tx.Commit()
}

func primaryKeyColumns(table Table) ([]Column, error) {
	for _, constraint := range table.Constraints {
		if constraint.Kind != PrimaryKey {
			continue
		}
		columns := make([]Column, 0, len(constraint.Columns))
		for _, name := range constraint.Columns {
			found := false
			for _, column := range table.Columns {
				if column.Name == name {
					columns = append(columns, column)
					found = true
					break
				}
			}
			if !found {
				return nil, fmt.Errorf("table %s primary key references unknown column %s", table.Name, name)
			}
		}
		return columns, nil
	}
	return nil, fmt.Errorf("automatic capture requires a primary key on table %s", table.Name)
}

func compileCaptureTriggers(engine Engine, table Table, primary []Column) []string {
	var statements []string
	for _, mutation := range []struct {
		kind, event, primaryAlias, beforeAlias, afterAlias, returnAlias string
	}{
		{"insert", "INSERT", "NEW", "", "NEW", "NEW"},
		{"update", "UPDATE", "OLD", "OLD", "NEW", "NEW"},
		{"delete", "DELETE", "OLD", "OLD", "", "OLD"},
	} {
		triggerName := "__compat_capture_" + table.Name + "_" + mutation.kind
		primaryJSON := captureJSONExpression(engine, mutation.primaryAlias, primary)
		beforeJSON := "NULL"
		if mutation.beforeAlias != "" {
			beforeJSON = captureJSONExpression(engine, mutation.beforeAlias, table.Columns)
		}
		afterJSON := "NULL"
		if mutation.afterAlias != "" {
			afterJSON = captureJSONExpression(engine, mutation.afterAlias, table.Columns)
		}
		insert := "INSERT INTO " + quoteIdentifier(changeJournalTable) + " (" +
			quoteIdentifier("committed_at") + ", " + quoteIdentifier("transaction_id") + ", " + quoteIdentifier("kind") + ", " + quoteIdentifier("table_name") + ", " + quoteIdentifier("primary_key") + ", " + quoteIdentifier("before_row") + ", " + quoteIdentifier("after_row") + ") VALUES ("
		if engine == SQLite {
			insert += "strftime('%Y-%m-%dT%H:%M:%fZ','now'), '', "
		} else {
			insert += "to_char(clock_timestamp() AT TIME ZONE 'UTC', 'YYYY-MM-DD\"T\"HH24:MI:SS.US\"Z\"'), txid_current()::text, "
		}
		insert += sqlString(mutation.kind) + ", " + sqlString(table.Name) + ", " + primaryJSON + ", " + beforeJSON + ", " + afterJSON + ")"
		if engine == SQLite {
			statements = append(statements,
				"DROP TRIGGER IF EXISTS "+quoteIdentifier(triggerName),
				"CREATE TRIGGER "+quoteIdentifier(triggerName)+" AFTER "+mutation.event+" ON "+quoteIdentifier(table.Name)+" FOR EACH ROW WHEN (SELECT "+quoteIdentifier("suppress")+" FROM "+quoteIdentifier(captureStateTable)+" WHERE "+quoteIdentifier("id")+" = 1) = 0 BEGIN "+insert+"; END",
			)
			continue
		}
		functionName := triggerName + "_fn"
		// The capture condition reads the transaction-local GUC compat.suppress
		// instead of the __compat_capture_state table. current_setting(..., true)
		// returns NULL when the GUC has not been set (missing_ok), and NULL IS
		// DISTINCT FROM '1' is true, so ordinary writes on a connection that is
		// not mid-replication are journaled. A replication transaction sets the
		// GUC via set_config('compat.suppress','1',true), which is visible only to
		// that transaction, so a concurrent transaction's writes are still
		// journaled — the suppression cannot leak across connections under MVCC.
		statements = append(statements,
			"DROP TRIGGER IF EXISTS "+quoteIdentifier(triggerName)+" ON "+quoteIdentifier(table.Name),
			"CREATE OR REPLACE FUNCTION "+quoteIdentifier(functionName)+"() RETURNS TRIGGER LANGUAGE plpgsql AS $compat$ BEGIN IF current_setting('compat.suppress', true) IS DISTINCT FROM '1' THEN "+insert+"; END IF; RETURN "+mutation.returnAlias+"; END $compat$",
			"CREATE TRIGGER "+quoteIdentifier(triggerName)+" AFTER "+mutation.event+" ON "+quoteIdentifier(table.Name)+" FOR EACH ROW EXECUTE FUNCTION "+quoteIdentifier(functionName)+"()",
		)
	}
	return statements
}

func captureJSONExpression(engine Engine, alias string, columns []Column) string {
	arguments := make([]string, 0, len(columns)*2)
	for _, column := range columns {
		arguments = append(arguments, sqlString(column.Name))
		value := alias + "." + quoteIdentifier(column.Name)
		encoded := "CAST(" + value + " AS TEXT)"
		switch column.Type.Family {
		case BinaryType:
			if engine == SQLite {
				encoded = "hex(" + value + ")"
			} else {
				encoded = "encode(" + value + ", 'hex')"
			}
		case FloatType:
			// SQLite's CAST(REAL AS TEXT) renders with ~15 significant digits and
			// silently loses precision: CAST(1.2345678901234567e+14 AS TEXT) yields
			// "123456789012346.0", so the journal would store a different float64
			// than the source and the destination driver would later reconstruct.
			// printf('%!.17g', x) emits the 17-significant-digit form that round-trips
			// a float64 (the IEEE-754 double round-trip bound); canonicalValue's
			// normalizeFloat then maps both the capture text and the destination
			// driver's float64 to the same shortest form. PostgreSQL's float8-to-text
			// already emits the shortest form that round-trips, so CAST is sufficient
			// there (verified by reading PostgreSQL's float8out; no live Postgres was
			// available in this environment to execute it).
			if engine == SQLite {
				encoded = "printf('%!.17g', " + value + ")"
			}
		case DecimalType:
			// A DECIMAL column on a native SQLite table with NUMERIC affinity stores
			// fractional values as REAL, so CAST(col AS TEXT) truncates to ~15
			// significant digits while the destination driver reconstructs the full
			// float64, raising a spurious ConflictError. Emit the round-trip
			// printf('%!.17g') form ONLY when the storage is REAL (typeof(col) =
			// 'real') and prefix it with the reserved realDecimalMarker so
			// canonicalValue can tell a REAL-stored value apart from an
			// arbitrary-precision TEXT decimal; arbitrary-precision values stored as
			// TEXT or INTEGER pass through CAST verbatim, preserving every digit
			// (e.g. a 38-digit decimal). PostgreSQL NUMERIC preserves arbitrary
			// precision in CAST, so no special-casing is needed there.
			if engine == SQLite {
				encoded = "CASE typeof(" + value + ") WHEN 'real' THEN " + sqlString(realDecimalMarker) + " || printf('%!.17g', " + value + ") ELSE CAST(" + value + " AS TEXT) END"
			}
		}
		arguments = append(arguments, "CASE WHEN "+value+" IS NULL THEN NULL ELSE "+encoded+" END")
	}
	function := "json_object"
	if engine == Postgres {
		function = "json_build_object"
	}
	return "CAST(" + function + "(" + strings.Join(arguments, ", ") + ") AS TEXT)"
}

func sqlString(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

// ReadCapturedChanges reads an ordered source stream after the supplied cursor.
func (store *Store) ReadCapturedChanges(ctx context.Context, schema Schema, after uint64, limit int) ([]Change, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("capture read limit must be positive")
	}
	query := "SELECT " + quoteIdentifier("sequence") + ", " + quoteIdentifier("committed_at") + ", " + quoteIdentifier("transaction_id") + ", " + quoteIdentifier("kind") + ", " + quoteIdentifier("table_name") + ", " + quoteIdentifier("primary_key") + ", " + quoteIdentifier("before_row") + ", " + quoteIdentifier("after_row") + " FROM " + quoteIdentifier(changeJournalTable) + " WHERE " + quoteIdentifier("sequence") + " > " + placeholder(store.Target.Engine, 1) + " ORDER BY " + quoteIdentifier("sequence") + " LIMIT " + placeholder(store.Target.Engine, 2)
	rows, err := store.DB.QueryContext(ctx, query, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var changes []Change
	for rows.Next() {
		var sequence uint64
		var committed, transactionID, kind, tableName, primaryJSON string
		var beforeJSON, afterJSON sql.NullString
		if err := rows.Scan(&sequence, &committed, &transactionID, &kind, &tableName, &primaryJSON, &beforeJSON, &afterJSON); err != nil {
			return nil, err
		}
		table, err := findTable(schema, tableName)
		if err != nil {
			return nil, err
		}
		primary, err := decodeCapturedRow(primaryJSON, table, false)
		if err != nil {
			return nil, err
		}
		change := Change{Source: store.Target, Sequence: sequence, Kind: ChangeKind(kind), Table: tableName, PrimaryKey: primary, TransactionID: transactionID}
		change.CommittedAt, err = time.Parse(time.RFC3339Nano, committed)
		if err != nil {
			return nil, err
		}
		if beforeJSON.Valid {
			change.Before, err = decodeCapturedRow(beforeJSON.String, table, true)
			if err != nil {
				return nil, err
			}
		}
		if afterJSON.Valid {
			change.After, err = decodeCapturedRow(afterJSON.String, table, true)
			if err != nil {
				return nil, err
			}
		}
		if err := change.Validate(); err != nil {
			return nil, err
		}
		changes = append(changes, change)
	}
	return changes, rows.Err()
}

func decodeCapturedRow(payload string, table Table, requireAllColumns bool) (Row, error) {
	var raw map[string]*string
	if err := json.Unmarshal([]byte(payload), &raw); err != nil {
		return nil, err
	}
	row := make(Row, len(raw))
	for _, column := range table.Columns {
		value, exists := raw[column.Name]
		if !exists {
			// The primary-key payload is intentionally a subset (only the key
			// columns are journaled), so a missing non-key column is expected there
			// and requireAllColumns is false. Full before/after rows are emitted by
			// captureJSONExpression with every table column, so a missing key there
			// means the journal payload does not match the schema (e.g. replaying a
			// stale capture against a table that later gained a column). Surface it
			// explicitly rather than silently dropping the column, which would let a
			// partial row enter the replication stream.
			if requireAllColumns {
				return nil, fmt.Errorf("captured row for table %q is missing column %q", table.Name, column.Name)
			}
			continue
		}
		if value == nil {
			row[column.Name] = Value{Kind: NullValue}
			continue
		}
		var source any = *value
		if column.Type.Family == BinaryType {
			decoded, err := hex.DecodeString(*value)
			if err != nil {
				return nil, err
			}
			source = decoded
		}
		// Vector values carry a declared dimension in Type.Arguments; pass it so
		// canonicalValue rejects a value whose component count differs from the
		// declared dimension before it enters the replication stream. Other
		// families do not need it and omit the variadic argument.
		var canonical Value
		var err error
		if column.Type.Family == VectorType {
			canonical, err = canonicalValue(column.Type.Family, source, column.Type.Arguments...)
		} else {
			canonical, err = canonicalValue(column.Type.Family, source)
		}
		if err != nil {
			return nil, err
		}
		row[column.Name] = canonical
	}
	return row, nil
}
