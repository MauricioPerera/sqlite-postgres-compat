//go:build e2e

package e2e_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"example.com/sqlite-postgres-compat/compat"
)

func cutoverSchema(name string) compat.Schema {
	return compat.Schema{Tables: []compat.Table{{
		Name: name,
		Columns: []compat.Column{
			{Name: "id", Type: compat.Type{Family: compat.IntegerType}},
			{Name: "title", Type: compat.Type{Family: compat.TextType}},
		},
		Constraints: []compat.Constraint{{Kind: compat.PrimaryKey, Columns: []string{"id"}}},
	}}}
}

// TestCutoverCLIEndToEnd drives the compat-cutover CLI as a subprocess: a SQLite
// source with data is cut over to a temporary PostgreSQL database, the process
// exits 0 with status=ready, and the migrated data is verified equivalent.
func TestCutoverCLIEndToEnd(t *testing.T) {
	ctx := context.Background()
	schema := cutoverSchema("cutover_cli_items")

	sourcePath := filepath.Join(t.TempDir(), "cutover-source.db")
	source, err := compat.OpenSQLite(compat.Version{Major: 3}, "file:"+filepath.ToSlash(sourcePath))
	if err != nil {
		t.Fatal(err)
	}
	if err := source.ApplySchema(ctx, schema); err != nil {
		t.Fatal(err)
	}
	if _, err := source.DB.ExecContext(ctx, `INSERT INTO cutover_cli_items (id, title) VALUES (?, ?), (?, ?)`, 1, "one", 2, "two"); err != nil {
		t.Fatal(err)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}

	configuration := map[string]any{
		"source_dsn":      "file:" + filepath.ToSlash(sourcePath),
		"destination_dsn": postgresTestDSN,
		"contract": compat.Contract{
			Source:      compat.Target{Engine: compat.SQLite, Version: compat.Version{Major: 3}},
			Destination: compat.Target{Engine: compat.Postgres, Version: compat.Version{Major: 17, Minor: 5}},
		},
		"schema": schema,
		"options": map[string]any{
			"poll_interval_ms": 10,
			"drain_polls":      2,
			"batch_limit":      100,
		},
	}
	data, err := json.Marshal(configuration)
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(t.TempDir(), "cutover.json")
	if err := os.WriteFile(configPath, data, 0o600); err != nil {
		t.Fatal(err)
	}

	command := exec.Command("go", "run", "./cmd/compat-cutover", configPath)
	command.Dir = ".."
	var stderr strings.Builder
	command.Stderr = &stderr
	stdout, err := command.Output()
	if err != nil {
		t.Fatalf("compat-cutover failed: %v\nstderr:\n%s", err, stderr.String())
	}
	output := stdout
	var report struct {
		Status            string `json:"status"`
		SourceDigest      string `json:"source_digest"`
		DestinationDigest string `json:"destination_digest"`
		ChangesApplied    int    `json:"changes_applied"`
	}
	if err := json.Unmarshal(output, &report); err != nil {
		t.Fatalf("invalid compat-cutover output %q: %v", output, err)
	}
	if report.Status != "ready" {
		t.Fatalf("expected status=ready, got %+v\n%s", report, output)
	}
	if report.SourceDigest == "" || report.SourceDigest != report.DestinationDigest {
		t.Fatalf("expected matching non-empty digests, got %+v", report)
	}

	postgres := openPostgres(t)
	var count int
	if err := postgres.DB.QueryRowContext(ctx, `SELECT count(*) FROM cutover_cli_items`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("expected 2 migrated rows, got %d", count)
	}

	reopened, err := compat.OpenSQLite(compat.Version{Major: 3}, "file:"+filepath.ToSlash(sourcePath))
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	assertStoreSnapshotsEquivalent(t, ctx, schema, reopened, postgres)
}

// TestCutoverOverlapTolerantResolvesInsertInSnapshot reproduces the inherent
// overlap of a zero-window cutover at the library level (SQLite -> SQLite, no
// PostgreSQL): a row inserted after capture-install is journaled AND carried
// inside the snapshot. Replaying the journal with ApplyChangesTolerant treats
// the insert as already applied, leaves the row once, and keeps the digests
// equivalent.
func TestCutoverOverlapTolerantResolvesInsertInSnapshot(t *testing.T) {
	ctx := context.Background()
	schema := cutoverSchema("overlap_tolerant_items")

	source := openSQLite(t, filepath.Join(t.TempDir(), "overlap-source.db"))
	if err := source.ApplySchema(ctx, schema); err != nil {
		t.Fatal(err)
	}
	if err := source.InstallChangeCapture(ctx, schema); err != nil {
		t.Fatal(err)
	}
	if _, err := source.DB.ExecContext(ctx, `INSERT INTO overlap_tolerant_items (id, title) VALUES (1, 'X')`); err != nil {
		t.Fatal(err)
	}

	snapshot, err := source.ExportSnapshot(ctx, schema)
	if err != nil {
		t.Fatal(err)
	}
	destination := openSQLite(t, filepath.Join(t.TempDir(), "overlap-destination.db"))
	if err := destination.ImportSnapshot(ctx, snapshot); err != nil {
		t.Fatal(err)
	}

	changes, err := source.ReadCapturedChanges(ctx, schema, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 1 || changes[0].Kind != compat.Insert {
		t.Fatalf("expected a single captured insert, got %+v", changes)
	}
	if err := destination.ApplyChangesTolerant(ctx, schema, changes); err != nil {
		t.Fatalf("tolerant catch-up of an insert already in the snapshot must not conflict: %v", err)
	}

	var count int
	if err := destination.DB.QueryRowContext(ctx, `SELECT count(*) FROM overlap_tolerant_items WHERE id = 1`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected the overlapped row to remain exactly once, got %d", count)
	}
	assertStoreSnapshotsEquivalent(t, ctx, schema, source, destination)
}

// TestCutoverOverlapStrictFailsOnInsertInSnapshot is the same overlap scenario
// under ApplyChanges strict mode. The strict insert path does not preload the
// row, so re-inserting a row already carried by the snapshot trips the primary
// key's unique constraint. This is the spurious overlap failure the tolerant
// mode exists to resolve; for a pure insert it surfaces as a unique-constraint
// error rather than a ConflictError (see TestCutoverOverlapUpdateStrictConflicts
// for the stale-Before update that produces a true ConflictError).
func TestCutoverOverlapStrictFailsOnInsertInSnapshot(t *testing.T) {
	ctx := context.Background()
	schema := cutoverSchema("overlap_strict_items")

	source := openSQLite(t, filepath.Join(t.TempDir(), "overlap-strict-source.db"))
	if err := source.ApplySchema(ctx, schema); err != nil {
		t.Fatal(err)
	}
	if err := source.InstallChangeCapture(ctx, schema); err != nil {
		t.Fatal(err)
	}
	if _, err := source.DB.ExecContext(ctx, `INSERT INTO overlap_strict_items (id, title) VALUES (1, 'X')`); err != nil {
		t.Fatal(err)
	}
	snapshot, err := source.ExportSnapshot(ctx, schema)
	if err != nil {
		t.Fatal(err)
	}
	destination := openSQLite(t, filepath.Join(t.TempDir(), "overlap-strict-destination.db"))
	if err := destination.ImportSnapshot(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	changes, err := source.ReadCapturedChanges(ctx, schema, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if err := destination.ApplyChanges(ctx, schema, changes); err == nil {
		t.Fatal("strict catch-up of an insert already in the snapshot must fail")
	}
}

// TestCutoverOverlapUpdateStrictConflicts demonstrates the precise conflict the
// tolerant mode resolves: a row updated during the overlap window has a stale
// Before (the pre-update state) while the snapshot already carries the after
// state. Strict ApplyChanges raises a ConflictError because Before no longer
// matches the destination; ApplyChangesTolerant sees the after state already
// matches and treats the update as already applied.
func TestCutoverOverlapUpdateStrictConflicts(t *testing.T) {
	ctx := context.Background()
	schema := cutoverSchema("overlap_update_items")

	source := openSQLite(t, filepath.Join(t.TempDir(), "overlap-update-source.db"))
	if err := source.ApplySchema(ctx, schema); err != nil {
		t.Fatal(err)
	}
	if _, err := source.DB.ExecContext(ctx, `INSERT INTO overlap_update_items (id, title) VALUES (1, 'first')`); err != nil {
		t.Fatal(err)
	}
	if err := source.InstallChangeCapture(ctx, schema); err != nil {
		t.Fatal(err)
	}
	if _, err := source.DB.ExecContext(ctx, `UPDATE overlap_update_items SET title = 'updated' WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	snapshot, err := source.ExportSnapshot(ctx, schema)
	if err != nil {
		t.Fatal(err)
	}
	changes, err := source.ReadCapturedChanges(ctx, schema, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 1 || changes[0].Kind != compat.Update {
		t.Fatalf("expected a single captured update, got %+v", changes)
	}

	strict := openSQLite(t, filepath.Join(t.TempDir(), "overlap-update-strict.db"))
	if err := strict.ImportSnapshot(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	if err := strict.ApplyChanges(ctx, schema, changes); err == nil {
		t.Fatal("strict catch-up of a stale-Before update must fail")
	} else {
		var conflict *compat.ConflictError
		if !errors.As(err, &conflict) {
			t.Fatalf("expected a ConflictError from strict catch-up, got %v", err)
		}
	}

	tolerant := openSQLite(t, filepath.Join(t.TempDir(), "overlap-update-tolerant.db"))
	if err := tolerant.ImportSnapshot(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	if err := tolerant.ApplyChangesTolerant(ctx, schema, changes); err != nil {
		t.Fatalf("tolerant catch-up of an update already in the snapshot must not conflict: %v", err)
	}
	assertStoreSnapshotsEquivalent(t, ctx, schema, source, tolerant)
}

// TestCutoverTolerantGenuineDivergenceStillConflicts confirms the tolerant mode
// is not a bypass: when the destination's current state diverges from the
// change's final state, a strict ConflictError is still raised.
func TestCutoverTolerantGenuineDivergenceStillConflicts(t *testing.T) {
	ctx := context.Background()
	schema := cutoverSchema("overlap_divergence_items")

	destination := openSQLite(t, filepath.Join(t.TempDir(), "divergence-destination.db"))
	if err := destination.ApplySchema(ctx, schema); err != nil {
		t.Fatal(err)
	}
	if _, err := destination.DB.ExecContext(ctx, `INSERT INTO overlap_divergence_items (id, title) VALUES (1, 'local')`); err != nil {
		t.Fatal(err)
	}
	change := compat.Change{
		Source:     compat.Target{Engine: compat.Postgres, Version: compat.Version{Major: 17}},
		Sequence:   1,
		Kind:       compat.Insert,
		Table:      "overlap_divergence_items",
		PrimaryKey: compat.Row{"id": {Kind: compat.IntegerValue, Value: "1"}},
		After:      compat.Row{"id": {Kind: compat.IntegerValue, Value: "1"}, "title": {Kind: compat.TextValue, Value: "remote"}},
	}
	err := destination.ApplyChangesTolerant(ctx, schema, []compat.Change{change})
	var conflict *compat.ConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("expected ConflictError for genuine divergence under tolerant mode, got %v", err)
	}
}