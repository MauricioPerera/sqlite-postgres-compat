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

	command := exec.Command("go", "run", "./cmd/compat", "copy", configPath)
	command.Dir = ".."
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("compat copy failed: %v\n%s", err, output)
	}
	var report compat.VerificationReport
	if err := json.Unmarshal(output, &report); err != nil {
		t.Fatalf("invalid compat copy output %q: %v", output, err)
	}
	if !report.Equivalent {
		t.Fatalf("compat copy reported non-equivalent snapshots: %+v", report)
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

func TestSystemInspectsNativeSchemaObjectsWithoutMetadata(t *testing.T) {
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
			`CREATE TABLE native_audit (product_code TEXT NOT NULL)`,
			`CREATE TRIGGER native_product_audit AFTER INSERT ON native_products FOR EACH ROW BEGIN INSERT INTO native_audit (product_code) VALUES (NEW.code); END`,
			`CREATE TRIGGER native_product_audit_update AFTER UPDATE ON native_products FOR EACH ROW BEGIN UPDATE native_audit SET product_code = NEW.code WHERE product_code = OLD.code; END`,
			`CREATE TRIGGER native_product_audit_delete AFTER DELETE ON native_products FOR EACH ROW BEGIN DELETE FROM native_audit WHERE product_code = OLD.code; END`,
			`CREATE UNIQUE INDEX native_products_code ON native_products (code ASC)`,
			`CREATE INDEX native_products_active_price ON native_products (price DESC) WHERE active = TRUE`,
			`CREATE VIEW native_active_products AS SELECT code AS product_code, price FROM native_products WHERE active = TRUE`,
			`CREATE TABLE native_parents (id INTEGER PRIMARY KEY, tenant INTEGER NOT NULL, code TEXT NOT NULL, UNIQUE (tenant, code))`,
			`CREATE TABLE native_children (id INTEGER PRIMARY KEY, parent_tenant INTEGER NOT NULL, parent_code TEXT NOT NULL, FOREIGN KEY (parent_tenant, parent_code) REFERENCES native_parents (tenant, code) ON UPDATE CASCADE ON DELETE CASCADE)`,
			`CREATE VIEW native_parent_counts AS SELECT p.tenant AS tenant, count(c.id) AS child_count FROM native_parents AS p LEFT JOIN native_children AS c ON ((c.parent_tenant = p.tenant) AND (c.parent_code = p.code)) GROUP BY p.tenant`,
		},
		compat.Postgres: {
			`CREATE TABLE native_products (code TEXT NOT NULL, price BIGINT NOT NULL DEFAULT 3, active BOOLEAN NOT NULL DEFAULT TRUE, status TEXT NOT NULL DEFAULT 'new', CHECK (price >= 0))`,
			`CREATE TABLE native_audit (product_code TEXT NOT NULL)`,
			`CREATE FUNCTION native_product_audit_fn() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN INSERT INTO native_audit (product_code) VALUES (NEW.code); RETURN NEW; END $$`,
			`CREATE TRIGGER native_product_audit AFTER INSERT ON native_products FOR EACH ROW EXECUTE FUNCTION native_product_audit_fn()`,
			`CREATE FUNCTION native_product_audit_update_fn() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN UPDATE native_audit SET product_code = NEW.code WHERE product_code = OLD.code; RETURN NEW; END $$`,
			`CREATE TRIGGER native_product_audit_update AFTER UPDATE ON native_products FOR EACH ROW EXECUTE FUNCTION native_product_audit_update_fn()`,
			`CREATE FUNCTION native_product_audit_delete_fn() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN DELETE FROM native_audit WHERE product_code = OLD.code; RETURN OLD; END $$`,
			`CREATE TRIGGER native_product_audit_delete AFTER DELETE ON native_products FOR EACH ROW EXECUTE FUNCTION native_product_audit_delete_fn()`,
			`CREATE UNIQUE INDEX native_products_code ON native_products (code ASC)`,
			`CREATE INDEX native_products_active_price ON native_products (price DESC) WHERE active = TRUE`,
			`CREATE VIEW native_active_products AS SELECT code AS product_code, price FROM native_products WHERE active = TRUE`,
			`CREATE TABLE native_parents (id BIGINT PRIMARY KEY, tenant BIGINT NOT NULL, code TEXT NOT NULL, UNIQUE (tenant, code))`,
			`CREATE TABLE native_children (id BIGINT PRIMARY KEY, parent_tenant BIGINT NOT NULL, parent_code TEXT NOT NULL, FOREIGN KEY (parent_tenant, parent_code) REFERENCES native_parents (tenant, code) ON UPDATE CASCADE ON DELETE CASCADE)`,
			`CREATE VIEW native_parent_counts AS SELECT p.tenant AS tenant, count(c.id) AS child_count FROM native_parents AS p LEFT JOIN native_children AS c ON ((c.parent_tenant = p.tenant) AND (c.parent_code = p.code)) GROUP BY p.tenant`,
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
		activeView, aggregateView := false, false
		for _, view := range inspection.Schema.Views {
			if view.Name == "native_active_products" && len(view.Query.Columns) == 2 && view.Query.Where != nil {
				activeView = true
			}
			if view.Name == "native_parent_counts" && len(view.Query.Columns) == 2 && len(view.Query.Joins) == 1 && len(view.Query.GroupBy) == 1 && view.Query.Columns[1].Expression.Kind == "count" {
				aggregateView = true
			}
		}
		if !activeView || !aggregateView {
			t.Fatalf("%s native view was not reconstructed: %+v", store.Target.Engine, inspection.Schema.Views)
		}
		triggerKinds := map[string]bool{}
		for _, trigger := range inspection.Schema.Triggers {
			if len(trigger.Actions) == 1 {
				triggerKinds[trigger.Actions[0].Kind] = true
			}
		}
		if len(inspection.Schema.Triggers) != 3 || !triggerKinds["insert"] || !triggerKinds["update"] || !triggerKinds["delete"] {
			t.Fatalf("%s native trigger was not reconstructed: %+v", store.Target.Engine, inspection.Schema.Triggers)
		}
		if _, err := store.DB.ExecContext(ctx, `INSERT INTO native_products (code) VALUES ('A')`); err != nil {
			t.Fatal(err)
		}
		var code string
		var price int
		if err := store.DB.QueryRowContext(ctx, `SELECT product_code, price FROM native_active_products`).Scan(&code, &price); err != nil || code != "A" || price != 3 {
			t.Fatalf("%s native view behavior differs: code=%q price=%d error=%v", store.Target.Engine, code, price, err)
		}
		var auditedCode string
		if err := store.DB.QueryRowContext(ctx, `SELECT product_code FROM native_audit`).Scan(&auditedCode); err != nil || auditedCode != "A" {
			t.Fatalf("%s native trigger behavior differs: code=%q error=%v", store.Target.Engine, auditedCode, err)
		}
		if _, err := store.DB.ExecContext(ctx, `UPDATE native_products SET code = 'B' WHERE code = 'A'`); err != nil {
			t.Fatal(err)
		}
		if err := store.DB.QueryRowContext(ctx, `SELECT product_code FROM native_audit`).Scan(&auditedCode); err != nil || auditedCode != "B" {
			t.Fatalf("%s native update trigger differs: code=%q error=%v", store.Target.Engine, auditedCode, err)
		}
		if _, err := store.DB.ExecContext(ctx, `INSERT INTO native_parents (id, tenant, code) VALUES (1, 7, 'P')`); err != nil {
			t.Fatal(err)
		}
		if _, err := store.DB.ExecContext(ctx, `INSERT INTO native_children (id, parent_tenant, parent_code) VALUES (1, 7, 'P')`); err != nil {
			t.Fatal(err)
		}
		var tenant, childCount int64
		if err := store.DB.QueryRowContext(ctx, `SELECT tenant, child_count FROM native_parent_counts`).Scan(&tenant, &childCount); err != nil || tenant != 7 || childCount != 1 {
			t.Fatalf("%s joined aggregate view differs: tenant=%d count=%d error=%v", store.Target.Engine, tenant, childCount, err)
		}
		if _, err := store.DB.ExecContext(ctx, `DELETE FROM native_products WHERE code = 'B'`); err != nil {
			t.Fatal(err)
		}
		var deletedAuditRows int
		if err := store.DB.QueryRowContext(ctx, `SELECT count(*) FROM native_audit WHERE product_code = 'B'`).Scan(&deletedAuditRows); err != nil || deletedAuditRows != 0 {
			t.Fatalf("%s native delete trigger left %d rows, error=%v", store.Target.Engine, deletedAuditRows, err)
		}
		if store.Target.Engine == compat.Postgres {
			if _, err := store.DB.ExecContext(ctx, `CREATE PROCEDURE native_write_audit(p_code TEXT) LANGUAGE plpgsql AS $$ BEGIN INSERT INTO native_audit (product_code) VALUES (p_code); END $$`); err != nil {
				t.Fatal(err)
			}
			withProcedure, err := store.InspectSchema(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if !withProcedure.Exact || len(withProcedure.Schema.Routines) != 1 || withProcedure.Schema.Routines[0].Name != "native_write_audit" {
				t.Fatalf("canonical PostgreSQL procedure was not translated: %+v", withProcedure)
			}
			arguments := map[string]compat.Value{"p_code": {Kind: compat.TextValue, Value: "R"}}
			for _, runtimeStore := range []*compat.Store{sqlite, postgres} {
				if err := runtimeStore.CallRoutine(ctx, withProcedure.Schema, "native_write_audit", arguments); err != nil {
					t.Fatalf("%s translated routine: %v", runtimeStore.Target.Engine, err)
				}
				var routineRows int
				if err := runtimeStore.DB.QueryRowContext(ctx, `SELECT count(*) FROM native_audit WHERE product_code = 'R'`).Scan(&routineRows); err != nil || routineRows != 1 {
					t.Fatalf("%s translated routine behavior differs: rows=%d error=%v", runtimeStore.Target.Engine, routineRows, err)
				}
			}
			if _, err := store.DB.ExecContext(ctx, `CREATE FUNCTION native_standalone() RETURNS BIGINT LANGUAGE SQL AS 'SELECT 1'`); err != nil {
				t.Fatal(err)
			}
			withRoutine, err := store.InspectSchema(ctx)
			if err != nil {
				t.Fatal(err)
			}
			foundRoutine := false
			for _, unresolved := range withRoutine.Unresolved {
				if unresolved.Kind == "routine" && unresolved.Name == "native_standalone" {
					foundRoutine = true
				}
			}
			if withRoutine.Exact || !foundRoutine {
				t.Fatalf("standalone PostgreSQL routine was silently ignored: %+v", withRoutine)
			}
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

// TestSystemDateFamilyRoundTripsEquivalent guards the ALTA fix from AUDIT5 §4.1.
// Before the fix, a schema with a `date` column made `compat copy` ALWAYS fail
// verification with ERR_VERIFY_DIVERGED: Postgres mapped DateType to native DATE,
// pgx returned it as a time.Time, and canonicalValue folded that to a
// TimestampValue ("2020-01-01T00:00:00Z") that diverged from the SQLite TEXT
// source ("2020-01-01"). DateType now maps to TEXT on Postgres (like timestamp/
// json/uuid), so the date round-trips byte-for-byte. This drives the BUILT
// binary (go run collapses exit codes on Windows) against real PostgreSQL and
// asserts exit 0 plus equivalent=true — the exact path that used to diverge.
func TestSystemDateFamilyRoundTripsEquivalent(t *testing.T) {
	ctx := context.Background()
	schema := compat.Schema{Tables: []compat.Table{{
		Name: "fixa5_dates",
		Columns: []compat.Column{
			{Name: "id", Type: compat.Type{Family: compat.IntegerType}},
			{Name: "d", Type: compat.Type{Family: compat.DateType}, Nullable: true},
		},
		Constraints: []compat.Constraint{{Kind: compat.PrimaryKey, Columns: []string{"id"}}},
	}}}

	sourcePath := filepath.Join(t.TempDir(), "date-source.db")
	source, err := compat.OpenSQLite(compat.Version{Major: 3}, "file:"+filepath.ToSlash(sourcePath))
	if err != nil {
		t.Fatal(err)
	}
	if err := source.ApplySchema(ctx, schema); err != nil {
		t.Fatal(err)
	}
	if _, err := source.DB.ExecContext(ctx, `INSERT INTO fixa5_dates (id, d) VALUES (?, ?), (?, ?)`, 1, "2020-01-01", 2, nil); err != nil {
		t.Fatal(err)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}

	configuration := map[string]any{
		"source_dsn": "file:" + filepath.ToSlash(sourcePath),
		// Drop and recreate any leftover fixa5_dates table on the shared e2e PG
		// database so the import is additive into an empty destination.
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
	configPath := filepath.Join(t.TempDir(), "date-migration.json")
	if err := os.WriteFile(configPath, data, 0o600); err != nil {
		t.Fatal(err)
	}

	// Ensure the destination is empty for the described objects (import is
	// additive); other tests share the e2e PG database, so drop the table first.
	postgres := openPostgres(t)
	_, _ = postgres.DB.ExecContext(ctx, `DROP TABLE IF EXISTS fixa5_dates`)

	stdout, stderr, exitCode := runBuiltCLI(t, "cmd/compat", "copy", configPath)
	if exitCode != 0 {
		t.Fatalf("expected exit 0 for date copy, got %d\nstdout:\n%s\nstderr:\n%s", exitCode, stdout, stderr)
	}
	var report compat.VerificationReport
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &report); err != nil {
		t.Fatalf("invalid compat copy output %q: %v", stdout, err)
	}
	if !report.Equivalent {
		t.Fatalf("expected equivalent=true for date copy, got %+v\nstderr:\n%s", report, stderr)
	}

	// Confirm the date value landed as the exact canonical text on Postgres.
	var d, dn sql.NullString
	if err := postgres.DB.QueryRowContext(ctx, `SELECT d FROM fixa5_dates WHERE id = 1`).Scan(&d); err != nil {
		t.Fatal(err)
	}
	if err := postgres.DB.QueryRowContext(ctx, `SELECT d FROM fixa5_dates WHERE id = 2`).Scan(&dn); err != nil {
		t.Fatal(err)
	}
	if !d.Valid || d.String != "2020-01-01" {
		t.Fatalf("expected date '2020-01-01' on Postgres, got %v", d)
	}
	if dn.Valid {
		t.Fatalf("expected NULL date for id=2, got %q", dn.String)
	}
}

// TestCLIUsageCountMessageConsistent guards the MEDIA fix from AUDIT5 §4.2. All
// three subcommands must emit the same "requires exactly one ... JSON argument"
// form for a wrong positional count, instead of cutover's former divergent
// "usage: compat cutover [--dry-run] <cutover.json>" envelope message. The hint
// on stderr is subcommand-specific by design; this asserts only the machine-
// facing ERR_USAGE envelope message converges to the documented majority form.
func TestCLIUsageCountMessageConsistent(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"audit", []string{"audit"}, "compat audit requires exactly one contract JSON argument"},
		{"copy", []string{"copy"}, "compat copy requires exactly one migration JSON argument"},
		{"cutover", []string{"cutover"}, "compat cutover requires exactly one cutover JSON argument"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stdout, _, exitCode := runBuiltCLI(t, "cmd/compat", tc.args...)
			if exitCode != 2 {
				t.Fatalf("expected exit 2 for %s wrong count, got %d\nstdout:\n%s", tc.name, exitCode, stdout)
			}
			parsed := firstErrorJSONLine(t, stdout)
			if code, _ := parsed["code"].(string); code != "ERR_USAGE" {
				t.Fatalf("expected code=ERR_USAGE, got %v", parsed["code"])
			}
			if msg, _ := parsed["message"].(string); msg != tc.want {
				t.Fatalf("expected message %q, got %q", tc.want, msg)
			}
		})
	}
}

// TestCopyCLINotExactStderrOnce guards AUDIT5 §4.6: on the ERR_AUDIT_NOT_EXACT
// path, compat copy must emit the []Finding array to stderr and the error line
// to stderr EXACTLY ONCE (the former code printed the error line twice — once
// explicitly and once via cliout.Die), with the typed envelope on stdout. The
// audit fails before any store is opened, so this needs no PostgreSQL.
func TestCopyCLINotExactStderrOnce(t *testing.T) {
	dir := t.TempDir()
	schema := compat.Schema{Tables: []compat.Table{{
		Name: "fixb5_notexact",
		Columns: []compat.Column{
			{Name: "id", Type: compat.Type{Family: compat.IntegerType}},
			{Name: "title", Type: compat.Type{Family: compat.TextType}},
		},
		Constraints: []compat.Constraint{{Kind: compat.PrimaryKey, Columns: []string{"id"}}},
	}}}
	// "views" is a generic family that audits as unknown, so RequireExact fails
	// at the audit step, before the destination DSN is ever used.
	configuration := map[string]any{
		"source_dsn":      "file:" + filepath.ToSlash(filepath.Join(dir, "src.db")),
		"destination_dsn": "file:/nonexistent/not-reached.db",
		"contract": compat.Contract{
			Source:           compat.Target{Engine: compat.SQLite, Version: compat.Version{Major: 3}},
			Destination:      compat.Target{Engine: compat.Postgres, Version: compat.Version{Major: 17, Minor: 5}},
			RequiredFeatures: []compat.Feature{compat.Views},
		},
		"schema": schema,
	}
	data, err := json.Marshal(configuration)
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(dir, "migration.json")
	if err := os.WriteFile(configPath, data, 0o600); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, exitCode := runBuiltCLI(t, "cmd/compat", "copy", configPath)
	if exitCode != 1 {
		t.Fatalf("expected exit 1 for not-exact copy, got %d\nstdout:\n%s\nstderr:\n%s", exitCode, stdout, stderr)
	}
	parsed := firstErrorJSONLine(t, stdout)
	if code, _ := parsed["code"].(string); code != "ERR_AUDIT_NOT_EXACT" {
		t.Fatalf("expected code=ERR_AUDIT_NOT_EXACT, got %v\nstdout:\n%s", parsed["code"], stdout)
	}
	// The structured []Finding payload is a single JSON line on stderr.
	findingsLines := 0
	for _, line := range strings.Split(stderr, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "[") {
			findingsLines++
		}
	}
	if findingsLines != 1 {
		t.Fatalf("expected exactly one findings JSON line on stderr, got %d\nstderr:\n%s", findingsLines, stderr)
	}
	// The error line appears exactly once on stderr (not twice).
	if got := strings.Count(stderr, `feature "views" is unknown`); got != 1 {
		t.Fatalf("expected the error line once on stderr, got %d\nstderr:\n%s", got, stderr)
	}
}

// TestCopyCLIDivergedStderrOnce guards AUDIT5 §4.6 on the ERR_VERIFY_DIVERGED
// path against real PostgreSQL: compat copy must emit the VerificationReport to
// stderr and the error line to stderr EXACTLY ONCE (the former code printed the
// error line twice), with the typed envelope on stdout and exit 1. The
// divergence is genuine and reproducible: a NUMERIC(38,18) column pads "0.10" to
// the declared scale ("0.100000000000000000") on Postgres, so the destination
// digest differs from the SQLite TEXT source digest. This is a fixture for the
// diverged stderr contract, not a claim about decimal-migration correctness.
func TestCopyCLIDivergedStderrOnce(t *testing.T) {
	ctx := context.Background()
	schema := compat.Schema{Tables: []compat.Table{{
		Name: "fixb5_div",
		Columns: []compat.Column{
			{Name: "id", Type: compat.Type{Family: compat.IntegerType}},
			{Name: "amount", Type: compat.Type{Family: compat.DecimalType, Arguments: []int{38, 18}}},
		},
		Constraints: []compat.Constraint{{Kind: compat.PrimaryKey, Columns: []string{"id"}}},
	}}}

	sourcePath := filepath.Join(t.TempDir(), "div-source.db")
	source, err := compat.OpenSQLite(compat.Version{Major: 3}, "file:"+filepath.ToSlash(sourcePath))
	if err != nil {
		t.Fatal(err)
	}
	if err := source.ApplySchema(ctx, schema); err != nil {
		t.Fatal(err)
	}
	if _, err := source.DB.ExecContext(ctx, `INSERT INTO fixb5_div (id, amount) VALUES (?, ?)`, 1, "0.10"); err != nil {
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
	configPath := filepath.Join(t.TempDir(), "div-migration.json")
	if err := os.WriteFile(configPath, data, 0o600); err != nil {
		t.Fatal(err)
	}

	// The destination must be empty for the described objects (import is
	// additive); other tests share the e2e PG database, so drop the table first.
	postgres := openPostgres(t)
	_, _ = postgres.DB.ExecContext(ctx, `DROP TABLE IF EXISTS fixb5_div`)

	stdout, stderr, exitCode := runBuiltCLI(t, "cmd/compat", "copy", configPath)
	if exitCode != 1 {
		t.Fatalf("expected exit 1 for diverged copy, got %d\nstdout:\n%s\nstderr:\n%s", exitCode, stdout, stderr)
	}
	parsed := firstErrorJSONLine(t, stdout)
	if code, _ := parsed["code"].(string); code != "ERR_VERIFY_DIVERGED" {
		t.Fatalf("expected code=ERR_VERIFY_DIVERGED, got %v\nstdout:\n%s", parsed["code"], stdout)
	}
	// The structured VerificationReport is emitted to stderr exactly once.
	if got := strings.Count(stderr, `"equivalent":false`); got != 1 {
		t.Fatalf("expected the VerificationReport once on stderr, got %d\nstderr:\n%s", got, stderr)
	}
	// The error line appears exactly once on stderr (not twice).
	if got := strings.Count(stderr, `snapshot mismatch: source`); got != 1 {
		t.Fatalf("expected the error line once on stderr, got %d\nstderr:\n%s", got, stderr)
	}
}
