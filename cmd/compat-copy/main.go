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
	SchemaRef      string          `json:"schema_ref,omitempty"`
}

func main() {
	_, positional := cliout.ParseArgsStrict(nil, os.Args[1:], 1,
		"uso: compat-copy <migration.json>",
		"compat-copy: unexpected flag %q",
		"compat-copy requires exactly one migration JSON argument")
	var config migrationConfig
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
	config.Contract.RequiredFeatures = append(config.Contract.RequiredFeatures, compat.InferFeatures(config.Schema)...)
	findings, err := compat.Audit(config.Contract)
	if err != nil {
		cliout.Die(cliout.ErrConfig, err)
	}
	if err := compat.RequireExact(findings); err != nil {
		// The full findings array is emitted to stderr before the typed error
		// envelope, mirroring compat-cutover exactly: an agent debugging a
		// non-exact audit via compat-copy gets every feature verdict, not just
		// the first failing one carried in the envelope's message.
		fmt.Fprintln(os.Stderr, err)
		encoded, _ := json.Marshal(findings)
		fmt.Fprintln(os.Stderr, string(encoded))
		cliout.Die(cliout.ErrAuditNotExact, err)
	}

	ctx := context.Background()
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

	snapshot, err := source.ExportSnapshot(ctx, config.Schema)
	if err != nil {
		cliout.Die(cliout.ErrSnapshot, err)
	}
	if err := destination.ImportSnapshot(ctx, snapshot); err != nil {
		cliout.Die(cliout.ErrSnapshot, err)
	}
	destinationSnapshot, err := destination.ExportSnapshot(ctx, config.Schema)
	if err != nil {
		cliout.Die(cliout.ErrSnapshot, err)
	}
	report, err := compat.VerifySnapshots(snapshot, destinationSnapshot)
	if err != nil {
		cliout.Die(cliout.ErrInternal, err)
	}
	if err := compat.RequireEquivalent(report); err != nil {
		// The structured VerificationReport (carrying both digests) is emitted to
		// stderr before the typed error envelope, consistent with the findings
		// path above: the digests survive as parseable JSON rather than only as
		// free text inside the envelope's message field.
		fmt.Fprintln(os.Stderr, err)
		encoded, _ := json.Marshal(report)
		fmt.Fprintln(os.Stderr, string(encoded))
		cliout.Die(cliout.ErrVerifyDiverged, err)
	}
	if err := cliout.EmitJSON(report); err != nil {
		cliout.Die(cliout.ErrInternal, err)
	}
}
