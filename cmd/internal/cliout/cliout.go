// Package cliout centralizes the machine-facing stdout protocol shared by the
// compat CLIs. Every CLI emits a single-line JSON result on success and, on any
// failure, a single-line typed error JSON on stdout:
//
//	{"status":"error","code":"<CODE>","message":"<detalle>"}
//
// An agent can parse stdout line-by-line and branch on the "code" field without
// scraping free-text stderr. The taxonomy is closed: callers pick the most
// specific applicable code from the constants below. The public compat/ API is
// not extended here; errors are classified by phase (which step failed) and, for
// replication, by errors.As against the existing compat.ConflictError.
package cliout

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"example.com/sqlite-postgres-compat/compat"
)

// ErrorCode is one value from the closed CLI error taxonomy.
type ErrorCode string

const (
	// ErrUsage is incorrect CLI invocation (wrong argument count); exit 2.
	ErrUsage ErrorCode = "ERR_USAGE"
	// ErrConfig is an unreadable or invalid JSON config (file read, unmarshal,
	// or contract validation). Exit 1.
	ErrConfig ErrorCode = "ERR_CONFIG"
	// ErrAuditNotExact is a required feature whose audit status is not exact.
	// The feature findings are still emitted before this error line. Exit 1.
	ErrAuditNotExact ErrorCode = "ERR_AUDIT_NOT_EXACT"
	// ErrConnectSource is a failure to reach or open the source store. Exit 1.
	ErrConnectSource ErrorCode = "ERR_CONNECT_SOURCE"
	// ErrConnectDestination is a failure to reach or open the destination store. Exit 1.
	ErrConnectDestination ErrorCode = "ERR_CONNECT_DESTINATION"
	// ErrSchema is a schema validation or ApplySchema failure. Exit 1.
	ErrSchema ErrorCode = "ERR_SCHEMA"
	// ErrSnapshot is an export/import snapshot failure. Exit 1.
	ErrSnapshot ErrorCode = "ERR_SNAPSHOT"
	// ErrReplicationConflict is a compat.ConflictError raised while replaying
	// the change journal during catch-up. Exit 1.
	ErrReplicationConflict ErrorCode = "ERR_REPLICATION_CONFLICT"
	// ErrCapture is a change-capture install or read failure. Exit 1.
	ErrCapture ErrorCode = "ERR_CAPTURE"
	// ErrVerifyDiverged is a digest mismatch at verification. The CLI still
	// emits its diverged result JSON with this code. Exit 1.
	ErrVerifyDiverged ErrorCode = "ERR_VERIFY_DIVERGED"
	// ErrInternal is any failure not covered by a more specific code. Exit 1.
	ErrInternal ErrorCode = "ERR_INTERNAL"
)

type errorEnvelope struct {
	Status  string    `json:"status"`
	Code    ErrorCode `json:"code"`
	Message string    `json:"message"`
}

// ExitCode returns the process exit code for a typed error. Incorrect usage is
// exit 2; every other failure is exit 1, preserving each CLI's existing codes.
func ExitCode(code ErrorCode) int {
	if code == ErrUsage {
		return 2
	}
	return 1
}

// EmitError writes a single-line typed error JSON to stdout and returns the
// exit code the caller should pass to os.Exit. The message is JSON-encoded, so
// embedded newlines in the underlying error never break the one-line contract.
func EmitError(code ErrorCode, message string) int {
	encoded, err := json.Marshal(errorEnvelope{Status: "error", Code: code, Message: message})
	if err != nil {
		// A struct of strings cannot fail to marshal in practice; fall back to a
		// minimal envelope so stdout always carries parseable JSON.
		fmt.Fprintf(os.Stdout, `{"status":"error","code":%q,"message":"failed to encode error"}`, string(code))
		return ExitCode(code)
	}
	fmt.Fprintln(os.Stdout, string(encoded))
	return ExitCode(code)
}

// EmitErrorFrom writes the typed error JSON for err using code and returns the
// exit code. A nil err is rendered as the code itself.
func EmitErrorFrom(code ErrorCode, err error) int {
	if err == nil {
		err = errors.New(string(code))
	}
	return EmitError(code, err.Error())
}

// EmitJSON marshals v to compact, single-line JSON and writes it to stdout with
// a trailing newline. It is the success-path counterpart to EmitError.
func EmitJSON(v any) error {
	encoded, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(os.Stdout, string(encoded))
	return err
}

// ReplicationCode classifies an error from the catch-up drain loop. A true
// ConflictError raised while applying the journal is ErrReplicationConflict;
// anything else from the drain is ErrInternal. Capture-install and
// capture-read failures are classified by the caller as ErrCapture before they
// reach this helper.
func ReplicationCode(err error) ErrorCode {
	var conflict *compat.ConflictError
	if errors.As(err, &conflict) {
		return ErrReplicationConflict
	}
	return ErrInternal
}