package main

import (
	"encoding/json"
	"fmt"
	"os"

	"example.com/sqlite-postgres-compat/cmd/internal/cliout"
	"example.com/sqlite-postgres-compat/compat"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "uso: compat-audit <contract.json>")
		os.Exit(cliout.EmitError(cliout.ErrUsage, "compat-audit requires exactly one contract JSON argument"))
	}

	data, err := os.ReadFile(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(cliout.EmitError(cliout.ErrConfig, err.Error()))
	}
	var contract compat.Contract
	if err := json.Unmarshal(data, &contract); err != nil {
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