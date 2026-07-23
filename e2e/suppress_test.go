//go:build e2e

package e2e_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/MauricioPerera/sqlite-postgres-compat/compat"
)

// suppressSchema is a single-table schema used by the suppression e2e tests.
// Each test uses a distinct table name so the shared change journal can be
// filtered by table_name and tests do not interfere with one another.
func suppressSchema(name string) compat.Schema {
	return compat.Schema{Tables: []compat.Table{{
		Name: name,
		Columns: []compat.Column{
			{Name: "id", Type: compat.Type{Family: compat.IntegerType}},
			{Name: "title", Type: compat.Type{Family: compat.TextType}},
		},
		Constraints: []compat.Constraint{{Kind: compat.PrimaryKey, Columns: []string{"id"}}},
	}}}
}

// journalCountForTable returns the number of committed capture-journal entries
// scoped to the supplied table, isolating each test from journal rows produced
// by other tests that share the disposable database.
func journalCountForTable(t *testing.T, db *sql.DB, table string) int {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM __compat_change_journal WHERE table_name = $1`, table).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

// suppressInsertChange builds a single-row insert stream as if it originated
// from a SQLite source, the shape ApplyChanges receives during replication. The
// source version is supplied by the caller because the disposable database is
// shared across the whole e2e suite and __compat_applied_changes dedups by
// (source_engine, source_version, sequence): each test must use a version that
// no other test uses so its stream is not mistaken for an already-applied one.
func suppressInsertChange(table string, source compat.Version, id, title string) []compat.Change {
	identifier := compat.Value{Kind: compat.IntegerValue, Value: id}
	return []compat.Change{{
		Source:     compat.Target{Engine: compat.SQLite, Version: source},
		Sequence:   1,
		Kind:       compat.Insert,
		Table:      table,
		PrimaryKey: compat.Row{"id": identifier},
		After:      compat.Row{"id": identifier, "title": {Kind: compat.TextValue, Value: title}},
	}}
}

// TestSuppressIsolationDoesNotLeakAcrossConnections proves the anti-echo
// suppression is transaction-local on Postgres and cannot leak to a concurrent
// transaction under MVCC. Connection A opens a transaction and arms the
// suppression using the exact SQL the production ApplyChanges path runs
// (set_config('compat.suppress','1',true) — the public ApplyChanges API commits
// internally, so the only way to hold an armed transaction open is to replay
// that mechanism directly). While A is still uncommitted, connection B inserts
// into a capture-installed table. B's mutation must be journaled (A's
// suppression did not leak), and A's own in-transaction mutation must not be
// journaled (A's suppression is in effect on its own connection).
func TestSuppressIsolationDoesNotLeakAcrossConnections(t *testing.T) {
	ctx := context.Background()
	const table = "suppress_isolation"
	schema := suppressSchema(table)

	store := openPostgres(t)
	if err := store.ApplySchema(ctx, schema); err != nil {
		t.Fatal(err)
	}
	if err := store.InstallChangeCapture(ctx, schema); err != nil {
		t.Fatal(err)
	}
	if got := journalCountForTable(t, store.DB, table); got != 0 {
		t.Fatalf("expected clean journal, got %d entries", got)
	}

	// Separate pool for connection B so the two transactions are guaranteed to
	// land on distinct Postgres backend connections.
	other, err := sql.Open("pgx", postgresTestDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	t.Cleanup(func() { _ = other.Close() })

	// Connection A: arm the transaction-local suppression and keep the tx open.
	txA, err := store.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer txA.Rollback()
	if _, err := txA.ExecContext(ctx, "SELECT set_config('compat.suppress', '1', true)"); err != nil {
		t.Fatal(err)
	}
	// A's own write while armed must be suppressed (no journal entry).
	if _, err := txA.ExecContext(ctx, `INSERT INTO `+table+` (id, title) VALUES (10, 'from A')`); err != nil {
		t.Fatal(err)
	}

	// Connection B: an independent transaction writes and commits before A does.
	txB, err := other.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := txB.ExecContext(ctx, `INSERT INTO `+table+` (id, title) VALUES (20, 'from B')`); err != nil {
		t.Fatal(err)
	}
	if err := txB.Commit(); err != nil {
		t.Fatal(err)
	}

	// B's committed write is journaled even though A still holds an armed
	// transaction: the suppression is transaction-local and did not leak.
	if got := journalCountForTable(t, store.DB, table); got != 1 {
		t.Fatalf("B's write must be journaled despite A's armed suppression: got %d entries", got)
	}

	if err := txA.Commit(); err != nil {
		t.Fatal(err)
	}
	// After A commits, A's suppressed write still produced no journal entry.
	if got := journalCountForTable(t, store.DB, table); got != 1 {
		t.Fatalf("A's armed write must remain unjournaled after commit: got %d entries", got)
	}

	var aTitle, bTitle string
	if err := store.DB.QueryRowContext(ctx, `SELECT title FROM `+table+` WHERE id = 10`).Scan(&aTitle); err != nil {
		t.Fatal(err)
	}
	if err := store.DB.QueryRowContext(ctx, `SELECT title FROM `+table+` WHERE id = 20`).Scan(&bTitle); err != nil {
		t.Fatal(err)
	}
	if aTitle != "from A" || bTitle != "from B" {
		t.Fatalf("both writes must persist: A=%q B=%q", aTitle, bTitle)
	}
}

// TestSuppressAntiEchoOnReplicatedWrites proves ApplyChanges against a Postgres
// destination with capture installed does not journal the replicated rows (no
// echo), and that a subsequent ordinary write is journaled (positive control
// confirming the triggers are still live and only replication is suppressed).
func TestSuppressAntiEchoOnReplicatedWrites(t *testing.T) {
	ctx := context.Background()
	const table = "suppress_echo"
	schema := suppressSchema(table)

	store := openPostgres(t)
	if err := store.ApplySchema(ctx, schema); err != nil {
		t.Fatal(err)
	}
	if err := store.InstallChangeCapture(ctx, schema); err != nil {
		t.Fatal(err)
	}

	if err := store.ApplyChanges(ctx, schema, suppressInsertChange(table, compat.Version{Major: 3, Minor: 9001}, "1", "replicated")); err != nil {
		t.Fatal(err)
	}
	if got := journalCountForTable(t, store.DB, table); got != 0 {
		t.Fatalf("replicated writes must not be journaled as echo: got %d entries", got)
	}
	var title string
	if err := store.DB.QueryRowContext(ctx, `SELECT title FROM `+table+` WHERE id = 1`).Scan(&title); err != nil {
		t.Fatal(err)
	}
	if title != "replicated" {
		t.Fatalf("replicated row must persist: got %q", title)
	}

	// Positive control: an ordinary write on a non-replication connection must
	// be journaled, proving the triggers are live and the suppression only
	// covers the replication transaction.
	if _, err := store.DB.ExecContext(ctx, `INSERT INTO `+table+` (id, title) VALUES (2, 'manual')`); err != nil {
		t.Fatal(err)
	}
	if got := journalCountForTable(t, store.DB, table); got != 1 {
		t.Fatalf("ordinary write must be journaled: got %d entries", got)
	}
}

// TestSuppressReapplicationIsIdempotent proves reapplying the same replication
// stream to a Postgres destination with capture installed stays a no-op: no
// echo entries appear and the row set is unchanged.
func TestSuppressReapplicationIsIdempotent(t *testing.T) {
	ctx := context.Background()
	const table = "suppress_idempotent"
	schema := suppressSchema(table)

	store := openPostgres(t)
	if err := store.ApplySchema(ctx, schema); err != nil {
		t.Fatal(err)
	}
	if err := store.InstallChangeCapture(ctx, schema); err != nil {
		t.Fatal(err)
	}

	stream := suppressInsertChange(table, compat.Version{Major: 3, Minor: 9002}, "1", "once")
	for i := 0; i < 2; i++ {
		if err := store.ApplyChanges(ctx, schema, stream); err != nil {
			t.Fatalf("reapplication %d failed: %v", i, err)
		}
		if got := journalCountForTable(t, store.DB, table); got != 0 {
			t.Fatalf("reapplication %d must not produce echo: got %d entries", i, got)
		}
	}
	var count int
	if err := store.DB.QueryRowContext(ctx, `SELECT count(*) FROM `+table).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("idempotent reapplication must leave exactly one row, got %d", count)
	}
}
