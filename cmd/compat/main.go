// Binary compat is the single entry point for the SQLite -> PostgreSQL
// compatibility toolchain. It dispatches to one of three subcommands — audit,
// copy, cutover — that previously lived in three separate binaries
// (compat-audit, compat-copy, compat-cutover). Each subcommand preserves the
// observable behavior of its former binary byte-for-byte (same JSON envelopes,
// exit codes, streams, line order, findings/reports/dry-run plan) with one
// deliberate exception: the message prefixes changed from "compat-audit:" /
// "compat-copy:" / "compat-cutover:" to "compat audit:" / "compat copy:" /
// "compat cutover:".
//
// Invoked with no subcommand, an unknown subcommand, or any --help-ish leading
// token, it emits the shared usage hint to stderr and a typed ERR_USAGE JSON
// envelope to stdout, exiting 2 — the same style as each subcommand's own
// usage path.
package main

import (
	"errors"
	"fmt"
	"os"

	"example.com/sqlite-postgres-compat/cmd/internal/cliout"
)

// usageHint is the top-level usage printed to stderr when compat is invoked
// with a missing or unrecognized subcommand. It mirrors the style of each
// subcommand's own usage hint: a one-line "uso" form followed by the subcommand
// list.
const usageHint = `uso: compat <subcommand> [flags] <config.json>
subcomandos:
  compat audit <contract.json>
  compat copy <migration.json>
  compat cutover [--dry-run] <cutover.json>`

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		usageFail()
	}
	switch args[0] {
	case "audit":
		runAudit(args[1:])
	case "copy":
		runCopy(args[1:])
	case "cutover":
		runCutover(args[1:])
	default:
		// Any leading token that is not a known subcommand — including --help-ish
		// flags like --help/-h and unknown subcommand names — is an ERR_USAGE
		// (exit 2), never silently treated as a positional config path.
		usageFail()
	}
}

// usageFail prints the top-level usage hint to stderr and emits a typed
// ERR_USAGE envelope to stdout, exiting 2. It is the dispatch-level counterpart
// to each subcommand's ParseArgsStrict usage path.
func usageFail() {
	fmt.Fprintln(os.Stderr, usageHint)
	cliout.Die(cliout.ErrUsage, errors.New("compat: missing or unknown subcommand"))
}
