//go:build e2e

package e2e_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
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

// runCLI runs a compat CLI as a subprocess from the repo root and returns its
// stdout, stderr, and exit code. Unlike exec.Command.Output, it returns the
// captured stdout even when the process exits non-zero.
func runCLI(t *testing.T, args ...string) (stdout, stderr string, exitCode int) {
	t.Helper()
	command := exec.Command("go", append([]string{"run"}, args...)...)
	command.Dir = ".."
	var out, errbuf strings.Builder
	command.Stdout = &out
	command.Stderr = &errbuf
	err := command.Run()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			t.Fatalf("failed to run CLI %v: %v\nstderr:\n%s", args, err, errbuf.String())
		}
	}
	return out.String(), errbuf.String(), exitCode
}

// firstErrorJSONLine scans a CLI stdout (which may contain a findings array
// followed by an error object) and returns the parsed error envelope, or fails
// the test if no single-line error JSON is present.
func firstErrorJSONLine(t *testing.T, stdout string) map[string]any {
	t.Helper()
	for _, line := range strings.Split(strings.TrimSpace(stdout), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || !strings.HasPrefix(line, "{") {
			continue
		}
		var parsed map[string]any
		if err := json.Unmarshal([]byte(line), &parsed); err != nil {
			continue
		}
		if status, _ := parsed["status"].(string); status == "error" {
			return parsed
		}
	}
	t.Fatalf("no error JSON line in stdout:\n%s", stdout)
	return nil
}

// TestDryRunCLISuccessPlan drives compat-cutover --dry-run against a real
// PostgreSQL destination: it exits 0 with a plan JSON carrying the correct
// source row counts and destination_has_tables=false, and it writes NOTHING to
// either store (no capture triggers, no journal, no destination tables).
func TestDryRunCLISuccessPlan(t *testing.T) {
	ctx := context.Background()
	const table = "dryrun_plan_items"
	schema := cutoverSchema(table)

	sourcePath := filepath.Join(t.TempDir(), "dryrun-source.db")
	source, err := compat.OpenSQLite(compat.Version{Major: 3}, "file:"+filepath.ToSlash(sourcePath))
	if err != nil {
		t.Fatal(err)
	}
	if err := source.ApplySchema(ctx, schema); err != nil {
		t.Fatal(err)
	}
	if _, err := source.DB.ExecContext(ctx, `INSERT INTO dryrun_plan_items (id, title) VALUES (?, ?), (?, ?)`, 1, "one", 2, "two"); err != nil {
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
		"schema":  schema,
		"options": map[string]any{"poll_interval_ms": 10, "drain_polls": 2, "batch_limit": 100},
	}
	data, err := json.Marshal(configuration)
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(t.TempDir(), "cutover.json")
	if err := os.WriteFile(configPath, data, 0o600); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, exitCode := runCLI(t, "./cmd/compat-cutover", "--dry-run", configPath)
	if exitCode != 0 {
		t.Fatalf("expected exit 0, got %d\nstderr:\n%s\nstdout:\n%s", exitCode, stderr, stdout)
	}

	var plan struct {
		Status               string `json:"status"`
		SourceTables         []struct {
			Name string `json:"name"`
			Rows int    `json:"rows"`
		} `json:"source_tables"`
		DestinationHasTables bool     `json:"destination_has_tables"`
		Phases               []string `json:"phases"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &plan); err != nil {
		t.Fatalf("invalid plan JSON %q: %v", stdout, err)
	}
	if plan.Status != "plan" {
		t.Fatalf("expected status=plan, got %q\n%s", plan.Status, stdout)
	}
	if len(plan.SourceTables) != 1 || plan.SourceTables[0].Name != table || plan.SourceTables[0].Rows != 2 {
		t.Fatalf("expected source_tables=[{name=%s,rows=2}], got %+v", table, plan.SourceTables)
	}
	if plan.DestinationHasTables {
		t.Fatalf("expected destination_has_tables=false on a fresh destination, got true")
	}
	wantPhases := []string{"install_capture", "snapshot", "catch_up", "verify"}
	if len(plan.Phases) != len(wantPhases) {
		t.Fatalf("expected %d phases, got %v", len(wantPhases), plan.Phases)
	}
	for i, phase := range wantPhases {
		if plan.Phases[i] != phase {
			t.Fatalf("expected phases=%v, got %v", wantPhases, plan.Phases)
		}
	}

	// No-write assertion on the source: no capture triggers and no journal table.
	reopened, err := compat.OpenSQLite(compat.Version{Major: 3}, "file:"+filepath.ToSlash(sourcePath))
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	var triggerCount int
	if err := reopened.DB.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type = 'trigger' AND name LIKE '__compat_capture_%'`).Scan(&triggerCount); err != nil {
		t.Fatal(err)
	}
	if triggerCount != 0 {
		t.Fatalf("dry-run must not install capture triggers; found %d", triggerCount)
	}
	var journalCount int
	if err := reopened.DB.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = '__compat_change_journal'`).Scan(&journalCount); err != nil {
		t.Fatal(err)
	}
	if journalCount != 0 {
		t.Fatalf("dry-run must not create a change journal; found %d", journalCount)
	}

	// No-write assertion on the destination: the contract table must be absent.
	postgres := openPostgres(t)
	var destTableCount int
	if err := postgres.DB.QueryRowContext(ctx, `SELECT count(*) FROM information_schema.tables WHERE table_schema = 'public' AND table_name = $1`, table).Scan(&destTableCount); err != nil {
		t.Fatal(err)
	}
	if destTableCount != 0 {
		t.Fatalf("dry-run must not create destination tables; %s present", table)
	}
}

// TestDryRunCLIInvalidConfigError verifies that an unreadable JSON config is
// reported as a typed ERR_CONFIG error on stdout with exit 1.
func TestDryRunCLIInvalidConfigError(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(configPath, []byte("{not valid json"), 0o600); err != nil {
		t.Fatal(err)
	}

	stdout, _, exitCode := runCLI(t, "./cmd/compat-cutover", "--dry-run", configPath)
	if exitCode != 1 {
		t.Fatalf("expected exit 1 for invalid config, got %d\nstdout:\n%s", exitCode, stdout)
	}
	parsed := firstErrorJSONLine(t, stdout)
	if code, _ := parsed["code"].(string); code != "ERR_CONFIG" {
		t.Fatalf("expected code=ERR_CONFIG, got %v\nstdout:\n%s", parsed["code"], stdout)
	}
}

// TestDryRunCLIUnreachableDestinationError verifies that a destination DSN that
// cannot be reached is reported as ERR_CONNECT_DESTINATION with exit 1, and that
// no write occurs on the source before the failure.
func TestDryRunCLIUnreachableDestinationError(t *testing.T) {
	ctx := context.Background()
	const table = "dryrun_unreach_items"
	schema := cutoverSchema(table)

	sourcePath := filepath.Join(t.TempDir(), "dryrun-unreach-source.db")
	source, err := compat.OpenSQLite(compat.Version{Major: 3}, "file:"+filepath.ToSlash(sourcePath))
	if err != nil {
		t.Fatal(err)
	}
	if err := source.ApplySchema(ctx, schema); err != nil {
		t.Fatal(err)
	}
	if _, err := source.DB.ExecContext(ctx, `INSERT INTO dryrun_unreach_items (id, title) VALUES (?, ?)`, 1, "one"); err != nil {
		t.Fatal(err)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}

	// Port 1 is privileged and unlistened; connect_timeout bounds the wait.
	const unreachableDSN = "postgres://postgres:x@127.0.0.1:1/postgres?sslmode=disable&connect_timeout=3"
	configuration := map[string]any{
		"source_dsn":      "file:" + filepath.ToSlash(sourcePath),
		"destination_dsn": unreachableDSN,
		"contract": compat.Contract{
			Source:      compat.Target{Engine: compat.SQLite, Version: compat.Version{Major: 3}},
			Destination: compat.Target{Engine: compat.Postgres, Version: compat.Version{Major: 17, Minor: 5}},
		},
		"schema": schema,
	}
	data, err := json.Marshal(configuration)
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(t.TempDir(), "cutover.json")
	if err := os.WriteFile(configPath, data, 0o600); err != nil {
		t.Fatal(err)
	}

	stdout, _, exitCode := runCLI(t, "./cmd/compat-cutover", "--dry-run", configPath)
	if exitCode != 1 {
		t.Fatalf("expected exit 1 for unreachable destination, got %d\nstdout:\n%s", exitCode, stdout)
	}
	parsed := firstErrorJSONLine(t, stdout)
	if code, _ := parsed["code"].(string); code != "ERR_CONNECT_DESTINATION" {
		t.Fatalf("expected code=ERR_CONNECT_DESTINATION, got %v\nstdout:\n%s", parsed["code"], stdout)
	}

	// The source must be untouched: the failure happens before any write phase.
	reopened, err := compat.OpenSQLite(compat.Version{Major: 3}, "file:"+filepath.ToSlash(sourcePath))
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	var triggerCount int
	if err := reopened.DB.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type = 'trigger' AND name LIKE '__compat_capture_%'`).Scan(&triggerCount); err != nil {
		t.Fatal(err)
	}
	if triggerCount != 0 {
		t.Fatalf("no capture triggers must exist when the destination is unreachable; found %d", triggerCount)
	}
}

// TestCutoverAuditCLIErrorCodeOnNotExact verifies that compat-audit emits a
// typed ERR_AUDIT_NOT_EXACT error JSON (in addition to its findings array) and
// exits 1 when a required feature is not exact. It needs no PostgreSQL.
func TestCutoverAuditCLIErrorCodeOnNotExact(t *testing.T) {
	contract := map[string]any{
		"source":           map[string]any{"engine": "sqlite", "version": map[string]any{"major": 3, "minor": 45, "patch": 0}},
		"destination":      map[string]any{"engine": "postgres", "version": map[string]any{"major": 17, "minor": 0, "patch": 0}},
		"required_features": []string{"tables", "views"},
	}
	data, err := json.Marshal(contract)
	if err != nil {
		t.Fatal(err)
	}
	contractPath := filepath.Join(t.TempDir(), "contract.json")
	if err := os.WriteFile(contractPath, data, 0o600); err != nil {
		t.Fatal(err)
	}

	stdout, _, exitCode := runCLI(t, "./cmd/compat-audit", contractPath)
	if exitCode != 1 {
		t.Fatalf("expected exit 1 for non-exact audit, got %d\nstdout:\n%s", exitCode, stdout)
	}

	// The findings array is emitted first; the typed error JSON follows it.
	var findings []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(stdout), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") {
			if err := json.Unmarshal([]byte(line), &findings); err != nil {
				t.Fatalf("invalid findings JSON %q: %v", line, err)
			}
			break
		}
	}
	if findings == nil {
		t.Fatalf("no findings array in audit stdout:\n%s", stdout)
	}
	parsed := firstErrorJSONLine(t, stdout)
	if code, _ := parsed["code"].(string); code != "ERR_AUDIT_NOT_EXACT" {
		t.Fatalf("expected code=ERR_AUDIT_NOT_EXACT, got %v\nstdout:\n%s", parsed["code"], stdout)
	}
}

// builtBinaries caches a compiled CLI executable per package across tests in the
// process. `go run` collapses every non-zero subprocess exit code to 1 on
// Windows, so it cannot reveal the ERR_USAGE exit-2 path; building the binary
// once and execing it directly yields the real process exit code. The binaries
// live in an OS temp dir (not t.TempDir) so they survive across tests; the OS
// reaps its temp dir.
var (
	builtOnce sync.Map // pkg -> *sync.Once
	builtPath sync.Map // pkg -> binary path
)

func builtCLI(t *testing.T, pkg string) string {
	t.Helper()
	once, _ := builtOnce.LoadOrStore(pkg, &sync.Once{})
	once.(*sync.Once).Do(func() {
		dir, err := os.MkdirTemp("", "compat-cli-*")
		if err != nil {
			t.Fatal(err)
		}
		name := "cli"
		if runtime.GOOS == "windows" {
			name = "cli.exe"
		}
		bin := filepath.Join(dir, name)
		build := exec.Command("go", "build", "-o", bin, "./"+pkg)
		build.Dir = ".."
		if out, err := build.CombinedOutput(); err != nil {
			t.Fatalf("build %s: %v\n%s", pkg, err, out)
		}
		builtPath.Store(pkg, bin)
	})
	p, _ := builtPath.Load(pkg)
	return p.(string)
}

// runBuiltCLI builds pkg (e.g. "cmd/compat-cutover") and runs the resulting binary
// with args, returning stdout, stderr and the real process exit code.
func runBuiltCLI(t *testing.T, pkg string, args ...string) (stdout, stderr string, exitCode int) {
	t.Helper()
	bin := builtCLI(t, pkg)
	command := exec.Command(bin, args...)
	var out, errbuf strings.Builder
	command.Stdout = &out
	command.Stderr = &errbuf
	err := command.Run()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			t.Fatalf("run %s: %v\nstderr:\n%s", pkg, err, errbuf.String())
		}
	}
	return out.String(), errbuf.String(), exitCode
}

// TestCLIRejectsUnknownFlagAsUsage verifies that an unrecognized flag (a token
// starting with "-" that is not a known flag like --dry-run) is reported as
// ERR_USAGE with exit 2 across all three CLIs, instead of being treated as the
// positional config path (the prior ERR_CONFIG/exit-1 behavior).
func TestCLIRejectsUnknownFlagAsUsage(t *testing.T) {
	for _, tc := range []struct {
		name string
		pkg  string
		args []string
	}{
		{"cutover", "cmd/compat-cutover", []string{"--bogus", "x.json"}},
		{"copy", "cmd/compat-copy", []string{"--bogus", "x.json"}},
		{"audit", "cmd/compat-audit", []string{"--bogus", "x.json"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stdout, _, exitCode := runBuiltCLI(t, tc.pkg, tc.args...)
			if exitCode != 2 {
				t.Fatalf("expected exit 2 for unknown flag, got %d\nstdout:\n%s", exitCode, stdout)
			}
			parsed := firstErrorJSONLine(t, stdout)
			if code, _ := parsed["code"].(string); code != "ERR_USAGE" {
				t.Fatalf("expected code=ERR_USAGE, got %v\nstdout:\n%s", parsed["code"], stdout)
			}
		})
	}
}

// writeCutoverConfig marshals config to a JSON file in dir and returns its path.
func writeCutoverConfig(t *testing.T, dir, name string, config map[string]any) string {
	t.Helper()
	data, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func cutoverContract() compat.Contract {
	return compat.Contract{
		Source:      compat.Target{Engine: compat.SQLite, Version: compat.Version{Major: 3}},
		Destination: compat.Target{Engine: compat.Postgres, Version: compat.Version{Major: 17, Minor: 5}},
	}
}

// TestCutoverRejectsUnknownConfigKey verifies that an unknown top-level key in the
// cutover config is rejected with ERR_CONFIG (exit 1) rather than silently
// dropped, via json.Decoder.DisallowUnknownFields. The decode fails before any
// store is opened.
func TestCutoverRejectsUnknownConfigKey(t *testing.T) {
	dir := t.TempDir()
	config := map[string]any{
		"source_dsn":      "file:" + filepath.ToSlash(filepath.Join(dir, "src.db")),
		"destination_dsn": postgresTestDSN,
		"contract":        cutoverContract(),
		"schema":          cutoverSchema("unknown_key_items"),
		"bogus_key":       "must be rejected",
	}
	configPath := writeCutoverConfig(t, dir, "cutover.json", config)

	stdout, _, exitCode := runBuiltCLI(t, "cmd/compat-cutover", configPath)
	if exitCode != 1 {
		t.Fatalf("expected exit 1 for unknown config key, got %d\nstdout:\n%s", exitCode, stdout)
	}
	parsed := firstErrorJSONLine(t, stdout)
	if code, _ := parsed["code"].(string); code != "ERR_CONFIG" {
		t.Fatalf("expected code=ERR_CONFIG, got %v\nstdout:\n%s", parsed["code"], stdout)
	}
}

// TestCutoverRejectsSchemaAndSchemaRef verifies that a config specifying both an
// inline schema and a schema_ref is rejected with ERR_CONFIG (exit 1): exactly
// one of the two is allowed.
func TestCutoverRejectsSchemaAndSchemaRef(t *testing.T) {
	dir := t.TempDir()
	schema := cutoverSchema("both_schema_items")
	schemaData, err := json.Marshal(schema)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "schema.json"), schemaData, 0o600); err != nil {
		t.Fatal(err)
	}
	config := map[string]any{
		"source_dsn":      "file:" + filepath.ToSlash(filepath.Join(dir, "src.db")),
		"destination_dsn": postgresTestDSN,
		"contract":        cutoverContract(),
		"schema":          schema,
		"schema_ref":      "schema.json",
	}
	configPath := writeCutoverConfig(t, dir, "cutover.json", config)

	stdout, _, exitCode := runBuiltCLI(t, "cmd/compat-cutover", configPath)
	if exitCode != 1 {
		t.Fatalf("expected exit 1 for schema + schema_ref, got %d\nstdout:\n%s", exitCode, stdout)
	}
	parsed := firstErrorJSONLine(t, stdout)
	if code, _ := parsed["code"].(string); code != "ERR_CONFIG" {
		t.Fatalf("expected code=ERR_CONFIG, got %v\nstdout:\n%s", parsed["code"], stdout)
	}
}

// TestCutoverRejectsMissingSchemaAndSchemaRef verifies that a config specifying
// neither an inline schema nor a schema_ref is rejected with ERR_CONFIG (exit 1),
// instead of running with an empty schema (the prior silent-degradation bug).
func TestCutoverRejectsMissingSchemaAndSchemaRef(t *testing.T) {
	dir := t.TempDir()
	config := map[string]any{
		"source_dsn":      "file:" + filepath.ToSlash(filepath.Join(dir, "src.db")),
		"destination_dsn": postgresTestDSN,
		"contract":        cutoverContract(),
	}
	configPath := writeCutoverConfig(t, dir, "cutover.json", config)

	stdout, _, exitCode := runBuiltCLI(t, "cmd/compat-cutover", configPath)
	if exitCode != 1 {
		t.Fatalf("expected exit 1 for missing schema/schema_ref, got %d\nstdout:\n%s", exitCode, stdout)
	}
	parsed := firstErrorJSONLine(t, stdout)
	if code, _ := parsed["code"].(string); code != "ERR_CONFIG" {
		t.Fatalf("expected code=ERR_CONFIG, got %v\nstdout:\n%s", parsed["code"], stdout)
	}
}

// TestCutoverWithSchemaRefSucceeds verifies the happy path for schema_ref: a
// cutover config that points schema_ref at a JSON file holding the canonical
// schema (resolved relative to the config file, not the cwd) runs the full
// cutover against a real PostgreSQL destination and prints status=ready with
// matching digests.
func TestCutoverWithSchemaRefSucceeds(t *testing.T) {
	ctx := context.Background()
	const table = "schemaref_items"
	schema := cutoverSchema(table)

	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "cutover-ref-source.db")
	source, err := compat.OpenSQLite(compat.Version{Major: 3}, "file:"+filepath.ToSlash(sourcePath))
	if err != nil {
		t.Fatal(err)
	}
	if err := source.ApplySchema(ctx, schema); err != nil {
		t.Fatal(err)
	}
	if _, err := source.DB.ExecContext(ctx, `INSERT INTO schemaref_items (id, title) VALUES (?, ?), (?, ?)`, 1, "one", 2, "two"); err != nil {
		t.Fatal(err)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}

	// The schema_ref points at a bare compat.Schema JSON object, sibling to the
	// config, referenced by a path relative to the config file.
	schemaData, err := json.Marshal(schema)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "schema.json"), schemaData, 0o600); err != nil {
		t.Fatal(err)
	}

	config := map[string]any{
		"source_dsn":      "file:" + filepath.ToSlash(sourcePath),
		"destination_dsn": postgresTestDSN,
		"contract":        cutoverContract(),
		"schema_ref":      "schema.json",
		"options": map[string]any{
			"poll_interval_ms": 10,
			"drain_polls":      2,
			"batch_limit":      100,
		},
	}
	configPath := writeCutoverConfig(t, dir, "cutover.json", config)

	stdout, stderr, exitCode := runBuiltCLI(t, "cmd/compat-cutover", configPath)
	if exitCode != 0 {
		t.Fatalf("expected exit 0, got %d\nstderr:\n%s\nstdout:\n%s", exitCode, stderr, stdout)
	}
	var report struct {
		Status            string `json:"status"`
		SourceDigest      string `json:"source_digest"`
		DestinationDigest string `json:"destination_digest"`
		ChangesApplied    int    `json:"changes_applied"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &report); err != nil {
		t.Fatalf("invalid cutover output %q: %v", stdout, err)
	}
	if report.Status != "ready" {
		t.Fatalf("expected status=ready, got %+v\n%s", report, stdout)
	}
	if report.SourceDigest == "" || report.SourceDigest != report.DestinationDigest {
		t.Fatalf("expected matching non-empty digests, got %+v", report)
	}

	postgres := openPostgres(t)
	var count int
	if err := postgres.DB.QueryRowContext(ctx, `SELECT count(*) FROM schemaref_items`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("expected 2 migrated rows, got %d", count)
	}
}