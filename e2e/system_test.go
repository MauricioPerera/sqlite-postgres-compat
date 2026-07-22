//go:build e2e

package e2e_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"example.com/sqlite-postgres-compat/compat"
)

var postgresTestDSN string

func TestMain(m *testing.M) {
	adminDSN := os.Getenv("COMPAT_POSTGRES_DSN")
	if adminDSN == "" {
		fmt.Fprintln(os.Stderr, "COMPAT_POSTGRES_DSN is required for end-to-end tests")
		os.Exit(2)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	admin, err := sql.Open("pgx", adminDSN)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if err := admin.PingContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}

	databaseName := fmt.Sprintf("compat_e2e_%d", time.Now().UnixNano())
	if _, err := admin.ExecContext(ctx, `CREATE DATABASE "`+databaseName+`"`); err != nil {
		fmt.Fprintln(os.Stderr, err)
		admin.Close()
		os.Exit(2)
	}
	postgresTestDSN, err = databaseDSN(adminDSN, databaseName)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		admin.Close()
		os.Exit(2)
	}

	code := m.Run()
	_, _ = admin.ExecContext(context.Background(), `SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = $1`, databaseName)
	if _, err := admin.ExecContext(context.Background(), `DROP DATABASE "`+databaseName+`"`); err != nil {
		fmt.Fprintln(os.Stderr, "cleanup:", err)
		code = 1
	}
	admin.Close()
	os.Exit(code)
}

func databaseDSN(dsn, database string) (string, error) {
	parsed, err := url.Parse(dsn)
	if err != nil {
		return "", err
	}
	parsed.Path = "/" + database
	return parsed.String(), nil
}

func TestSystemPortableCoreRoundTripSQLitePostgresSQLite(t *testing.T) {
	ctx := context.Background()
	schema := compat.Schema{Tables: []compat.Table{{
		Name: "portable_core",
		Columns: []compat.Column{
			{Name: "id", Type: compat.Type{Family: compat.IntegerType}},
			{Name: "title", Type: compat.Type{Family: compat.TextType}},
			{Name: "enabled", Type: compat.Type{Family: compat.BooleanType}},
			{Name: "payload", Type: compat.Type{Family: compat.BinaryType}, Nullable: true},
		},
		Constraints: []compat.Constraint{{Kind: compat.PrimaryKey, Columns: []string{"id"}}},
	}}}

	source := openSQLite(t, filepath.Join(t.TempDir(), "source.db"))
	if err := source.ApplySchema(ctx, schema); err != nil {
		t.Fatal(err)
	}
	if _, err := source.DB.ExecContext(ctx, `INSERT INTO portable_core (id, title, enabled, payload) VALUES (?, ?, ?, ?), (?, ?, ?, ?)`,
		1, "áéí 🚀", 1, []byte{0, 1, 2, 255}, 2, "second", 0, nil); err != nil {
		t.Fatal(err)
	}

	sourceSnapshot, err := source.ExportSnapshot(ctx, schema)
	if err != nil {
		t.Fatal(err)
	}
	postgres := openPostgres(t)
	if err := postgres.ImportSnapshot(ctx, sourceSnapshot); err != nil {
		t.Fatal(err)
	}
	postgresSnapshot, err := postgres.ExportSnapshot(ctx, schema)
	if err != nil {
		t.Fatal(err)
	}
	assertEquivalent(t, sourceSnapshot, postgresSnapshot)

	returnSQLite := openSQLite(t, filepath.Join(t.TempDir(), "return.db"))
	if err := returnSQLite.ImportSnapshot(ctx, postgresSnapshot); err != nil {
		t.Fatal(err)
	}
	returnSnapshot, err := returnSQLite.ExportSnapshot(ctx, schema)
	if err != nil {
		t.Fatal(err)
	}
	assertEquivalent(t, sourceSnapshot, returnSnapshot)
}

func TestSystemCompatCopyCLIEndToEnd(t *testing.T) {
	ctx := context.Background()
	schema := compat.Schema{Tables: []compat.Table{{
		Name: "cli_roundtrip",
		Columns: []compat.Column{
			{Name: "id", Type: compat.Type{Family: compat.IntegerType}},
			{Name: "title", Type: compat.Type{Family: compat.TextType}},
		},
		Constraints: []compat.Constraint{{Kind: compat.PrimaryKey, Columns: []string{"id"}}},
	}}}

	sourcePath := filepath.Join(t.TempDir(), "cli-source.db")
	source, err := compat.OpenSQLite(compat.Version{Major: 3}, "file:"+filepath.ToSlash(sourcePath))
	if err != nil {
		t.Fatal(err)
	}
	if err := source.ApplySchema(ctx, schema); err != nil {
		t.Fatal(err)
	}
	if _, err := source.DB.ExecContext(ctx, `INSERT INTO cli_roundtrip (id, title) VALUES (?, ?), (?, ?)`, 1, "one", 2, "two"); err != nil {
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
	}
	data, err := json.Marshal(configuration)
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(t.TempDir(), "migration.json")
	if err := os.WriteFile(configPath, data, 0o600); err != nil {
		t.Fatal(err)
	}

	command := exec.Command("go", "run", "./cmd/compat-copy", configPath)
	command.Dir = ".."
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("compat-copy failed: %v\n%s", err, output)
	}
	var report compat.VerificationReport
	if err := json.Unmarshal(output, &report); err != nil {
		t.Fatalf("invalid compat-copy output %q: %v", output, err)
	}
	if !report.Equivalent {
		t.Fatalf("compat-copy reported non-equivalent snapshots: %+v", report)
	}

	postgres := openPostgres(t)
	var count int
	if err := postgres.DB.QueryRowContext(ctx, `SELECT count(*) FROM cli_roundtrip`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("expected 2 migrated rows, got %d", count)
	}
}

func TestSystemPreservesArbitraryPrecisionDecimals(t *testing.T) {
	ctx := context.Background()
	schema := compat.Schema{Tables: []compat.Table{{
		Name: "decimal_precision",
		Columns: []compat.Column{
			{Name: "id", Type: compat.Type{Family: compat.IntegerType}},
			{Name: "amount", Type: compat.Type{Family: compat.DecimalType, Arguments: []int{38, 18}}},
		},
		Constraints: []compat.Constraint{{Kind: compat.PrimaryKey, Columns: []string{"id"}}},
	}}}
	want := "12345678901234567890.123456789012345678"

	source := openSQLite(t, filepath.Join(t.TempDir(), "decimal.db"))
	if err := source.ApplySchema(ctx, schema); err != nil {
		t.Fatal(err)
	}
	if _, err := source.DB.ExecContext(ctx, `INSERT INTO decimal_precision (id, amount) VALUES (?, ?)`, 1, want); err != nil {
		t.Fatal(err)
	}
	snapshot, err := source.ExportSnapshot(ctx, schema)
	if err != nil {
		t.Fatal(err)
	}
	got := snapshot.Rows["decimal_precision"][0]["amount"].Value
	if got != want {
		t.Fatalf("precision was not preserved: want %s, got %s", want, got)
	}

	postgres := openPostgres(t)
	if err := postgres.ImportSnapshot(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	postgresSnapshot, err := postgres.ExportSnapshot(ctx, schema)
	if err != nil {
		t.Fatal(err)
	}
	assertEquivalent(t, snapshot, postgresSnapshot)
}

func TestSystemPreservesJSONUUIDAndTimestampSemantics(t *testing.T) {
	ctx := context.Background()
	schema := compat.Schema{Tables: []compat.Table{{
		Name: "rich_values",
		Columns: []compat.Column{
			{Name: "id", Type: compat.Type{Family: compat.UUIDType}},
			{Name: "document", Type: compat.Type{Family: compat.JSONType}},
			{Name: "occurred_at", Type: compat.Type{Family: compat.TimestampType}},
		},
		Constraints: []compat.Constraint{{Kind: compat.PrimaryKey, Columns: []string{"id"}}},
	}}}

	source := openSQLite(t, filepath.Join(t.TempDir(), "rich.db"))
	if err := source.ApplySchema(ctx, schema); err != nil {
		t.Fatal(err)
	}
	if _, err := source.DB.ExecContext(ctx, `INSERT INTO rich_values (id, document, occurred_at) VALUES (?, ?, ?)`,
		"550e8400-e29b-41d4-a716-446655440000", `{"b":2,"a":1}`, "2026-07-22T12:34:56.123456789-06:00"); err != nil {
		t.Fatal(err)
	}
	sourceSnapshot, err := source.ExportSnapshot(ctx, schema)
	if err != nil {
		t.Fatal(err)
	}
	postgres := openPostgres(t)
	if err := postgres.ImportSnapshot(ctx, sourceSnapshot); err != nil {
		t.Fatal(err)
	}
	postgresSnapshot, err := postgres.ExportSnapshot(ctx, schema)
	if err != nil {
		t.Fatal(err)
	}
	assertEquivalent(t, sourceSnapshot, postgresSnapshot)
}

func TestSystemEnforcesForeignKeysEqually(t *testing.T) {
	ctx := context.Background()
	schema := compat.Schema{Tables: []compat.Table{
		{
			Name:        "parents",
			Columns:     []compat.Column{{Name: "id", Type: compat.Type{Family: compat.IntegerType}}},
			Constraints: []compat.Constraint{{Kind: compat.PrimaryKey, Columns: []string{"id"}}},
		},
		{
			Name: "children",
			Columns: []compat.Column{
				{Name: "id", Type: compat.Type{Family: compat.IntegerType}},
				{Name: "parent_id", Type: compat.Type{Family: compat.IntegerType}},
			},
			Constraints: []compat.Constraint{
				{Kind: compat.PrimaryKey, Columns: []string{"id"}},
				{Kind: compat.ForeignKey, Columns: []string{"parent_id"}, References: &compat.Reference{Table: "parents", Columns: []string{"id"}}},
			},
		},
	}}

	sqlite := openSQLite(t, filepath.Join(t.TempDir(), "fk.db"))
	if err := sqlite.ApplySchema(ctx, schema); err != nil {
		t.Fatal(err)
	}
	_, sqliteErr := sqlite.DB.ExecContext(ctx, `INSERT INTO children (id, parent_id) VALUES (1, 999)`)

	postgres := openPostgres(t)
	if err := postgres.ApplySchema(ctx, schema); err != nil {
		t.Fatal(err)
	}
	_, postgresErr := postgres.DB.ExecContext(ctx, `INSERT INTO children (id, parent_id) VALUES (1, 999)`)

	if (sqliteErr == nil) != (postgresErr == nil) {
		t.Fatalf("foreign-key behavior differs: sqlite error=%v, postgres error=%v", sqliteErr, postgresErr)
	}
	if sqliteErr == nil {
		t.Fatal("both engines accepted an invalid foreign key")
	}
}

func TestSystemClaimsExactCoverageForRequiredFeatureFamilies(t *testing.T) {
	contract := compat.Contract{
		Source:      compat.Target{Engine: compat.SQLite, Version: compat.Version{Major: 3}},
		Destination: compat.Target{Engine: compat.Postgres, Version: compat.Version{Major: 17}},
		RequiredFeatures: []compat.Feature{
			compat.Tables,
			compat.PrimaryKeys,
			compat.ForeignKeys,
			compat.CheckRules,
			compat.Transactions,
			compat.Indexes,
			compat.JSONValues,
			compat.UUIDValues,
			compat.Triggers,
			compat.Views,
			compat.StoredRoutines,
			compat.FullText,
		},
	}
	findings, err := compat.Audit(contract)
	if err != nil {
		t.Fatal(err)
	}
	if err := compat.RequireExact(findings); err != nil {
		t.Fatalf("system does not provide exact coverage: %v; findings=%+v", err, findings)
	}
}

func openSQLite(t *testing.T, path string) *compat.Store {
	t.Helper()
	store, err := compat.OpenSQLite(compat.Version{Major: 3}, "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func openPostgres(t *testing.T) *compat.Store {
	t.Helper()
	store, err := compat.OpenPostgres(compat.Version{Major: 17, Minor: 5}, postgresTestDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func assertEquivalent(t *testing.T, source, destination compat.Snapshot) {
	t.Helper()
	report, err := compat.VerifySnapshots(source, destination)
	if err != nil {
		t.Fatal(err)
	}
	if err := compat.RequireEquivalent(report); err != nil {
		t.Fatal(err)
	}
}

func TestDatabaseDSNPreservesConnectionParameters(t *testing.T) {
	dsn, err := databaseDSN("postgres://user@127.0.0.1:5432/postgres?sslmode=disable", "temporary")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(dsn, "/temporary?") || !strings.Contains(dsn, "sslmode=disable") {
		t.Fatalf("unexpected DSN %q", dsn)
	}
}
