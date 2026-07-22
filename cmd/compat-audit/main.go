package main

import (
	"os"

	"example.com/sqlite-postgres-compat/cmd/internal/cliout"
	"example.com/sqlite-postgres-compat/compat"
)

func main() {
	_, positional := cliout.ParseArgsStrict(nil, os.Args[1:], 1,
		"uso: compat-audit <contract.json>",
		"compat-audit: unexpected flag %q",
		"compat-audit requires exactly one contract JSON argument")

	var contract compat.Contract
	if err := cliout.DecodeFileStrict(positional[0], &contract); err != nil {
		cliout.Die(cliout.ErrConfig, err)
	}
	findings, err := compat.Audit(contract)
	if err != nil {
		cliout.Die(cliout.ErrConfig, err)
	}
	// The feature findings are always emitted first so an agent can inspect the
	// per-feature verdicts even when the audit is not exact.
	if err := cliout.EmitJSON(findings); err != nil {
		cliout.Die(cliout.ErrInternal, err)
	}
	if err := compat.RequireExact(findings); err != nil {
		cliout.Die(cliout.ErrAuditNotExact, err)
	}
}