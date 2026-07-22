package main

import (
	"fmt"
	"os"

	"example.com/sqlite-postgres-compat/cmd/internal/cliout"
	"example.com/sqlite-postgres-compat/compat"
)

func main() {
	_, positional, unexpected, ok := cliout.SplitArgs(nil, os.Args[1:])
	if !ok {
		fmt.Fprintln(os.Stderr, "uso: compat-audit <contract.json>")
		os.Exit(cliout.EmitError(cliout.ErrUsage, fmt.Sprintf("compat-audit: unexpected flag %q", unexpected)))
	}
	if len(positional) != 1 {
		fmt.Fprintln(os.Stderr, "uso: compat-audit <contract.json>")
		os.Exit(cliout.EmitError(cliout.ErrUsage, "compat-audit requires exactly one contract JSON argument"))
	}

	var contract compat.Contract
	if err := cliout.DecodeFileStrict(positional[0], &contract); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(cliout.EmitError(cliout.ErrConfig, err.Error()))
	}
	findings, err := compat.Audit(contract)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(cliout.EmitError(cliout.ErrConfig, err.Error()))
	}
	// The feature findings are always emitted first so an agent can inspect the
	// per-feature verdicts even when the audit is not exact.
	if err := cliout.EmitJSON(findings); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(cliout.EmitError(cliout.ErrInternal, err.Error()))
	}
	if err := compat.RequireExact(findings); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(cliout.EmitError(cliout.ErrAuditNotExact, err.Error()))
	}
}