// compat-copy transfers a canonical schema and snapshot between SQLite and
// PostgreSQL. It refuses plans that do not currently prove exact equivalence.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

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
		os.Exit(2)
	}
	data, err := os.ReadFile(os.Args[1])
	if err != nil {
		fatal(err)
	}
	var config migrationConfig
	if err := json.Unmarshal(data, &config); err != nil {
		fatal(err)
	}
	if err := config.Schema.Validate(); err != nil {
		fatal(err)
	}
	config.Contract.RequiredFeatures = append(config.Contract.RequiredFeatures, compat.InferFeatures(config.Schema)...)
	findings, err := compat.Audit(config.Contract)
	if err != nil {
		fatal(err)
	}
	if err := compat.RequireExact(findings); err != nil {
		fatal(err)
	}

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

	snapshot, err := source.ExportSnapshot(ctx, config.Schema)
	if err != nil {
		fatal(err)
	}
	if err := destination.ImportSnapshot(ctx, snapshot); err != nil {
		fatal(err)
	}
	destinationSnapshot, err := destination.ExportSnapshot(ctx, config.Schema)
	if err != nil {
		fatal(err)
	}
	report, err := compat.VerifySnapshots(snapshot, destinationSnapshot)
	if err != nil {
		fatal(err)
	}
	if err := compat.RequireEquivalent(report); err != nil {
		fatal(err)
	}
	if err := json.NewEncoder(os.Stdout).Encode(report); err != nil {
		fatal(err)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
