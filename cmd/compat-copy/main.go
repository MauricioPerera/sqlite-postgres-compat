// compat-copy transfers a canonical schema and snapshot between SQLite and
// PostgreSQL. It refuses plans that do not currently prove exact equivalence.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"example.com/sqlite-postgres-compat/cmd/internal/cliout"
	"example.com/sqlite-postgres-compat/compat"
)

type migrationConfig struct {
	SourceDSN      string          `json:"source_dsn"`
	DestinationDSN string          `json:"destination_dsn"`
	Contract       compat.Contract `json:"contract"`
	Schema         compat.Schema   `json:"schema"`
}

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "uso: compat-copy <migration.json>")
		os.Exit(cliout.EmitError(cliout.ErrUsage, "compat-copy requires exactly one migration JSON argument"))
	}
	data, err := os.ReadFile(os.Args[1])
	if err != nil {
		fail(cliout.ErrConfig, err)
	}
	var config migrationConfig
	if err := json.Unmarshal(data, &config); err != nil {
		fail(cliout.ErrConfig, err)
	}
	if err := config.Schema.Validate(); err != nil {
		fail(cliout.ErrSchema, err)
	}
	config.Contract.RequiredFeatures = append(config.Contract.RequiredFeatures, compat.InferFeatures(config.Schema)...)
	findings, err := compat.Audit(config.Contract)
	if err != nil {
		fail(cliout.ErrConfig, err)
	}
	if err := compat.RequireExact(findings); err != nil {
		fail(cliout.ErrAuditNotExact, err)
	}

	ctx := context.Background()
	source, err := compat.OpenStore(config.Contract.Source, config.SourceDSN)
	if err != nil {
		fail(cliout.ErrConnectSource, err)
	}
	defer source.Close()
	if err := source.DB.PingContext(ctx); err != nil {
		fail(cliout.ErrConnectSource, err)
	}
	destination, err := compat.OpenStore(config.Contract.Destination, config.DestinationDSN)
	if err != nil {
		fail(cliout.ErrConnectDestination, err)
	}
	defer destination.Close()
	if err := destination.DB.PingContext(ctx); err != nil {
		fail(cliout.ErrConnectDestination, err)
	}

	snapshot, err := source.ExportSnapshot(ctx, config.Schema)
	if err != nil {
		fail(cliout.ErrSnapshot, err)
	}
	if err := destination.ImportSnapshot(ctx, snapshot); err != nil {
		fail(cliout.ErrSnapshot, err)
	}
	destinationSnapshot, err := destination.ExportSnapshot(ctx, config.Schema)
	if err != nil {
		fail(cliout.ErrSnapshot, err)
	}
	report, err := compat.VerifySnapshots(snapshot, destinationSnapshot)
	if err != nil {
		fail(cliout.ErrInternal, err)
	}
	if err := compat.RequireEquivalent(report); err != nil {
		fail(cliout.ErrVerifyDiverged, err)
	}
	if err := cliout.EmitJSON(report); err != nil {
		fail(cliout.ErrInternal, err)
	}
}

// fail prints the error to stderr, emits the typed error JSON to stdout, and
// exits with the code's canonical exit status. It does not return.
func fail(code cliout.ErrorCode, err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(cliout.EmitErrorFrom(code, err))
}