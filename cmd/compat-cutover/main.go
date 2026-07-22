// compat-cutover runs a zero-window SQLite -> PostgreSQL cutover: it audits the
// contract, installs change capture on the source, snapshots the source into the
// destination, drains the overlapping change journal with the tolerant catch-up
// policy, and verifies equivalence. It refuses plans that do not prove exact
// equivalence.
//
// Cutting the application's DSN over to the destination is NOT this tool's
// responsibility and must be done manually once it prints status=ready.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"example.com/sqlite-postgres-compat/compat"
)

type cutoverConfig struct {
	SourceDSN      string          `json:"source_dsn"`
	DestinationDSN string          `json:"destination_dsn"`
	Contract       compat.Contract `json:"contract"`
	Schema         compat.Schema   `json:"schema"`
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
	SourceDigest      string `json:"source_digest"`
	DestinationDigest string `json:"destination_digest"`
	ChangesApplied    int    `json:"changes_applied"`
}

func main() {
	if len(os.Args) != 2 {
		usage()
		os.Exit(2)
	}
	data, err := os.ReadFile(os.Args[1])
	if err != nil {
		fatal(err)
	}
	var config cutoverConfig
	if err := json.Unmarshal(data, &config); err != nil {
		fatal(err)
	}
	if err := config.Schema.Validate(); err != nil {
		fatal(err)
	}
	options := config.Options.withDefaults()

	config.Contract.RequiredFeatures = append(config.Contract.RequiredFeatures, compat.InferFeatures(config.Schema)...)
	findings, err := compat.Audit(config.Contract)
	if err != nil {
		fatal(err)
	}
	if err := compat.RequireExact(findings); err != nil {
		fmt.Fprintln(os.Stderr, err)
		encoded, _ := json.Marshal(findings)
		fmt.Fprintln(os.Stderr, string(encoded))
		os.Exit(1)
	}
	logf("audit: exact coverage for %d required features", len(findings))

	ctx := context.Background()
	source, err := compat.OpenStore(config.Contract.Source, config.SourceDSN)
	if err != nil {
		fatal(err)
	}
	defer source.Close()
	destination, err := compat.OpenStore(config.Contract.Destination, config.DestinationDSN)
	if err != nil {
		fatal(err)
	}
	defer destination.Close()

	// The destination schema and rows are brought up together by ImportSnapshot
	// below. A separate ApplySchema is intentionally omitted: ImportSnapshot
	// creates the canonical schema (tables, constraints, triggers, metadata) in
	// one transaction, so calling ApplySchema first would duplicate the tables
	// and fail with "table already exists" on a fresh destination.

	if err := source.InstallChangeCapture(ctx, config.Schema); err != nil {
		fatal(err)
	}
	logf("capture: change capture installed on source")

	snapshot, err := source.ExportSnapshot(ctx, config.Schema)
	if err != nil {
		fatal(err)
	}
	if err := destination.ImportSnapshot(ctx, snapshot); err != nil {
		fatal(err)
	}
	logf("snapshot: imported into destination")

	applied, err := drainChanges(ctx, source, destination, config.Schema, options)
	if err != nil {
		fatal(err)
	}
	logf("catch-up: drained after %d changes", applied)

	sourceSnapshot, err := source.ExportSnapshot(ctx, config.Schema)
	if err != nil {
		fatal(err)
	}
	destinationSnapshot, err := destination.ExportSnapshot(ctx, config.Schema)
	if err != nil {
		fatal(err)
	}
	report, err := compat.VerifySnapshots(sourceSnapshot, destinationSnapshot)
	if err != nil {
		fatal(err)
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
	}
	if err := json.NewEncoder(os.Stdout).Encode(out); err != nil {
		fatal(err)
	}
	if !report.Equivalent {
		os.Exit(1)
	}
}

// drainChanges replays the source journal into the destination with the tolerant
// catch-up policy. Because capture was installed before the snapshot, changes
// journaled during the overlap window already traveled inside the snapshot;
// ApplyChangesTolerant treats those as already applied instead of raising a
// spurious conflict. The stream is considered drained after drain_polls
// consecutive empty reads, waiting poll_interval_ms between polls.
func drainChanges(ctx context.Context, source, destination *compat.Store, schema compat.Schema, options cutoverOptions) (int, error) {
	cursor := uint64(0)
	applied := 0
	empty := 0
	interval := time.Duration(options.PollIntervalMs) * time.Millisecond
	for {
		changes, err := source.ReadCapturedChanges(ctx, schema, cursor, options.BatchLimit)
		if err != nil {
			return applied, err
		}
		if len(changes) == 0 {
			empty++
			if empty >= options.DrainPolls {
				return applied, nil
			}
			select {
			case <-time.After(interval):
			case <-ctx.Done():
				return applied, ctx.Err()
			}
			continue
		}
		empty = 0
		if err := destination.ApplyChangesTolerant(ctx, schema, changes); err != nil {
			return applied, err
		}
		applied += len(changes)
		cursor = changes[len(changes)-1].Sequence
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "uso: compat-cutover <cutover.json>")
	fmt.Fprintln(os.Stderr, "el corte del DSN de la aplicación no es responsabilidad de esta herramienta: cortá la conexión de la app manualmente tras recibir status=ready.")
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}

func logf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "compat-cutover: "+format+"\n", args...)
}