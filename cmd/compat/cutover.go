package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/MauricioPerera/sqlite-postgres-compat/cmd/internal/cliout"
	"github.com/MauricioPerera/sqlite-postgres-compat/compat"
)

// cutoverConfig is the JSON config for `compat cutover`: the two store DSNs, the
// contract, exactly one of an inline schema or a schema_ref path, and options.
type cutoverConfig struct {
	SourceDSN      string          `json:"source_dsn"`
	DestinationDSN string          `json:"destination_dsn"`
	Contract       compat.Contract `json:"contract"`
	Schema         compat.Schema   `json:"schema"`
	SchemaRef      string          `json:"schema_ref,omitempty"`
	Options        cutoverOptions  `json:"options"`
}

type cutoverOptions struct {
	PollIntervalMs int `json:"poll_interval_ms"`
	DrainPolls     int `json:"drain_polls"`
	BatchLimit     int `json:"batch_limit"`
}

func (o cutoverOptions) withDefaults() cutoverOptions {
	if o.PollIntervalMs <= 0 {
		o.PollIntervalMs = 1000
	}
	if o.DrainPolls <= 0 {
		o.DrainPolls = 3
	}
	if o.BatchLimit <= 0 {
		o.BatchLimit = 500
	}
	return o
}

type cutoverReport struct {
	Status            string `json:"status"`
	Code              string `json:"code,omitempty"`
	SourceDigest      string `json:"source_digest"`
	DestinationDigest string `json:"destination_digest"`
	ChangesApplied    int    `json:"changes_applied"`
}

// planReport is the --dry-run output: a read-only preview of what a real
// cutover would do, without installing capture, snapshotting, or writing.
type planReport struct {
	Status               string           `json:"status"`
	Audit                []compat.Finding `json:"audit"`
	SourceTables         []planTable      `json:"source_tables"`
	DestinationHasTables bool             `json:"destination_has_tables"`
	Phases               []string         `json:"phases"`
}

type planTable struct {
	Name string `json:"name"`
	Rows int    `json:"rows"`
}

// cutoverPhases is the fixed phase list a real cutover would run after the plan.
var cutoverPhases = []string{"install_capture", "snapshot", "catch_up", "verify"}

// runCutover implements `compat cutover [--dry-run] <cutover.json>`: a
// zero-window SQLite -> PostgreSQL cutover. It is the exact behavior of the
// former compat-cutover binary, with the message prefix changed from
// "compat-cutover:" to "compat cutover:".
//
// With --dry-run it executes only the read-only phases (parse config, audit
// contract, connect both stores, count source rows per contract table, detect
// whether the destination already holds those tables) and prints a plan JSON.
// It never installs capture, creates tables, or writes a journal.
//
// Cutting the application's DSN over to the destination is NOT this tool's
// responsibility and must be done manually once it prints status=ready.
func runCutover(args []string) {
	present, positional := cliout.ParseArgsStrict([]string{"--dry-run"}, args, 1,
		"uso: compat cutover [--dry-run] <cutover.json>\nel corte del DSN de la aplicación no es responsabilidad de esta herramienta: cortá la conexión de la app manualmente tras recibir status=ready.",
		"compat cutover: unexpected flag %q",
		"compat cutover: duplicate flag %q",
		"compat cutover requires exactly one cutover JSON argument")
	dryRun := present["--dry-run"]
	var config cutoverConfig
	if err := cliout.DecodeFileStrict(positional[0], &config); err != nil {
		cliout.Die(cliout.ErrConfig, err)
	}
	schema, err := cliout.ResolveSchema(positional[0], config.SchemaRef, config.Schema)
	if err != nil {
		cliout.Die(cliout.ErrConfig, err)
	}
	config.Schema = schema
	if err := config.Schema.Validate(); err != nil {
		cliout.Die(cliout.ErrSchema, err)
	}
	options := config.Options.withDefaults()

	// The audit is shared by the real flow and --dry-run: both refuse a contract
	// whose required (and inferred) features are not exact.
	config.Contract.RequiredFeatures = append(config.Contract.RequiredFeatures, compat.InferFeatures(config.Schema)...)
	findings, err := compat.Audit(config.Contract)
	if err != nil {
		cliout.Die(cliout.ErrConfig, err)
	}
	if err := compat.RequireExact(findings); err != nil {
		// The findings array is emitted to stderr before the typed error
		// envelope; the error line itself is printed once (by Die below), not
		// duplicated, so stderr carries the structured payload plus a single
		// error line and stdout carries the typed envelope.
		encoded, _ := json.Marshal(findings)
		fmt.Fprintln(os.Stderr, string(encoded))
		cliout.Die(cliout.ErrAuditNotExact, err)
	}
	logf("audit: exact coverage for %d required features", len(findings))

	ctx := context.Background()

	if dryRun {
		runDryRun(ctx, config, findings)
		return
	}

	source, err := compat.OpenStore(config.Contract.Source, config.SourceDSN)
	if err != nil {
		cliout.Die(cliout.ErrConnectSource, err)
	}
	defer source.Close()
	if err := source.DB.PingContext(ctx); err != nil {
		cliout.Die(cliout.ErrConnectSource, err)
	}
	destination, err := compat.OpenStore(config.Contract.Destination, config.DestinationDSN)
	if err != nil {
		cliout.Die(cliout.ErrConnectDestination, err)
	}
	defer destination.Close()
	if err := destination.DB.PingContext(ctx); err != nil {
		cliout.Die(cliout.ErrConnectDestination, err)
	}

	// The destination schema and rows are brought up together by ImportSnapshot
	// below. A separate ApplySchema is intentionally omitted: ImportSnapshot
	// creates the canonical schema (tables, constraints, triggers, metadata) in
	// one transaction, so calling ApplySchema first would duplicate the tables
	// and fail with "table already exists" on a fresh destination.

	if err := source.InstallChangeCapture(ctx, config.Schema); err != nil {
		cliout.Die(cliout.ErrCapture, err)
	}
	logf("capture: change capture installed on source")

	snapshot, err := source.ExportSnapshot(ctx, config.Schema)
	if err != nil {
		cliout.Die(cliout.ErrSnapshot, err)
	}
	if err := destination.ImportSnapshot(ctx, snapshot); err != nil {
		cliout.Die(cliout.ErrSnapshot, err)
	}
	logf("snapshot: imported into destination")

	applied, code, err := drainChanges(ctx, source, destination, config.Schema, options)
	if err != nil {
		cliout.Die(code, err)
	}
	logf("catch-up: drained after %d changes", applied)

	sourceSnapshot, err := source.ExportSnapshot(ctx, config.Schema)
	if err != nil {
		cliout.Die(cliout.ErrSnapshot, err)
	}
	destinationSnapshot, err := destination.ExportSnapshot(ctx, config.Schema)
	if err != nil {
		cliout.Die(cliout.ErrSnapshot, err)
	}
	report, err := compat.VerifySnapshots(sourceSnapshot, destinationSnapshot)
	if err != nil {
		cliout.Die(cliout.ErrInternal, err)
	}
	out := cutoverReport{
		SourceDigest:      report.SourceDigest,
		DestinationDigest: report.DestinationDigest,
		ChangesApplied:    applied,
	}
	if report.Equivalent {
		out.Status = "ready"
	} else {
		out.Status = "diverged"
		out.Code = string(cliout.ErrVerifyDiverged)
	}
	if err := cliout.EmitJSON(out); err != nil {
		cliout.Die(cliout.ErrInternal, err)
	}
	if !report.Equivalent {
		os.Exit(1)
	}
}

// runDryRun executes the read-only cutover preview. It audits (already done by
// the caller), connects both stores, counts source rows per contract table, and
// detects whether the destination already holds those tables, then prints a
// plan JSON and exits 0. It performs no writes on either store.
func runDryRun(ctx context.Context, config cutoverConfig, findings []compat.Finding) {
	source, err := compat.OpenStore(config.Contract.Source, config.SourceDSN)
	if err != nil {
		cliout.Die(cliout.ErrConnectSource, err)
	}
	defer source.Close()
	if err := source.DB.PingContext(ctx); err != nil {
		cliout.Die(cliout.ErrConnectSource, err)
	}
	destination, err := compat.OpenStore(config.Contract.Destination, config.DestinationDSN)
	if err != nil {
		cliout.Die(cliout.ErrConnectDestination, err)
	}
	defer destination.Close()
	if err := destination.DB.PingContext(ctx); err != nil {
		cliout.Die(cliout.ErrConnectDestination, err)
	}

	sourceTables := make([]planTable, 0, len(config.Schema.Tables))
	for _, table := range config.Schema.Tables {
		rows, err := countRows(ctx, source, table.Name)
		if err != nil {
			cliout.Die(cliout.ErrInternal, fmt.Errorf("inspect source %s: %w", table.Name, err))
		}
		sourceTables = append(sourceTables, planTable{Name: table.Name, Rows: rows})
	}

	// destination_has_tables is true iff every contract table already exists on
	// the destination; a real cutover's ImportSnapshot would collide with those.
	destinationHasTables := true
	for _, table := range config.Schema.Tables {
		exists, err := tableExists(ctx, destination, table.Name)
		if err != nil {
			cliout.Die(cliout.ErrInternal, fmt.Errorf("inspect destination %s: %w", table.Name, err))
		}
		if !exists {
			destinationHasTables = false
		}
	}

	plan := planReport{
		Status:               "plan",
		Audit:                findings,
		SourceTables:         sourceTables,
		DestinationHasTables: destinationHasTables,
		Phases:               cutoverPhases,
	}
	if err := cliout.EmitJSON(plan); err != nil {
		cliout.Die(cliout.ErrInternal, err)
	}
}

// countRows returns the row count of table on store. It is a pure SELECT.
func countRows(ctx context.Context, store *compat.Store, table string) (int, error) {
	var count int
	err := store.DB.QueryRowContext(ctx, "SELECT count(*) FROM "+quoteIdent(table)).Scan(&count)
	return count, err
}

// tableExists reports whether table exists on store, via the engine's catalog.
func tableExists(ctx context.Context, store *compat.Store, table string) (bool, error) {
	var count int
	var err error
	if store.Target.Engine == compat.Postgres {
		err = store.DB.QueryRowContext(ctx,
			"SELECT count(*) FROM information_schema.tables WHERE table_schema = 'public' AND table_name = $1", table).Scan(&count)
	} else {
		err = store.DB.QueryRowContext(ctx,
			"SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?", table).Scan(&count)
	}
	return count > 0, err
}

// quoteIdent double-quotes an identifier, doubling embedded quotes, the common
// form accepted by both SQLite and PostgreSQL.
func quoteIdent(name string) string {
	return "\"" + strings.ReplaceAll(name, "\"", "\"\"") + "\""
}

// drainChanges replays the source journal into the destination with the tolerant
// catch-up policy. Because capture was installed before the snapshot, changes
// journaled during the overlap window already traveled inside the snapshot;
// ApplyChangesTolerant treats those as already applied instead of raising a
// spurious conflict. The stream is considered drained after drain_polls
// consecutive empty reads, waiting poll_interval_ms between polls. It returns
// the count of applied changes, a typed error code classifying the failure, and
// the error itself.
func drainChanges(ctx context.Context, source, destination *compat.Store, schema compat.Schema, options cutoverOptions) (int, cliout.ErrorCode, error) {
	cursor := uint64(0)
	applied := 0
	empty := 0
	interval := time.Duration(options.PollIntervalMs) * time.Millisecond
	for {
		changes, err := source.ReadCapturedChanges(ctx, schema, cursor, options.BatchLimit)
		if err != nil {
			return applied, cliout.ErrCapture, err
		}
		if len(changes) == 0 {
			empty++
			if empty >= options.DrainPolls {
				return applied, cliout.ErrInternal, nil
			}
			select {
			case <-time.After(interval):
			case <-ctx.Done():
				return applied, cliout.ErrInternal, ctx.Err()
			}
			continue
		}
		empty = 0
		if err := destination.ApplyChangesTolerant(ctx, schema, changes); err != nil {
			return applied, cliout.ReplicationCode(err), err
		}
		applied += len(changes)
		cursor = changes[len(changes)-1].Sequence
	}
}

func logf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "compat cutover: "+format+"\n", args...)
}
