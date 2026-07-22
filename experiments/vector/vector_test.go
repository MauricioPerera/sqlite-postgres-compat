package main

import (
	"context"
	"database/sql"
	"encoding/base64"
	"os"
	"strings"
	"testing"
	"time"

	"example.com/sqlite-postgres-compat/compat"

	_ "github.com/jackc/pgx/v5/stdlib"
	_ "github.com/tursodatabase/libsql-client-go/libsql"
)

// Environment configuration. Both engines are remote (~100-300ms RTT), so the
// suite relies on the generous -timeout flag passed on the command line and
// keeps row counts tiny.
func libsqlURL() string {
	if v := os.Getenv("VECTOR_LIBSQL_URL"); v != "" {
		return v
	}
	return "http://localhost:8081"
}

func pgDSN() string {
	if v := os.Getenv("VECTOR_PG_DSN"); v != "" {
		return v
	}
	return "postgres://postgres:postgres@localhost:5434/postgres?sslmode=disable"
}

func openLibSQL(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("libsql", libsqlURL())
	if err != nil {
		t.Fatalf("open libsql: %v", err)
	}
	db.SetMaxOpenConns(1)
	return db
}

func openPG(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("pgx", pgDSN())
	if err != nil {
		t.Fatalf("open pg: %v", err)
	}
	return db
}

// maskDSN hides the password before pasting DSNs into evidence.
func maskDSN(dsn string) string {
	at := strings.Index(dsn, "@")
	colon := strings.Index(dsn, "://")
	if colon == -1 || at == -1 || at < colon {
		return dsn
	}
	scheme := dsn[:colon+3]
	rest := dsn[at:]
	return scheme + "postgres:***" + rest
}

// ---- cleanup helpers (best-effort, vexp_ prefix only) ----

func cleanupLibSQL(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := context.Background()
	rows, err := db.QueryContext(ctx, `SELECT name, type FROM sqlite_master WHERE name LIKE 'vexp_%' ORDER BY type DESC`)
	if err != nil {
		t.Logf("libsql cleanup query: %v", err)
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
			t.Logf("libsql cleanup %s %s: %v", o.kind, o.name, err)
		}
	}
}

func cleanupPG(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := context.Background()
	// Drop tables and views first.
	rows, err := db.QueryContext(ctx, `SELECT relname, relkind FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=current_schema() AND relname LIKE 'vexp_%'`)
	if err != nil {
		t.Logf("pg cleanup query: %v", err)
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
			t.Logf("pg cleanup %s %s: %v", o.kind, o.name, err)
		}
	}
	// Drop functions.
	frows, err := db.QueryContext(ctx, `SELECT proname FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname=current_schema() AND proname LIKE 'vexp_%'`)
	if err == nil {
		var fns []string
		for frows.Next() {
			var n string
			if frows.Scan(&n) == nil {
				fns = append(fns, n)
			}
		}
		frows.Close()
		for _, n := range fns {
			if _, err := db.ExecContext(ctx, "DROP FUNCTION IF EXISTS "+qi(n)+" CASCADE"); err != nil {
				t.Logf("pg cleanup fn %s: %v", n, err)
			}
		}
	}
}

// qi quotes an identifier for SQLite/Postgres (double quotes, escaped).
func qi(name string) string {
	return "\"" + strings.ReplaceAll(name, "\"", "\"\"") + "\""
}

func ctxSec(t *testing.T, seconds int) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(seconds)*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// =====================================================================
// 1. SANITY per engine (direct, no compat layer)
// =====================================================================

func TestSanityDirectLibSQL(t *testing.T) {
	db := openLibSQL(t)
	defer db.Close()
	defer cleanupLibSQL(t, db)
	ctx := ctxSec(t, 120)

	mustExec(t, db, ctx, "DROP TABLE IF EXISTS vexp_san_l")
	mustExec(t, db, ctx, "CREATE TABLE vexp_san_l(id INTEGER PRIMARY KEY, v F32_BLOB(3))")
	mustExec(t, db, ctx, "CREATE INDEX vexp_san_l_idx ON vexp_san_l(libsql_vector_idx(v))")
	mustExec(t, db, ctx, "INSERT INTO vexp_san_l(id,v) VALUES(1, vector32('[1,2,3]'))")
	mustExec(t, db, ctx, "INSERT INTO vexp_san_l(id,v) VALUES(2, vector32('[4,5,6]'))")

	// cosine distance between the two vectors.
	var dist float64
	if err := db.QueryRowContext(ctx, "SELECT vector_distance_cos(v, vector32('[1,2,3]')) FROM vexp_san_l WHERE id=2").Scan(&dist); err != nil {
		t.Fatalf("libsql distance: %v", err)
	}
	t.Logf("libsql vector_distance_cos([4,5,6],[1,2,3]) = %v", dist)
	if dist <= 0 || dist >= 1 {
		t.Fatalf("expected cosine distance in (0,1), got %v", dist)
	}

	// vector_top_k ANN retrieval.
	var topID int
	if err := db.QueryRowContext(ctx, "SELECT id FROM vector_top_k('vexp_san_l_idx', vector32('[1,2,3]'), 1)").Scan(&topID); err != nil {
		t.Fatalf("libsql vector_top_k: %v", err)
	}
	t.Logf("libsql vector_top_k(...,1) -> id=%d (nearest to [1,2,3])", topID)
	if topID != 1 {
		t.Fatalf("expected nearest id=1, got %d", topID)
	}
}

func TestSanityDirectPgvector(t *testing.T) {
	db := openPG(t)
	defer db.Close()
	defer cleanupPG(t, db)
	ctx := ctxSec(t, 120)

	mustExec(t, db, ctx, "DROP TABLE IF EXISTS vexp_san_p")
	mustExec(t, db, ctx, "CREATE TABLE vexp_san_p(id INT PRIMARY KEY, v vector(3))")
	mustExec(t, db, ctx, "INSERT INTO vexp_san_p(id,v) VALUES(1, '[1,2,3]')")
	mustExec(t, db, ctx, "INSERT INTO vexp_san_p(id,v) VALUES(2, '[4,5,6]')")

	var dist float64
	if err := db.QueryRowContext(ctx, "SELECT v <=> '[1,2,3]'::vector FROM vexp_san_p WHERE id=2").Scan(&dist); err != nil {
		t.Fatalf("pg <=>: %v", err)
	}
	t.Logf("pgvector [4,5,6] <=> [1,2,3] = %v", dist)
	if dist <= 0 || dist >= 1 {
		t.Fatalf("expected cosine distance in (0,1), got %v", dist)
	}
	t.Logf("pgvector DSN: %s", maskDSN(pgDSN()))
}

// =====================================================================
// 2. INSPECTION of a native F32_BLOB column through the compat layer
//    (Post-VectorType: F32_BLOB(N) is now recognized as the canonical
//    vector family with dimension N, no longer silently misread as binary.)
// =====================================================================

func TestInspectF32BlobMapsToVectorFamily(t *testing.T) {
	db := openLibSQL(t)
	defer db.Close()
	defer cleanupLibSQL(t, db)
	ctx := ctxSec(t, 120)

	// Force catalog inspection (no canonical metadata) so the column family is
	// read purely from the sqld catalog, not from a stored schema blob.
	dropCompatMetadata(t, db, ctx, "sqlite")

	mustExec(t, db, ctx, "DROP TABLE IF EXISTS vexp_inspect")
	mustExec(t, db, ctx, "CREATE TABLE vexp_inspect(id INTEGER PRIMARY KEY, v F32_BLOB(3))")

	// Store is constructed BY HAND, exactly as the prompt requires: the SQLite
	// engine target is paired with a libsql driver connection to the remote sqld.
	store := &compat.Store{
		Target: compat.Target{Engine: compat.SQLite, Version: compat.Version{Major: 1, Minor: 0, Patch: 0}},
		DB:     db,
	}

	insp, err := store.InspectSchema(ctx)
	if err != nil {
		// An error here would itself be evidence; capture it literally.
		t.Fatalf("InspectSchema returned error: %v", err)
	}
	t.Logf("InspectSchema Exact=%v unresolved=%d", insp.Exact, len(insp.Unresolved))
	for _, u := range insp.Unresolved {
		t.Logf("  unresolved: kind=%s name=%s reason=%s", u.Kind, u.Name, u.Reason)
	}

	var col compat.Column
	found := false
	for _, tbl := range insp.Schema.Tables {
		if tbl.Name == "vexp_inspect" {
			for _, c := range tbl.Columns {
				if c.Name == "v" {
					col = c
					found = true
				}
			}
		}
	}
	if !found {
		t.Fatalf("column vexp_inspect.v not found in inspection")
	}
	t.Logf("inspected family of vexp_inspect.v (declared F32_BLOB(3)) = %q args=%v", col.Type.Family, col.Type.Arguments)

	// Post-VectorType: sqliteTypeFamily matches "F32_BLOB" before the generic
	// "BLOB" rule and returns VectorType, and parseTypeArguments extracts the
	// declared dimension 3. This is the revised verdict for matrix row 2: the
	// column is now inspected as a vector of dimension 3, not a silent binary.
	if col.Type.Family != compat.VectorType {
		t.Fatalf("expected F32_BLOB to be inspected as %q, got %q", compat.VectorType, col.Type.Family)
	}
	if len(col.Type.Arguments) != 1 || col.Type.Arguments[0] != 3 {
		t.Fatalf("expected vector dimension [3], got %v", col.Type.Arguments)
	}
}

// =====================================================================
// 3. BINARY route: raw F32_BLOB bytes through snapshot libSQL -> PG bytea
// =====================================================================

func TestSnapshotBinaryRoute(t *testing.T) {
	libDB := openLibSQL(t)
	defer libDB.Close()
	defer cleanupLibSQL(t, libDB)
	pgDB := openPG(t)
	defer pgDB.Close()
	defer cleanupPG(t, pgDB)
	ctx := ctxSec(t, 180)

	mustExec(t, libDB, ctx, "DROP TABLE IF EXISTS vexp_bin")
	mustExec(t, libDB, ctx, "CREATE TABLE vexp_bin(id INTEGER PRIMARY KEY, v F32_BLOB(3))")
	mustExec(t, libDB, ctx, "INSERT INTO vexp_bin(id,v) VALUES(1, vector32('[1,2,3]'))")

	// Raw bytes as stored by libSQL (3 x float32 little-endian = 12 bytes).
	var raw []byte
	if err := libDB.QueryRowContext(ctx, "SELECT v FROM vexp_bin WHERE id=1").Scan(&raw); err != nil {
		t.Fatalf("read raw libsql bytes: %v", err)
	}
	t.Logf("libSQL raw F32_BLOB bytes (len=%d): %v", len(raw), raw)

	schema := compat.Schema{
		Tables: []compat.Table{{
			Name: "vexp_bin",
			Columns: []compat.Column{
				{Name: "id", Type: compat.Type{Family: compat.IntegerType}},
				{Name: "v", Type: compat.Type{Family: compat.BinaryType}},
			},
			Constraints: []compat.Constraint{{Kind: compat.PrimaryKey, Columns: []string{"id"}}},
		}},
	}

	src := &compat.Store{Target: compat.Target{Engine: compat.SQLite, Version: compat.Version{Major: 1, Minor: 0, Patch: 0}}, DB: libDB}
	snap, err := src.ExportSnapshot(ctx, schema)
	if err != nil {
		t.Fatalf("ExportSnapshot: %v", err)
	}
	if len(snap.Rows["vexp_bin"]) != 1 {
		t.Fatalf("expected 1 exported row, got %d", len(snap.Rows["vexp_bin"]))
	}
	vVal := snap.Rows["vexp_bin"][0]["v"]
	t.Logf("exported canonical value for v: kind=%s base64=%s", vVal.Kind, vVal.Value)
	if vVal.Kind != compat.BinaryValue {
		t.Fatalf("expected BinaryValue, got %s", vVal.Kind)
	}
	expBytes, _ := base64.StdEncoding.DecodeString(vVal.Value)
	t.Logf("decoded snapshot bytes (len=%d): %v", len(expBytes), expBytes)

	dst := &compat.Store{Target: compat.Target{Engine: compat.Postgres, Version: compat.Version{Major: 17, Minor: 0, Patch: 0}}, DB: pgDB}
	if err := dst.ImportSnapshot(ctx, snap); err != nil {
		t.Fatalf("ImportSnapshot to PG: %v", err)
	}
	t.Logf("ImportSnapshot to PG bytea OK")

	// Verify the bytes arrived identical in PG bytea.
	var pgBytes []byte
	if err := pgDB.QueryRowContext(ctx, "SELECT v FROM vexp_bin WHERE id=1").Scan(&pgBytes); err != nil {
		t.Fatalf("read PG bytea: %v", err)
	}
	t.Logf("PG bytea bytes (len=%d): %v", len(pgBytes), pgBytes)
	if len(pgBytes) != len(raw) {
		t.Fatalf("byte length mismatch: libsql=%d pg=%d", len(raw), len(pgBytes))
	}
	for i := range raw {
		if raw[i] != pgBytes[i] {
			t.Fatalf("byte mismatch at %d: %d != %d", i, raw[i], pgBytes[i])
		}
	}
	t.Logf("bytes identical libSQL -> PG bytea: PASS")

	// Now demonstrate that bytea is NOT directly castable to a pgvector vector.
	_, castErr := pgDB.ExecContext(ctx, "SELECT v::vector FROM vexp_bin WHERE id=1")
	if castErr == nil {
		t.Fatalf("expected bytea::vector to fail, but it succeeded")
	}
	t.Logf("bytea::vector error (literal): %q", castErr.Error())
}

// =====================================================================
// 4. TEXT route: incremental replication libSQL -> PG, then use as vector
// =====================================================================

func TestTextReplicationRoute(t *testing.T) {
	libDB := openLibSQL(t)
	defer libDB.Close()
	defer cleanupLibSQL(t, libDB)
	pgDB := openPG(t)
	defer pgDB.Close()
	defer cleanupPG(t, pgDB)
	ctx := ctxSec(t, 240)

	schema := compat.Schema{
		Tables: []compat.Table{{
			Name: "vexp_txt",
			Columns: []compat.Column{
				{Name: "id", Type: compat.Type{Family: compat.IntegerType}},
				{Name: "v", Type: compat.Type{Family: compat.TextType}},
			},
			Constraints: []compat.Constraint{{Kind: compat.PrimaryKey, Columns: []string{"id"}}},
		}},
	}

	mustExec(t, libDB, ctx, "DROP TABLE IF EXISTS vexp_txt")
	mustExec(t, libDB, ctx, "CREATE TABLE vexp_txt(id INTEGER PRIMARY KEY, v TEXT)")
	// Row inserted BEFORE capture starts: it will NOT be in the journal; only
	// later mutations are replicated incrementally.
	mustExec(t, libDB, ctx, "INSERT INTO vexp_txt(id,v) VALUES(1, '[1,2,3]')")

	src := &compat.Store{Target: compat.Target{Engine: compat.SQLite, Version: compat.Version{Major: 1, Minor: 0, Patch: 0}}, DB: libDB}

	// Install engine-native capture triggers on sqld and document the outcome.
	installErr := src.InstallChangeCapture(ctx, schema)
	if installErr != nil {
		// DOCUMENTED fallback: if sqld rejects the generated triggers, fall back
		// to a full snapshot. The incompatibility is recorded, not skipped.
		t.Logf("InstallChangeCapture on sqld FAILED (literal): %q", installErr.Error())
		t.Logf("FALLBACK: using ExportSnapshot/ImportSnapshot instead of incremental capture")
		runTextSnapshotFallback(t, ctx, libDB, pgDB, schema)
		return
	}
	t.Logf("InstallChangeCapture on sqld: OK (triggers installed)")

	// The capture journal persists across runs (it is not a vexp_ table). Use
	// the current high-water mark as the cursor so only new mutations are read.
	var cursor uint64
	_ = libDB.QueryRowContext(ctx, "SELECT COALESCE(MAX(sequence),0) FROM \"__compat_change_journal\"").Scan(&cursor)
	t.Logf("capture cursor (max prior sequence) = %d", cursor)

	// Mutation AFTER capture: must be journaled by the sqld triggers.
	mustExec(t, libDB, ctx, "INSERT INTO vexp_txt(id,v) VALUES(2, '[4,5,6]')")

	changes, err := src.ReadCapturedChanges(ctx, schema, cursor, 100)
	if err != nil {
		t.Fatalf("ReadCapturedChanges: %v", err)
	}
	t.Logf("captured %d change(s) from sqld:", len(changes))
	for i, c := range changes {
		t.Logf("  change[%d] seq=%d kind=%s table=%s after=%v", i, c.Sequence, c.Kind, c.Table, c.After)
	}
	if len(changes) == 0 {
		t.Fatalf("expected at least 1 captured change (insert id=2), got 0 -- sqld triggers did not journal")
	}

	// Prepare the destination table in PG, then apply the incremental stream.
	dst := &compat.Store{Target: compat.Target{Engine: compat.Postgres, Version: compat.Version{Major: 17, Minor: 0, Patch: 0}}, DB: pgDB}
	if err := dst.ApplySchema(ctx, schema); err != nil {
		t.Fatalf("ApplySchema on PG: %v", err)
	}
	if err := dst.ApplyChanges(ctx, schema, changes); err != nil {
		t.Fatalf("ApplyChanges on PG: %v", err)
	}
	t.Logf("ApplyChanges on PG: OK")

	// Verify the incrementally-replicated row landed in PG.
	var pgV string
	if err := pgDB.QueryRowContext(ctx, "SELECT v FROM vexp_txt WHERE id=2").Scan(&pgV); err != nil {
		t.Fatalf("read replicated row from PG: %v", err)
	}
	t.Logf("PG vexp_txt id=2 v=%q", pgV)
	if pgV != "[4,5,6]" {
		t.Fatalf("expected replicated text '[4,5,6]', got %q", pgV)
	}

	// Demonstrate the text is usable as a pgvector vector via cast.
	var dist float64
	if err := pgDB.QueryRowContext(ctx, "SELECT v::vector <=> '[1,2,3]'::vector FROM vexp_txt WHERE id=2").Scan(&dist); err != nil {
		t.Fatalf("PG v::vector <=> distance: %v", err)
	}
	t.Logf("PG v::vector <=> '[1,2,3]' = %v", dist)

	// Attempt an ANN index on the expression (col::vector). Document whether
	// pgvector permits it.
	_, idxErr := pgDB.ExecContext(ctx, "CREATE INDEX vexp_txt_idx ON vexp_txt USING hnsw ((v::vector) vector_cosine_ops)")
	if idxErr != nil {
		t.Logf("expression ANN index ((v::vector)) error (literal): %q", idxErr.Error())
	} else {
		t.Logf("expression ANN index ((v::vector) hnsw vector_cosine_ops): CREATED")
	}

	// Manual step: pgvector needs a fixed dimension for an ANN index. Try the
	// expression with an explicit dimension cast.
	_, idxDimErr := pgDB.ExecContext(ctx, "CREATE INDEX vexp_txt_idx_dim ON vexp_txt USING hnsw ((v::vector(3)) vector_cosine_ops)")
	if idxDimErr != nil {
		t.Logf("expression ANN index ((v::vector(3))) error (literal): %q", idxDimErr.Error())
	} else {
		t.Logf("expression ANN index ((v::vector(3)) hnsw vector_cosine_ops): CREATED (manual dimension step)")
	}
}

func runTextSnapshotFallback(t *testing.T, ctx context.Context, libDB, pgDB *sql.DB, schema compat.Schema) {
	t.Helper()
	// Ensure a second row exists to demonstrate the snapshot carries text.
	mustExec(t, libDB, ctx, "INSERT INTO vexp_txt(id,v) VALUES(2, '[4,5,6]')")
	src := &compat.Store{Target: compat.Target{Engine: compat.SQLite, Version: compat.Version{Major: 1, Minor: 0, Patch: 0}}, DB: libDB}
	snap, err := src.ExportSnapshot(ctx, schema)
	if err != nil {
		t.Fatalf("fallback ExportSnapshot: %v", err)
	}
	t.Logf("fallback ExportSnapshot rows=%v", snap.Rows["vexp_txt"])
	dst := &compat.Store{Target: compat.Target{Engine: compat.Postgres, Version: compat.Version{Major: 17, Minor: 0, Patch: 0}}, DB: pgDB}
	if err := dst.ImportSnapshot(ctx, snap); err != nil {
		t.Fatalf("fallback ImportSnapshot: %v", err)
	}
	var pgV string
	if err := pgDB.QueryRowContext(ctx, "SELECT v FROM vexp_txt WHERE id=2").Scan(&pgV); err != nil {
		t.Fatalf("fallback read PG: %v", err)
	}
	t.Logf("fallback PG vexp_txt id=2 v=%q", pgV)
	if pgV != "[4,5,6]" {
		t.Fatalf("expected '[4,5,6]', got %q", pgV)
	}
	var dist float64
	if err := pgDB.QueryRowContext(ctx, "SELECT v::vector <=> '[1,2,3]'::vector FROM vexp_txt WHERE id=2").Scan(&dist); err != nil {
		t.Fatalf("fallback PG v::vector <=>: %v", err)
	}
	t.Logf("fallback PG v::vector <=> '[1,2,3]' = %v", dist)
}

// =====================================================================
// 5. FUNCTION TRANSLATION: the catalog parser rejects vector_distance_cos
// =====================================================================

func TestCatalogParserRejectsVectorDistanceCos(t *testing.T) {
	db := openLibSQL(t)
	defer db.Close()
	defer cleanupLibSQL(t, db)
	ctx := ctxSec(t, 120)

	mustExec(t, db, ctx, "DROP TABLE IF EXISTS vexp_fn_t")
	mustExec(t, db, ctx, "DROP VIEW IF EXISTS vexp_fn_v")
	mustExec(t, db, ctx, "CREATE TABLE vexp_fn_t(id INTEGER PRIMARY KEY, v F32_BLOB(3))")
	mustExec(t, db, ctx, "INSERT INTO vexp_fn_t(id,v) VALUES(1, vector32('[1,2,3]'))")
	// A view that uses the vector distance function. InspectSchema parses view
	// definitions through the bounded catalog grammar.
	mustExec(t, db, ctx, "CREATE VIEW vexp_fn_v AS SELECT vector_distance_cos(v, vector32('[1,2,3]')) AS d FROM vexp_fn_t")

	store := &compat.Store{
		Target: compat.Target{Engine: compat.SQLite, Version: compat.Version{Major: 1, Minor: 0, Patch: 0}},
		DB:     db,
	}
	insp, err := store.InspectSchema(ctx)
	if err != nil {
		t.Fatalf("InspectSchema returned error: %v", err)
	}

	var reason string
	found := false
	for _, u := range insp.Unresolved {
		t.Logf("  unresolved: kind=%s name=%s reason=%s", u.Kind, u.Name, u.Reason)
		if u.Name == "vexp_fn_v" || strings.Contains(u.Reason, "vector_distance_cos") {
			reason = u.Reason
			found = true
		}
	}
	if !found {
		t.Fatalf("expected an unresolved entry rejecting vector_distance_cos; got none. unresolved=%v", insp.Unresolved)
	}
	t.Logf("catalog rejection reason (literal): %q", reason)
	if !strings.Contains(reason, "unsupported catalog function") {
		t.Fatalf("expected reason to mention 'unsupported catalog function', got %q", reason)
	}
	t.Logf("CONFIRMED: vector query translation does not exist in the compat layer")
}

// ---- tiny exec helper ----

func mustExec(t *testing.T, db *sql.DB, ctx context.Context, query string) {
	t.Helper()
	if _, err := db.ExecContext(ctx, query); err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
}
