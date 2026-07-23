package main

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/MauricioPerera/sqlite-postgres-compat/compat"

	_ "github.com/jackc/pgx/v5/stdlib"
	_ "github.com/tursodatabase/libsql-client-go/libsql"
)

// This file holds the Post-VectorType validation: the first-class canonical
// vector type (Family=vector, Arguments=[N]) is exercised end-to-end against
// the live remote engines. Tables use the vexp2_ prefix and are cleaned up
// best-effort. None of these tests use t.Skip; an incompatibility is asserted
// and recorded, never skipped.

// ---- vexp2_ cleanup helpers (best-effort, vexp2_ prefix only) ----

func cleanupLibSQL2(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := context.Background()
	rows, err := db.QueryContext(ctx, `SELECT name, type FROM sqlite_master WHERE name LIKE 'vexp2_%' ORDER BY type DESC`)
	if err != nil {
		t.Logf("libsql cleanup2 query: %v", err)
		return
	}
	type obj struct{ name, kind string }
	var objs []obj
	for rows.Next() {
		var o obj
		if err := rows.Scan(&o.name, &o.kind); err == nil {
			objs = append(objs, o)
		}
	}
	rows.Close()
	for _, o := range objs {
		var stmt string
		switch o.kind {
		case "trigger":
			stmt = "DROP TRIGGER IF EXISTS " + qi(o.name)
		case "view":
			stmt = "DROP VIEW IF EXISTS " + qi(o.name)
		case "index":
			stmt = "DROP INDEX IF EXISTS " + qi(o.name)
		default:
			stmt = "DROP TABLE IF EXISTS " + qi(o.name)
		}
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Logf("libsql cleanup2 %s %s: %v", o.kind, o.name, err)
		}
	}
}

func cleanupPG2(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := context.Background()
	rows, err := db.QueryContext(ctx, `SELECT relname, relkind FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=current_schema() AND relname LIKE 'vexp2_%'`)
	if err != nil {
		t.Logf("pg cleanup2 query: %v", err)
		return
	}
	type obj struct{ name, kind string }
	var objs []obj
	for rows.Next() {
		var o obj
		if err := rows.Scan(&o.name, &o.kind); err == nil {
			objs = append(objs, o)
		}
	}
	rows.Close()
	for _, o := range objs {
		var stmt string
		switch o.kind {
		case "v":
			stmt = "DROP VIEW IF EXISTS " + qi(o.name) + " CASCADE"
		default:
			stmt = "DROP TABLE IF EXISTS " + qi(o.name) + " CASCADE"
		}
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Logf("pg cleanup2 %s %s: %v", o.kind, o.name, err)
		}
	}
}

// dropCompatMetadata removes the compat-internal tables so InspectSchema falls
// back to engine catalog inspection instead of returning a stored canonical
// schema blob written by a prior ApplySchema/ImportSnapshot. Without this, a
// leftover __compat_schema row from another test would shadow the table under
// inspection. Best-effort: missing tables are expected on a fresh schema.
func dropCompatMetadata(t *testing.T, db *sql.DB, ctx context.Context, engine string) {
	t.Helper()
	tables := []string{"__compat_schema", "__compat_applied_changes", "__compat_capture_state", "__compat_change_journal"}
	for _, name := range tables {
		stmt := `DROP TABLE IF EXISTS "` + name + `"`
		if engine == "postgres" {
			stmt += " CASCADE"
		}
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Logf("drop %s: %v", name, err)
		}
	}
}

// vectorSchema3 is the canonical schema used across the Post-VectorType tests:
// id integer PK + emb vector(3).
func vectorSchema3(table string) compat.Schema {
	return compat.Schema{Tables: []compat.Table{{
		Name: table,
		Columns: []compat.Column{
			{Name: "id", Type: compat.Type{Family: compat.IntegerType}},
			{Name: "emb", Type: compat.Type{Family: compat.VectorType, Arguments: []int{3}}},
		},
		Constraints: []compat.Constraint{{Kind: compat.PrimaryKey, Columns: []string{"id"}}},
	}}}
}

func sqliteStore(db *sql.DB) *compat.Store {
	return &compat.Store{Target: compat.Target{Engine: compat.SQLite, Version: compat.Version{Major: 1, Minor: 0, Patch: 0}}, DB: db}
}

func postgresStore(db *sql.DB) *compat.Store {
	return &compat.Store{Target: compat.Target{Engine: compat.Postgres, Version: compat.Version{Major: 17, Minor: 0, Patch: 0}}, DB: db}
}

// pgColumnType returns (type name, atttypmod) for a column from pg_attribute.
// pg_attribute is the catalog that actually exposes atttypmod; the more obvious
// information_schema.columns does NOT carry an atttypmod column in PostgreSQL 17
// (selecting it raises 42703), which is also why compat.InspectSchema's column
// query fails against real PG — see TestPostVectorTypeInspectPG.
func pgColumnType(t *testing.T, db *sql.DB, ctx context.Context, table, column string) (string, int) {
	t.Helper()
	var typName string
	var typmod int
	if err := db.QueryRowContext(ctx,
		`SELECT t.typname, a.atttypmod FROM pg_attribute a JOIN pg_class c ON c.oid=a.attrelid JOIN pg_namespace n ON n.oid=c.relnamespace JOIN pg_type t ON t.oid=a.atttypid WHERE n.nspname=current_schema() AND c.relname=$1 AND a.attname=$2`,
		table, column).Scan(&typName, &typmod); err != nil {
		t.Fatalf("pg_attribute query for %s.%s: %v", table, column, err)
	}
	return typName, typmod
}

// =====================================================================
// 1. APPLY: canonical vector(3) schema -> real vector(3) on PG, TEXT on libSQL
// =====================================================================

func TestPostVectorTypeApplySchema(t *testing.T) {
	libDB := openLibSQL(t)
	defer libDB.Close()
	defer cleanupLibSQL2(t, libDB)
	pgDB := openPG(t)
	defer pgDB.Close()
	defer cleanupPG2(t, pgDB)
	ctx := ctxSec(t, 180)
	dropCompatMetadata(t, libDB, ctx, "sqlite")
	dropCompatMetadata(t, pgDB, ctx, "postgres")

	schema := vectorSchema3("vexp2_apply")

	// PG: ApplySchema must emit a real vector(3) column.
	if err := postgresStore(pgDB).ApplySchema(ctx, schema); err != nil {
		t.Fatalf("PG ApplySchema: %v", err)
	}
	t.Logf("PG ApplySchema (canonical vector(3)): OK")
	pgTypName, pgTypmod := pgColumnType(t, pgDB, ctx, "vexp2_apply", "emb")
	t.Logf("PG vexp2_apply.emb pg_type=%q atttypmod=%d", pgTypName, pgTypmod)
	if pgTypName != "vector" {
		t.Fatalf("expected PG type %q, got %q", "vector", pgTypName)
	}
	if pgTypmod != 3 {
		t.Logf("NOTE: PG atttypmod=%d (3 expected if atttypmod is the dimension)", pgTypmod)
	}

	// libSQL: ApplySchema (SQLite engine, hand-built store with the libsql conn)
	// must emit a TEXT column as the interoperable carrier.
	if err := sqliteStore(libDB).ApplySchema(ctx, schema); err != nil {
		t.Fatalf("libSQL ApplySchema: %v", err)
	}
	t.Logf("libSQL ApplySchema (TEXT carrier for vector): OK")
	var libType string
	if err := libDB.QueryRowContext(ctx,
		`SELECT type FROM pragma_table_xinfo('vexp2_apply') WHERE name='emb'`).Scan(&libType); err != nil {
		t.Fatalf("libSQL pragma_table_xinfo query: %v", err)
	}
	t.Logf("libSQL vexp2_apply.emb declared type = %q", libType)
	if !strings.EqualFold(libType, "TEXT") {
		t.Fatalf("expected libSQL TEXT column for vector, got %q", libType)
	}
}

// =====================================================================
// 2 + 6. SNAPSHOT libSQL -> PG + VERIFY: native vector column, direct distance
// =====================================================================

func TestPostVectorTypeSnapshotAndVerify(t *testing.T) {
	libDB := openLibSQL(t)
	defer libDB.Close()
	defer cleanupLibSQL2(t, libDB)
	pgDB := openPG(t)
	defer pgDB.Close()
	defer cleanupPG2(t, pgDB)
	ctx := ctxSec(t, 240)
	dropCompatMetadata(t, libDB, ctx, "sqlite")
	dropCompatMetadata(t, pgDB, ctx, "postgres")

	schema := vectorSchema3("vexp2_snap")

	// Origin: libSQL stores the canonical vector as TEXT.
	src := sqliteStore(libDB)
	if err := src.ApplySchema(ctx, schema); err != nil {
		t.Fatalf("libSQL ApplySchema: %v", err)
	}
	mustExec(t, libDB, ctx, "INSERT INTO vexp2_snap(id,emb) VALUES(1,'[1,2,3]')")
	mustExec(t, libDB, ctx, "INSERT INTO vexp2_snap(id,emb) VALUES(2,'[4,5,6]')")

	snap, err := src.ExportSnapshot(ctx, schema)
	if err != nil {
		t.Fatalf("ExportSnapshot: %v", err)
	}
	rows := snap.Rows["vexp2_snap"]
	if len(rows) != 2 {
		t.Fatalf("expected 2 exported rows, got %d", len(rows))
	}
	t.Logf("exported %d rows from libSQL:", len(rows))
	for i, r := range rows {
		ev := r["emb"]
		t.Logf("  snap row[%d] id=%v emb={%s %s}", i, r["id"], ev.Kind, ev.Value)
		if ev.Kind != compat.VectorValue {
			t.Fatalf("row %d emb kind=%s, want %s", i, ev.Kind, compat.VectorValue)
		}
	}

	// Destination: PG receives a native vector(3) column with real vectors.
	dst := postgresStore(pgDB)
	if err := dst.ImportSnapshot(ctx, snap); err != nil {
		t.Fatalf("ImportSnapshot to PG: %v", err)
	}
	t.Logf("ImportSnapshot to PG (native vector(3) column): OK")

	pgTypName, pgTypmod := pgColumnType(t, pgDB, ctx, "vexp2_snap", "emb")
	t.Logf("PG vexp2_snap.emb pg_type=%q atttypmod=%d", pgTypName, pgTypmod)
	if pgTypName != "vector" {
		t.Fatalf("expected PG type %q, got %q", "vector", pgTypName)
	}

	// The advance vs the pre-VectorType matrix: the distance is computed DIRECTLY
	// on the native vector column, with no manual `emb::vector` cast on the column
	// (only the literal is cast, as is standard pgvector usage).
	var dist float64
	if err := pgDB.QueryRowContext(ctx,
		`SELECT emb <=> '[1,2,3]'::vector FROM vexp2_snap WHERE id=2`).Scan(&dist); err != nil {
		t.Fatalf("PG direct emb <=> distance: %v", err)
	}
	t.Logf("PG emb <=> '[1,2,3]' (direct on native vector column, no cast) = %v", dist)
	if dist <= 0 || dist >= 1 {
		t.Fatalf("expected cosine distance in (0,1), got %v", dist)
	}

	// ANN index directly on the native vector column (no expression/dimension
	// cast). This is the manual ANN step, now working on (emb) directly.
	_, idxErr := pgDB.ExecContext(ctx, "CREATE INDEX vexp2_snap_idx ON vexp2_snap USING hnsw (emb vector_cosine_ops)")
	if idxErr != nil {
		t.Fatalf("native ANN index on emb: %v", idxErr)
	}
	t.Logf("native ANN index (emb hnsw vector_cosine_ops): CREATED directly on native vector column, no cast")

	// VERIFY: re-export from PG and compare digests. Both sides canonicalize the
	// vector to the same "[1,2,3]"/"[4,5,6]" text, so Equivalent must be true.
	pgSnap, err := dst.ExportSnapshot(ctx, schema)
	if err != nil {
		t.Fatalf("re-export from PG: %v", err)
	}
	t.Logf("re-exported %d rows from PG:", len(pgSnap.Rows["vexp2_snap"]))
	for i, r := range pgSnap.Rows["vexp2_snap"] {
		t.Logf("  pgSnap row[%d] id=%v emb={%s %s}", i, r["id"], r["emb"].Kind, r["emb"].Value)
	}
	report, err := compat.VerifySnapshots(snap, pgSnap)
	if err != nil {
		t.Fatalf("VerifySnapshots: %v", err)
	}
	t.Logf("VerifySnapshots source=%s destination=%s equivalent=%v",
		report.SourceDigest, report.DestinationDigest, report.Equivalent)
	if !report.Equivalent {
		t.Fatalf("expected snapshots equivalent, got source=%s destination=%s", report.SourceDigest, report.DestinationDigest)
	}
}

// =====================================================================
// 3. INCREMENTAL REPLICATION: capture on libSQL -> ApplyChanges on PG
// =====================================================================

func TestPostVectorTypeIncrementalReplication(t *testing.T) {
	libDB := openLibSQL(t)
	defer libDB.Close()
	defer cleanupLibSQL2(t, libDB)
	pgDB := openPG(t)
	defer pgDB.Close()
	defer cleanupPG2(t, pgDB)
	ctx := ctxSec(t, 300)
	dropCompatMetadata(t, libDB, ctx, "sqlite")
	dropCompatMetadata(t, pgDB, ctx, "postgres")

	schema := vectorSchema3("vexp2_repl")
	src := sqliteStore(libDB)

	if err := src.ApplySchema(ctx, schema); err != nil {
		t.Fatalf("libSQL ApplySchema: %v", err)
	}
	// Row present BEFORE capture starts: it will not be journaled; only later
	// mutations are replicated incrementally.
	mustExec(t, libDB, ctx, "INSERT INTO vexp2_repl(id,emb) VALUES(1,'[1,2,3]')")

	if err := src.InstallChangeCapture(ctx, schema); err != nil {
		t.Fatalf("InstallChangeCapture on sqld: %v", err)
	}
	t.Logf("InstallChangeCapture on sqld: OK (triggers installed)")

	var cursor uint64
	_ = libDB.QueryRowContext(ctx, `SELECT COALESCE(MAX(sequence),0) FROM "__compat_change_journal"`).Scan(&cursor)
	t.Logf("capture cursor (max prior sequence) = %d", cursor)

	// Mutation AFTER capture: must be journaled as a canonical vector change.
	mustExec(t, libDB, ctx, "INSERT INTO vexp2_repl(id,emb) VALUES(2,'[4,5,6]')")

	changes, err := src.ReadCapturedChanges(ctx, schema, cursor, 100)
	if err != nil {
		// A dimension error here, if it appears, is the expected side-effect of the
		// in-flight capture.go/replicate.go work noted in the task brief.
		t.Fatalf("ReadCapturedChanges: %v", err)
	}
	t.Logf("captured %d change(s) from sqld:", len(changes))
	for i, c := range changes {
		t.Logf("  change[%d] seq=%d kind=%s table=%s after=%v", i, c.Sequence, c.Kind, c.Table, c.After)
	}
	if len(changes) == 0 {
		t.Fatalf("expected at least 1 captured change (insert id=2), got 0")
	}
	var ins *compat.Change
	for i := range changes {
		if changes[i].Kind == compat.Insert && changes[i].Table == "vexp2_repl" {
			ins = &changes[i]
		}
	}
	if ins == nil {
		t.Fatalf("no insert change captured for vexp2_repl")
	}
	emb := ins.After["emb"]
	t.Logf("captured insert id=2 emb={%s %s}", emb.Kind, emb.Value)
	if emb.Kind != compat.VectorValue || emb.Value != "[4,5,6]" {
		t.Fatalf("expected captured vector {%s [4,5,6]}, got {%s %s}", compat.VectorValue, emb.Kind, emb.Value)
	}

	// Apply the incremental stream to PG (native vector(3) column).
	dst := postgresStore(pgDB)
	if err := dst.ApplySchema(ctx, schema); err != nil {
		t.Fatalf("PG ApplySchema: %v", err)
	}
	if err := dst.ApplyChanges(ctx, schema, changes); err != nil {
		// Likewise, a dimension-mismatch error here would be the expected in-flight
		// capture.go/replicate.go side-effect; record it literally.
		t.Fatalf("ApplyChanges on PG: %v", err)
	}
	t.Logf("ApplyChanges on PG: OK")

	var pgV string
	if err := pgDB.QueryRowContext(ctx, `SELECT emb::text FROM vexp2_repl WHERE id=2`).Scan(&pgV); err != nil {
		t.Fatalf("read replicated row from PG: %v", err)
	}
	t.Logf("PG vexp2_repl id=2 emb=%q", pgV)

	var dist float64
	if err := pgDB.QueryRowContext(ctx,
		`SELECT emb <=> '[1,2,3]'::vector FROM vexp2_repl WHERE id=2`).Scan(&dist); err != nil {
		t.Fatalf("PG direct emb <=> distance on replicated row: %v", err)
	}
	t.Logf("PG emb <=> '[1,2,3]' (replicated row, direct on native column) = %v", dist)
	if dist <= 0 || dist >= 1 {
		t.Fatalf("expected cosine distance in (0,1), got %v", dist)
	}
}

// =====================================================================
// 4. PG INSPECTION: InspectSchema against real PG returns the vector column
//    with Family=vector and Arguments=[3] (the atttypmod==dimension mapping,
//    now sourced from pg_attribute). The bare pg_attribute supposition is still
//    validated directly. A companion test covers a table with NO vector column,
//    since the prior regression (atttypmod selected from information_schema) broke
//    InspectSchema against ANY real PG table, vector or not.
// =====================================================================

// findColumn returns the inspected column named column from table table, or
// fatals if either the table or the column is absent from the inspection.
func findColumn(t *testing.T, insp compat.Inspection, table, column string) compat.Column {
	t.Helper()
	for _, tbl := range insp.Schema.Tables {
		if tbl.Name != table {
			continue
		}
		for _, c := range tbl.Columns {
			if c.Name == column {
				return c
			}
		}
		t.Fatalf("column %s.%s not found in inspection", table, column)
	}
	t.Fatalf("table %s not found in inspection", table)
	return compat.Column{}
}

func TestPostVectorTypeInspectPG(t *testing.T) {
	pgDB := openPG(t)
	defer pgDB.Close()
	defer cleanupPG2(t, pgDB)
	ctx := ctxSec(t, 180)
	dropCompatMetadata(t, pgDB, ctx, "postgres")

	// Create the vector(3) table DIRECTLY, outside compat, so the catalog (not a
	// stored schema blob) is the sole source of truth for the inspection.
	mustExec(t, pgDB, ctx, "DROP TABLE IF EXISTS vexp2_inspg")
	mustExec(t, pgDB, ctx, "CREATE TABLE vexp2_inspg(id INT PRIMARY KEY, v vector(3))")

	// Observe the raw atttypmod straight from pg_attribute: this is the
	// supposition under test, validated independently of the compat wrapper.
	var rawTypmod int
	if err := pgDB.QueryRowContext(ctx,
		`SELECT a.atttypmod FROM pg_attribute a JOIN pg_class c ON c.oid=a.attrelid JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=current_schema() AND c.relname='vexp2_inspg' AND a.attname='v'`).Scan(&rawTypmod); err != nil {
		t.Fatalf("raw pg_attribute.atttypmod query: %v", err)
	}
	t.Logf("raw pg_attribute.atttypmod for vexp2_inspg.v = %d", rawTypmod)
	if rawTypmod != 3 {
		t.Fatalf("expected raw atttypmod=3 (the vector(3) dimension), got %d", rawTypmod)
	}
	t.Logf("CONFIRMED (direct pg_attribute): atttypmod=%d IS the pgvector dimension for vector(3)", rawTypmod)

	// Happy path: InspectSchema now runs against real PG17 (the atttypmod source
	// was moved off information_schema.columns to a pg_attribute scalar subquery)
	// and surfaces the vector column as Family=vector, Arguments=[3].
	store := postgresStore(pgDB)
	insp, err := store.InspectSchema(ctx)
	if err != nil {
		t.Fatalf("compat.InspectSchema against real PG17: %v", err)
	}
	t.Logf("InspectSchema Exact=%v unresolved=%d", insp.Exact, len(insp.Unresolved))
	for _, u := range insp.Unresolved {
		t.Logf("  unresolved: kind=%s name=%s reason=%s", u.Kind, u.Name, u.Reason)
	}
	col := findColumn(t, insp, "vexp2_inspg", "v")
	t.Logf("inspected vexp2_inspg.v family=%q args=%v (raw atttypmod=%d)", col.Type.Family, col.Type.Arguments, rawTypmod)
	if col.Type.Family != compat.VectorType {
		t.Fatalf("expected inspected family %q, got %q", compat.VectorType, col.Type.Family)
	}
	if len(col.Type.Arguments) != 1 || col.Type.Arguments[0] != rawTypmod {
		t.Fatalf("expected inspected args=[%d] mirroring atttypmod, got %v", rawTypmod, col.Type.Arguments)
	}
	// The PK id column must still inspect as integer (the regression broke ALL
	// columns, not just the vector one).
	idCol := findColumn(t, insp, "vexp2_inspg", "id")
	if idCol.Type.Family != compat.IntegerType {
		t.Fatalf("expected id family %q, got %q", compat.IntegerType, idCol.Type.Family)
	}
	t.Logf("inspected vexp2_inspg.id family=%q (non-vector column inspects correctly)", idCol.Type.Family)
}

// TestPostVectorTypeInspectPGNonVector guards the no-vector case: the prior
// regression (atttypmod selected from information_schema.columns) made
// InspectSchema fail against ANY real PG table, so a plain table with no vector
// column must also inspect cleanly through the fixed query.
func TestPostVectorTypeInspectPGNonVector(t *testing.T) {
	pgDB := openPG(t)
	defer pgDB.Close()
	defer cleanupPG2(t, pgDB)
	ctx := ctxSec(t, 180)
	dropCompatMetadata(t, pgDB, ctx, "postgres")

	mustExec(t, pgDB, ctx, "DROP TABLE IF EXISTS vexp2_inspn")
	mustExec(t, pgDB, ctx, "CREATE TABLE vexp2_inspn(id INT PRIMARY KEY, name TEXT, qty INT, price NUMERIC(38,18), created TIMESTAMP)")

	store := postgresStore(pgDB)
	insp, err := store.InspectSchema(ctx)
	if err != nil {
		t.Fatalf("compat.InspectSchema against real PG17 (no-vector table): %v", err)
	}
	t.Logf("InspectSchema (no-vector) Exact=%v unresolved=%d", insp.Exact, len(insp.Unresolved))
	for _, u := range insp.Unresolved {
		t.Logf("  unresolved: kind=%s name=%s reason=%s", u.Kind, u.Name, u.Reason)
	}

	type expect struct {
		col    string
		family compat.TypeFamily
	}
	for _, e := range []expect{
		{"id", compat.IntegerType},
		{"name", compat.TextType},
		{"qty", compat.IntegerType},
		{"price", compat.DecimalType},
		{"created", compat.TimestampType},
	} {
		c := findColumn(t, insp, "vexp2_inspn", e.col)
		t.Logf("inspected vexp2_inspn.%s family=%q", e.col, c.Type.Family)
		if c.Type.Family != e.family {
			t.Fatalf("vexp2_inspn.%s: expected family %q, got %q", e.col, e.family, c.Type.Family)
		}
	}
	// No vector column exists, so none of the inspected columns may carry the
	// vector family; this is the no-regression assertion for the non-vector path.
	for _, tbl := range insp.Schema.Tables {
		if tbl.Name != "vexp2_inspn" {
			continue
		}
		for _, c := range tbl.Columns {
			if c.Type.Family == compat.VectorType {
				t.Fatalf("non-vector table vexp2_inspn.%s unexpectedly inspected as vector", c.Name)
			}
		}
	}
}
