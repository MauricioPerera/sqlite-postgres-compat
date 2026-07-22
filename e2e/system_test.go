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
	"reflect"
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

func TestSystemCanonicalViewProducesEquivalentResults(t *testing.T) {
	ctx := context.Background()
	joinCondition := compat.Expression{Kind: "eq", Args: []compat.Expression{
		{Kind: "column", Value: "s.product_id"},
		{Kind: "column", Value: "p.id"},
	}}
	activeCondition := compat.Expression{Kind: "eq", Args: []compat.Expression{
		{Kind: "column", Value: "p.active"},
		{Kind: "boolean", Value: "true"},
	}}
	schema := compat.Schema{
		Tables: []compat.Table{
			{
				Name: "view_products",
				Columns: []compat.Column{
					{Name: "id", Type: compat.Type{Family: compat.IntegerType}},
					{Name: "name", Type: compat.Type{Family: compat.TextType}},
					{Name: "active", Type: compat.Type{Family: compat.BooleanType}},
				},
				Constraints: []compat.Constraint{{Kind: compat.PrimaryKey, Columns: []string{"id"}}},
			},
			{
				Name: "view_sales",
				Columns: []compat.Column{
					{Name: "id", Type: compat.Type{Family: compat.IntegerType}},
					{Name: "product_id", Type: compat.Type{Family: compat.IntegerType}},
					{Name: "amount", Type: compat.Type{Family: compat.IntegerType}},
				},
				Constraints: []compat.Constraint{
					{Kind: compat.PrimaryKey, Columns: []string{"id"}},
					{Kind: compat.ForeignKey, Columns: []string{"product_id"}, References: &compat.Reference{Table: "view_products", Columns: []string{"id"}}},
				},
			},
		},
		Views: []compat.View{{
			Name: "active_sales",
			Query: compat.SelectQuery{
				Columns: []compat.Projection{
					{Expression: compat.Expression{Kind: "column", Value: "p.name"}, Alias: "name"},
					{Expression: compat.Expression{Kind: "sum", Args: []compat.Expression{{Kind: "column", Value: "s.amount"}}}, Alias: "total"},
				},
				From:  compat.TableSource{Table: "view_sales", Alias: "s"},
				Joins: []compat.Join{{Kind: "inner", Table: compat.TableSource{Table: "view_products", Alias: "p"}, On: joinCondition}},
				Where: &activeCondition,
				GroupBy: []compat.Expression{
					{Kind: "column", Value: "p.name"},
				},
			},
		}},
	}

	source := openSQLite(t, filepath.Join(t.TempDir(), "views.db"))
	if err := source.ApplySchema(ctx, schema); err != nil {
		t.Fatal(err)
	}
	if _, err := source.DB.ExecContext(ctx, `INSERT INTO view_products (id, name, active) VALUES (1, 'alpha', 1), (2, 'beta', 0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := source.DB.ExecContext(ctx, `INSERT INTO view_sales (id, product_id, amount) VALUES (1, 1, 10), (2, 1, 15), (3, 2, 100)`); err != nil {
		t.Fatal(err)
	}
	snapshot, err := source.ExportSnapshot(ctx, schema)
	if err != nil {
		t.Fatal(err)
	}
	postgres := openPostgres(t)
	if err := postgres.ImportSnapshot(ctx, snapshot); err != nil {
		t.Fatal(err)
	}

	sqliteRows := queryNameTotals(t, source.DB)
	postgresRows := queryNameTotals(t, postgres.DB)
	if fmt.Sprint(sqliteRows) != fmt.Sprint(postgresRows) {
		t.Fatalf("view results differ: sqlite=%v postgres=%v", sqliteRows, postgresRows)
	}
}

func TestSystemCanonicalTriggerProducesEquivalentEffects(t *testing.T) {
	ctx := context.Background()
	schema := compat.Schema{
		Tables: []compat.Table{
			{
				Name: "trigger_entries",
				Columns: []compat.Column{
					{Name: "id", Type: compat.Type{Family: compat.IntegerType}},
					{Name: "title", Type: compat.Type{Family: compat.TextType}},
				},
				Constraints: []compat.Constraint{{Kind: compat.PrimaryKey, Columns: []string{"id"}}},
			},
			{
				Name: "trigger_audit",
				Columns: []compat.Column{
					{Name: "entry_id", Type: compat.Type{Family: compat.IntegerType}},
					{Name: "copied_title", Type: compat.Type{Family: compat.TextType}},
				},
			},
		},
		Triggers: []compat.Trigger{{
			Name:   "capture_entry_insert",
			Table:  "trigger_entries",
			Timing: "after",
			Event:  "insert",
			Actions: []compat.TriggerAction{{
				Kind:  "insert",
				Table: "trigger_audit",
				Assignments: []compat.Assignment{
					{Column: "entry_id", Value: compat.Expression{Kind: "column", Value: "new.id"}},
					{Column: "copied_title", Value: compat.Expression{Kind: "column", Value: "new.title"}},
				},
			}},
		}},
	}

	sqlite := openSQLite(t, filepath.Join(t.TempDir(), "triggers.db"))
	if err := sqlite.ApplySchema(ctx, schema); err != nil {
		t.Fatal(err)
	}
	postgres := openPostgres(t)
	if err := postgres.ApplySchema(ctx, schema); err != nil {
		t.Fatal(err)
	}
	for _, store := range []*compat.Store{sqlite, postgres} {
		if _, err := store.DB.ExecContext(ctx, `INSERT INTO trigger_entries (id, title) VALUES (1, 'captured')`); err != nil {
			t.Fatal(err)
		}
	}

	query := func(db *sql.DB) string {
		var value string
		if err := db.QueryRow(`SELECT CAST(entry_id AS TEXT) || ':' || copied_title FROM trigger_audit`).Scan(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	sqliteValue := query(sqlite.DB)
	postgresValue := query(postgres.DB)
	if sqliteValue != "1:captured" || postgresValue != sqliteValue {
		t.Fatalf("trigger effects differ: sqlite=%q postgres=%q", sqliteValue, postgresValue)
	}
}

func TestSystemCanonicalRoutineExecutesEqually(t *testing.T) {
	ctx := context.Background()
	schema := compat.Schema{
		Tables: []compat.Table{{
			Name: "routine_entries",
			Columns: []compat.Column{
				{Name: "id", Type: compat.Type{Family: compat.IntegerType}},
				{Name: "title", Type: compat.Type{Family: compat.TextType}},
			},
			Constraints: []compat.Constraint{{Kind: compat.PrimaryKey, Columns: []string{"id"}}},
		}},
		Routines: []compat.Routine{{
			Name: "create_entry",
			Parameters: []compat.RoutineParameter{
				{Name: "id", Type: compat.Type{Family: compat.IntegerType}},
				{Name: "title", Type: compat.Type{Family: compat.TextType}},
			},
			Actions: []compat.RoutineAction{{
				Kind:  "insert",
				Table: "routine_entries",
				Assignments: []compat.Assignment{
					{Column: "id", Value: compat.Expression{Kind: "parameter", Value: "id"}},
					{Column: "title", Value: compat.Expression{Kind: "parameter", Value: "title"}},
				},
			}},
		}},
	}

	sqlite := openSQLite(t, filepath.Join(t.TempDir(), "routines.db"))
	postgres := openPostgres(t)
	for _, store := range []*compat.Store{sqlite, postgres} {
		if err := store.ApplySchema(ctx, schema); err != nil {
			t.Fatal(err)
		}
		if err := store.CallRoutine(ctx, schema, "create_entry", map[string]compat.Value{
			"id":    {Kind: compat.IntegerValue, Value: "1"},
			"title": {Kind: compat.TextValue, Value: "created by routine"},
		}); err != nil {
			t.Fatal(err)
		}
	}
	sqliteSnapshot, err := sqlite.ExportSnapshot(ctx, schema)
	if err != nil {
		t.Fatal(err)
	}
	postgresSnapshot, err := postgres.ExportSnapshot(ctx, schema)
	if err != nil {
		t.Fatal(err)
	}
	assertEquivalent(t, sqliteSnapshot, postgresSnapshot)
}

func TestSystemCanonicalFullTextReturnsEquivalentResults(t *testing.T) {
	ctx := context.Background()
	schema := compat.Schema{Tables: []compat.Table{{
		Name: "search_documents",
		Columns: []compat.Column{
			{Name: "id", Type: compat.Type{Family: compat.IntegerType}},
			{Name: "title", Type: compat.Type{Family: compat.TextType}},
			{Name: "body", Type: compat.Type{Family: compat.TextType}},
		},
		Constraints: []compat.Constraint{{Kind: compat.PrimaryKey, Columns: []string{"id"}}},
	}}}
	sqlite := openSQLite(t, filepath.Join(t.TempDir(), "search.db"))
	if err := sqlite.ApplySchema(ctx, schema); err != nil {
		t.Fatal(err)
	}
	if _, err := sqlite.DB.ExecContext(ctx, `INSERT INTO search_documents (id, title, body) VALUES
		(1, 'Árboles del mundo', 'Una guía sobre árboles y bosques'),
		(2, 'Bases de datos', 'SQLite y PostgreSQL'),
		(3, 'Bosques', 'Árboles antiguos')`); err != nil {
		t.Fatal(err)
	}
	snapshot, err := sqlite.ExportSnapshot(ctx, schema)
	if err != nil {
		t.Fatal(err)
	}
	postgres := openPostgres(t)
	if err := postgres.ImportSnapshot(ctx, snapshot); err != nil {
		t.Fatal(err)
	}

	sqliteResults, err := sqlite.SearchText(ctx, "search_documents", "id", []string{"title", "body"}, "árboles bosques")
	if err != nil {
		t.Fatal(err)
	}
	postgresResults, err := postgres.SearchText(ctx, "search_documents", "id", []string{"title", "body"}, "árboles bosques")
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(sqliteResults) != fmt.Sprint(postgresResults) || len(sqliteResults) != 2 {
		t.Fatalf("search results differ: sqlite=%v postgres=%v", sqliteResults, postgresResults)
	}
}

func TestSystemReconstructsExactCanonicalSchemaFromBothEngines(t *testing.T) {
	ctx := context.Background()
	schema := compat.Schema{
		Tables: []compat.Table{{
			Name: "inspection_entries",
			Columns: []compat.Column{
				{Name: "id", Type: compat.Type{Family: compat.UUIDType}},
				{Name: "payload", Type: compat.Type{Family: compat.JSONType}},
			},
			Constraints: []compat.Constraint{{Kind: compat.PrimaryKey, Columns: []string{"id"}}},
		}},
		Routines: []compat.Routine{{
			Name: "inspection_insert",
			Parameters: []compat.RoutineParameter{
				{Name: "id", Type: compat.Type{Family: compat.UUIDType}},
				{Name: "payload", Type: compat.Type{Family: compat.JSONType}},
			},
			Actions: []compat.RoutineAction{{
				Kind:  "insert",
				Table: "inspection_entries",
				Assignments: []compat.Assignment{
					{Column: "id", Value: compat.Expression{Kind: "parameter", Value: "id"}},
					{Column: "payload", Value: compat.Expression{Kind: "parameter", Value: "payload"}},
				},
			}},
		}},
	}
	sqlite := openSQLite(t, filepath.Join(t.TempDir(), "inspection.db"))
	postgres := openPostgres(t)
	for _, store := range []*compat.Store{sqlite, postgres} {
		if err := store.ApplySchema(ctx, schema); err != nil {
			t.Fatal(err)
		}
		inspection, err := store.InspectSchema(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !inspection.Exact || inspection.Source != "canonical_metadata" || !reflect.DeepEqual(schema, inspection.Schema) {
			t.Fatalf("schema was not reconstructed exactly: %+v", inspection)
		}
	}
}

func TestSystemReplicatesIncrementalChangesBothDirections(t *testing.T) {
	ctx := context.Background()
	schema := compat.Schema{Tables: []compat.Table{{
		Name: "bidirectional_items",
		Columns: []compat.Column{
			{Name: "id", Type: compat.Type{Family: compat.IntegerType}},
			{Name: "title", Type: compat.Type{Family: compat.TextType}},
		},
		Constraints: []compat.Constraint{{Kind: compat.PrimaryKey, Columns: []string{"id"}}},
	}}}
	sqlite := openSQLite(t, filepath.Join(t.TempDir(), "replication.db"))
	postgres := openPostgres(t)
	for _, store := range []*compat.Store{sqlite, postgres} {
		if err := store.ApplySchema(ctx, schema); err != nil {
			t.Fatal(err)
		}
	}

	id := compat.Value{Kind: compat.IntegerValue, Value: "1"}
	first := compat.Row{"id": id, "title": {Kind: compat.TextValue, Value: "from sqlite"}}
	updated := compat.Row{"id": id, "title": {Kind: compat.TextValue, Value: "updated by sqlite"}}
	sqliteStream := []compat.Change{
		{Source: compat.Target{Engine: compat.SQLite, Version: compat.Version{Major: 3}}, Sequence: 1, Kind: compat.Insert, Table: "bidirectional_items", PrimaryKey: compat.Row{"id": id}, After: first},
		{Source: compat.Target{Engine: compat.SQLite, Version: compat.Version{Major: 3}}, Sequence: 2, Kind: compat.Update, Table: "bidirectional_items", PrimaryKey: compat.Row{"id": id}, Before: first, After: updated},
	}
	for _, store := range []*compat.Store{sqlite, postgres} {
		if err := store.ApplyChanges(ctx, schema, sqliteStream); err != nil {
			t.Fatal(err)
		}
		if err := store.ApplyChanges(ctx, schema, sqliteStream); err != nil {
			t.Fatal("stream reapplication must be idempotent:", err)
		}
	}
	assertStoreSnapshotsEquivalent(t, ctx, schema, sqlite, postgres)

	reversed := compat.Row{"id": id, "title": {Kind: compat.TextValue, Value: "returned from postgres"}}
	postgresStream := []compat.Change{{
		Source: compat.Target{Engine: compat.Postgres, Version: compat.Version{Major: 17, Minor: 5}}, Sequence: 1,
		Kind: compat.Update, Table: "bidirectional_items", PrimaryKey: compat.Row{"id": id}, Before: updated, After: reversed,
	}}
	for _, store := range []*compat.Store{postgres, sqlite} {
		if err := store.ApplyChanges(ctx, schema, postgresStream); err != nil {
			t.Fatal(err)
		}
	}
	assertStoreSnapshotsEquivalent(t, ctx, schema, sqlite, postgres)
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
		"550E8400-E29B-41D4-A716-446655440000", `{ "b": 123456789012345678901234567890, "a": 1 }`, "2026-07-22T12:34:56.123456789-06:00"); err != nil {
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
				{Kind: compat.ForeignKey, Columns: []string{"parent_id"}, References: &compat.Reference{Table: "parents", Columns: []string{"id"}, OnUpdate: compat.Cascade, OnDelete: compat.Cascade}},
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
	for _, store := range []*compat.Store{sqlite, postgres} {
		if _, err := store.DB.ExecContext(ctx, `INSERT INTO parents (id) VALUES (1)`); err != nil {
			t.Fatal(err)
		}
		if _, err := store.DB.ExecContext(ctx, `INSERT INTO children (id, parent_id) VALUES (2, 1)`); err != nil {
			t.Fatal(err)
		}
		if _, err := store.DB.ExecContext(ctx, `UPDATE parents SET id = 3 WHERE id = 1`); err != nil {
			t.Fatalf("%s update cascade: %v", store.Target.Engine, err)
		}
		var parentID int
		if err := store.DB.QueryRowContext(ctx, `SELECT parent_id FROM children WHERE id = 2`).Scan(&parentID); err != nil || parentID != 3 {
			t.Fatalf("%s update cascade produced parent_id=%d error=%v", store.Target.Engine, parentID, err)
		}
		if _, err := store.DB.ExecContext(ctx, `DELETE FROM parents WHERE id = 3`); err != nil {
			t.Fatalf("%s delete cascade: %v", store.Target.Engine, err)
		}
		var children int
		if err := store.DB.QueryRowContext(ctx, `SELECT count(*) FROM children`).Scan(&children); err != nil || children != 0 {
			t.Fatalf("%s delete cascade left %d rows, error=%v", store.Target.Engine, children, err)
		}
	}
}

func TestSystemEnforcesCanonicalChecksAndIndexesEqually(t *testing.T) {
	ctx := context.Background()
	nonNegative := compat.Expression{Kind: "gte", Args: []compat.Expression{
		{Kind: "column", Value: "price"},
		{Kind: "integer", Value: "0"},
	}}
	active := compat.Expression{Kind: "eq", Args: []compat.Expression{
		{Kind: "column", Value: "active"},
		{Kind: "boolean", Value: "true"},
	}}
	schema := compat.Schema{
		Tables: []compat.Table{{
			Name: "indexed_products",
			Columns: []compat.Column{
				{Name: "id", Type: compat.Type{Family: compat.IntegerType}},
				{Name: "code", Type: compat.Type{Family: compat.TextType}},
				{Name: "price", Type: compat.Type{Family: compat.IntegerType}},
				{Name: "active", Type: compat.Type{Family: compat.BooleanType}},
			},
			Constraints: []compat.Constraint{
				{Kind: compat.PrimaryKey, Columns: []string{"id"}},
				{Kind: compat.Check, Expression: &nonNegative},
			},
		}},
		Indexes: []compat.Index{
			{Name: "indexed_products_code_unique", Table: "indexed_products", Unique: true, Columns: []compat.IndexColumn{{Column: "code"}}},
			{Name: "indexed_products_active_price", Table: "indexed_products", Columns: []compat.IndexColumn{{Column: "price", Descending: true}}, Where: &active},
		},
	}

	stores := []*compat.Store{
		openSQLite(t, filepath.Join(t.TempDir(), "checks-indexes.db")),
		openPostgres(t),
	}
	for _, store := range stores {
		if err := store.ApplySchema(ctx, schema); err != nil {
			t.Fatalf("%s apply schema: %v", store.Target.Engine, err)
		}
		if _, err := store.DB.ExecContext(ctx, `INSERT INTO indexed_products (id, code, price, active) VALUES (1, 'A', 25, TRUE)`); err != nil {
			t.Fatalf("%s rejected valid row: %v", store.Target.Engine, err)
		}
		if _, err := store.DB.ExecContext(ctx, `INSERT INTO indexed_products (id, code, price, active) VALUES (2, 'B', -1, TRUE)`); err == nil {
			t.Fatalf("%s accepted row violating CHECK", store.Target.Engine)
		}
		if _, err := store.DB.ExecContext(ctx, `INSERT INTO indexed_products (id, code, price, active) VALUES (3, 'A', 30, FALSE)`); err == nil {
			t.Fatalf("%s accepted row violating unique index", store.Target.Engine)
		}
		inspection, err := store.InspectSchema(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !inspection.Exact || !reflect.DeepEqual(schema, inspection.Schema) {
			t.Fatalf("%s did not reconstruct checks and indexes exactly: %+v", store.Target.Engine, inspection)
		}
		var count int
		if store.Target.Engine == compat.SQLite {
			err = store.DB.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type = 'index' AND name IN ('indexed_products_code_unique', 'indexed_products_active_price')`).Scan(&count)
		} else {
			err = store.DB.QueryRowContext(ctx, `SELECT count(*) FROM pg_indexes WHERE schemaname = current_schema() AND indexname IN ('indexed_products_code_unique', 'indexed_products_active_price')`).Scan(&count)
		}
		if err != nil || count != 2 {
			t.Fatalf("%s physical indexes: count=%d error=%v", store.Target.Engine, count, err)
		}
	}
	assertStoreSnapshotsEquivalent(t, ctx, schema, stores[0], stores[1])
}

func TestSystemInspectsNativeConstraintsAndIndexesWithoutMetadata(t *testing.T) {
	ctx := context.Background()
	sqlite := openSQLite(t, filepath.Join(t.TempDir(), "native-catalog.db"))
	postgres := openPostgres(t)
	// This case must inspect an external catalog without canonical metadata or
	// objects left by earlier tests in the suite's disposable database.
	if _, err := postgres.DB.ExecContext(ctx, `DROP SCHEMA public CASCADE`); err != nil {
		t.Fatal(err)
	}
	if _, err := postgres.DB.ExecContext(ctx, `CREATE SCHEMA public`); err != nil {
		t.Fatal(err)
	}
	statements := map[compat.Engine][]string{
		compat.SQLite: {
			`CREATE TABLE native_products (code TEXT NOT NULL, price INTEGER NOT NULL DEFAULT 3, active BOOLEAN NOT NULL DEFAULT TRUE, status TEXT NOT NULL DEFAULT 'new', CHECK (price >= 0))`,
			`CREATE UNIQUE INDEX native_products_code ON native_products (code ASC)`,
			`CREATE INDEX native_products_active_price ON native_products (price DESC) WHERE active = TRUE`,
			`CREATE TABLE native_parents (id INTEGER PRIMARY KEY, tenant INTEGER NOT NULL, code TEXT NOT NULL, UNIQUE (tenant, code))`,
			`CREATE TABLE native_children (id INTEGER PRIMARY KEY, parent_tenant INTEGER NOT NULL, parent_code TEXT NOT NULL, FOREIGN KEY (parent_tenant, parent_code) REFERENCES native_parents (tenant, code) ON UPDATE CASCADE ON DELETE CASCADE)`,
		},
		compat.Postgres: {
			`CREATE TABLE native_products (code TEXT NOT NULL, price BIGINT NOT NULL DEFAULT 3, active BOOLEAN NOT NULL DEFAULT TRUE, status TEXT NOT NULL DEFAULT 'new', CHECK (price >= 0))`,
			`CREATE UNIQUE INDEX native_products_code ON native_products (code ASC)`,
			`CREATE INDEX native_products_active_price ON native_products (price DESC) WHERE active = TRUE`,
			`CREATE TABLE native_parents (id BIGINT PRIMARY KEY, tenant BIGINT NOT NULL, code TEXT NOT NULL, UNIQUE (tenant, code))`,
			`CREATE TABLE native_children (id BIGINT PRIMARY KEY, parent_tenant BIGINT NOT NULL, parent_code TEXT NOT NULL, FOREIGN KEY (parent_tenant, parent_code) REFERENCES native_parents (tenant, code) ON UPDATE CASCADE ON DELETE CASCADE)`,
		},
	}
	for _, store := range []*compat.Store{sqlite, postgres} {
		for _, statement := range statements[store.Target.Engine] {
			if _, err := store.DB.ExecContext(ctx, statement); err != nil {
				t.Fatalf("%s native DDL: %v", store.Target.Engine, err)
			}
		}
		inspection, err := store.InspectSchema(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !inspection.Exact || len(inspection.Unresolved) != 0 {
			t.Fatalf("%s native catalog was not translated exactly: %+v", store.Target.Engine, inspection)
		}
		var products, parents, children *compat.Table
		for i := range inspection.Schema.Tables {
			switch inspection.Schema.Tables[i].Name {
			case "native_products":
				products = &inspection.Schema.Tables[i]
			case "native_parents":
				parents = &inspection.Schema.Tables[i]
			case "native_children":
				children = &inspection.Schema.Tables[i]
			}
		}
		if products == nil || len(products.Constraints) != 1 || products.Constraints[0].Kind != compat.Check {
			t.Fatalf("%s missing native CHECK: %+v", store.Target.Engine, inspection.Schema)
		}
		if !hasColumnDefault(*products, "price", "integer", "3") || !hasColumnDefault(*products, "active", "boolean", "true") || !hasColumnDefault(*products, "status", "string", "new") {
			t.Fatalf("%s native defaults were not reconstructed: %+v", store.Target.Engine, products.Columns)
		}
		if parents == nil || children == nil || !hasConstraint(*parents, compat.PrimaryKey, 1) || !hasConstraint(*parents, compat.UniqueKey, 2) || !hasConstraint(*children, compat.PrimaryKey, 1) || !hasConstraint(*children, compat.ForeignKey, 2) {
			t.Fatalf("%s native key constraints were not reconstructed: %+v", store.Target.Engine, inspection.Schema.Tables)
		}
		if !hasForeignActions(*children, compat.Cascade, compat.Cascade) {
			t.Fatalf("%s native referential actions were not reconstructed: %+v", store.Target.Engine, children.Constraints)
		}
		if len(inspection.Schema.Indexes) != 2 {
			t.Fatalf("%s missing native indexes: %+v", store.Target.Engine, inspection.Schema.Indexes)
		}
		var foundUnique, foundPartial bool
		for _, index := range inspection.Schema.Indexes {
			switch index.Name {
			case "native_products_code":
				foundUnique = index.Unique && len(index.Columns) == 1 && index.Columns[0].Column == "code"
			case "native_products_active_price":
				foundPartial = index.Where != nil && len(index.Columns) == 1 && index.Columns[0].Column == "price" && index.Columns[0].Descending
			}
		}
		if !foundUnique || !foundPartial {
			t.Fatalf("%s native index semantics lost: %+v", store.Target.Engine, inspection.Schema.Indexes)
		}
	}
}

func TestSystemAutomaticallyCapturesAndReplicatesBothDirections(t *testing.T) {
	ctx := context.Background()
	schema := compat.Schema{Tables: []compat.Table{{
		Name: "automatic_changes",
		Columns: []compat.Column{
			{Name: "id", Type: compat.Type{Family: compat.IntegerType}},
			{Name: "title", Type: compat.Type{Family: compat.TextType}},
			{Name: "payload", Type: compat.Type{Family: compat.BinaryType}, Nullable: true},
		},
		Constraints: []compat.Constraint{{Kind: compat.PrimaryKey, Columns: []string{"id"}}},
	}}}
	sqlite := openSQLite(t, filepath.Join(t.TempDir(), "automatic-capture.db"))
	postgres := openPostgres(t)
	for _, store := range []*compat.Store{sqlite, postgres} {
		if err := store.ApplySchema(ctx, schema); err != nil {
			t.Fatal(err)
		}
		if err := store.InstallChangeCapture(ctx, schema); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := sqlite.DB.ExecContext(ctx, `INSERT INTO automatic_changes (id, title, payload) VALUES (1, 'sqlite', x'00FF')`); err != nil {
		t.Fatal(err)
	}
	if _, err := sqlite.DB.ExecContext(ctx, `UPDATE automatic_changes SET title = 'sqlite updated' WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	sqliteChanges, err := sqlite.ReadCapturedChanges(ctx, schema, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(sqliteChanges) != 2 {
		t.Fatalf("expected two automatic SQLite changes, got %+v", sqliteChanges)
	}
	if err := postgres.ApplyChanges(ctx, schema, sqliteChanges); err != nil {
		t.Fatal(err)
	}
	postgresEchoes, err := postgres.ReadCapturedChanges(ctx, schema, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(postgresEchoes) != 0 {
		t.Fatalf("replicated changes were captured again by PostgreSQL: %+v", postgresEchoes)
	}
	assertStoreSnapshotsEquivalent(t, ctx, schema, sqlite, postgres)

	if _, err := postgres.DB.ExecContext(ctx, `UPDATE automatic_changes SET title = 'postgres' WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	if _, err := postgres.DB.ExecContext(ctx, `DELETE FROM automatic_changes WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	postgresChanges, err := postgres.ReadCapturedChanges(ctx, schema, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(postgresChanges) != 2 || postgresChanges[0].Kind != compat.Update || postgresChanges[1].Kind != compat.Delete {
		t.Fatalf("unexpected automatic PostgreSQL stream: %+v", postgresChanges)
	}
	if err := sqlite.ApplyChanges(ctx, schema, postgresChanges); err != nil {
		t.Fatal(err)
	}
	sqliteEchoes, err := sqlite.ReadCapturedChanges(ctx, schema, sqliteChanges[len(sqliteChanges)-1].Sequence, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(sqliteEchoes) != 0 {
		t.Fatalf("replicated changes were captured again by SQLite: %+v", sqliteEchoes)
	}
	assertStoreSnapshotsEquivalent(t, ctx, schema, sqlite, postgres)
}

func hasConstraint(table compat.Table, kind compat.ConstraintKind, columnCount int) bool {
	for _, constraint := range table.Constraints {
		if constraint.Kind == kind && len(constraint.Columns) == columnCount {
			return true
		}
	}
	return false
}

func hasColumnDefault(table compat.Table, columnName, kind, value string) bool {
	for _, column := range table.Columns {
		if column.Name == columnName {
			return column.Default != nil && column.Default.Kind == kind && column.Default.Value == value
		}
	}
	return false
}

func hasForeignActions(table compat.Table, onUpdate, onDelete compat.ReferentialAction) bool {
	for _, constraint := range table.Constraints {
		if constraint.Kind == compat.ForeignKey && constraint.References != nil && constraint.References.OnUpdate == onUpdate && constraint.References.OnDelete == onDelete {
			return true
		}
	}
	return false
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
	for _, finding := range findings {
		finding := finding
		t.Run(string(finding.Feature), func(t *testing.T) {
			if finding.Status != compat.Exact {
				t.Fatalf("system does not provide exact coverage: status=%s reason=%s", finding.Status, finding.Reason)
			}
		})
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

func assertStoreSnapshotsEquivalent(t *testing.T, ctx context.Context, schema compat.Schema, left, right *compat.Store) {
	t.Helper()
	leftSnapshot, err := left.ExportSnapshot(ctx, schema)
	if err != nil {
		t.Fatal(err)
	}
	rightSnapshot, err := right.ExportSnapshot(ctx, schema)
	if err != nil {
		t.Fatal(err)
	}
	assertEquivalent(t, leftSnapshot, rightSnapshot)
}

func queryNameTotals(t *testing.T, db *sql.DB) []string {
	t.Helper()
	rows, err := db.Query(`SELECT name, total FROM active_sales ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var result []string
	for rows.Next() {
		var name string
		var total int64
		if err := rows.Scan(&name, &total); err != nil {
			t.Fatal(err)
		}
		result = append(result, fmt.Sprintf("%s:%d", name, total))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return result
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
