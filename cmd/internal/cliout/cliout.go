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
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

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

// Die writes err to stderr, emits the typed error envelope for err on stdout, and
// exits with the code's canonical process status. It is the non-returning
// counterpart to EmitErrorFrom and centralizes the "log to stderr, emit envelope
// to stdout, os.Exit" pattern shared by every compat CLI's error path. A nil err
// is rendered as the code itself (mirroring EmitErrorFrom); callers always pass a
// non-nil error in practice.
//
// Die does not return, so any code after a Die call is unreachable to the
// compiler's flow analysis — there is no need for an explicit return statement
// following it.
func Die(code ErrorCode, err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(EmitErrorFrom(code, err))
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

// SplitArgs partitions a CLI argument list into recognized boolean flags and
// positional arguments. Every flag in this CLI surface is value-less (e.g.
// `--dry-run`), so flags never consume the following token. A token that begins
// with "-" and is not one of knownFlags is an unexpected flag: ok is false and
// unexpected is the offending token, so the caller emits ErrUsage (exit 2). This
// makes ERR_USAGE actually cover "unexpected flag" as the docs claim, instead of
// letting an unknown flag fall through to the positional config path.
//
// present holds the recognized flags that were seen (empty when knownFlags is
// empty, e.g. for compat-audit/compat-copy). positional holds the non-flag
// tokens in order; the caller still validates the expected count.
func SplitArgs(knownFlags []string, args []string) (present map[string]bool, positional []string, unexpected string, ok bool) {
	known := make(map[string]bool, len(knownFlags))
	for _, f := range knownFlags {
		known[f] = true
	}
	present = make(map[string]bool)
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			if known[a] {
				present[a] = true
				continue
			}
			return nil, nil, a, false
		}
		positional = append(positional, a)
	}
	return present, positional, "", true
}

// ParseArgsStrict is the shared front-end of every compat CLI: it partitions
// args into known boolean flags and positional arguments, rejects any unknown
// leading-dash token as ERR_USAGE (exit 2), and requires exactly wantN
// positional arguments. On a violation it prints hint to stderr, emits the
// ERR_USAGE envelope to stdout, and exits — it never returns on a violation. On
// success it returns the recognized flags that were seen and the positional
// tokens in order; the caller owns any flag-specific logic (e.g. --dry-run).
//
// hint is the stderr usage hint printed before the envelope; unexpectedMsg is
// the envelope message for an unexpected flag (formatted with %q and the
// offending token); countMsg is the envelope message for a wrong positional
// count. They are caller-supplied so each CLI keeps its existing, documented
// usage strings and envelope messages byte-for-byte — this helper only removes
// the duplicated SplitArgs + fmt.Fprintln + os.Exit plumbing, never the
// observable text or exit codes.
func ParseArgsStrict(knownFlags, args []string, wantN int, hint, unexpectedMsg, countMsg string) (present map[string]bool, positional []string) {
	present, positional, unexpected, ok := SplitArgs(knownFlags, args)
	if !ok {
		fmt.Fprintln(os.Stderr, hint)
		Die(ErrUsage, fmt.Errorf(unexpectedMsg, unexpected))
	}
	if len(positional) != wantN {
		fmt.Fprintln(os.Stderr, hint)
		Die(ErrUsage, errors.New(countMsg))
	}
	return present, positional
}

// DecodeFileStrict reads path and decodes it into v using a json.Decoder with
// DisallowUnknownFields, so an unknown key is an explicit error instead of being
// silently dropped. This closes the silent-degradation gap: a typo'd or
// unsupported config key is reported (ERR_CONFIG), not ignored, matching the
// "never silently degrade" principle the project promises.
func DecodeFileStrict(path string, v any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

// decodeSchemaRef reads the JSON file referenced by ref (holding a bare
// compat.Schema object) resolved relative to configPath's directory, and decodes
// it with DisallowUnknownFields. The path is resolved relative to the config
// file's location, not the process cwd, so a config and its schema_ref travel
// together.
func decodeSchemaRef(configPath, ref string) (compat.Schema, error) {
	resolved := ref
	if !filepath.IsAbs(resolved) {
		resolved = filepath.Join(filepath.Dir(configPath), ref)
	}
	var schema compat.Schema
	if err := DecodeFileStrict(resolved, &schema); err != nil {
		return compat.Schema{}, fmt.Errorf("schema_ref %q: %w", ref, err)
	}
	return schema, nil
}

// ResolveSchema picks the canonical schema for a cutover/migration config from
// exactly one of the inline `schema` field or a `schema_ref` path. inline is the
// decoded inline schema; ref is the schema_ref value. Exactly one must be
// populated: inline is "populated" when it declares at least one table, and ref
// is "populated" when non-empty. Both or neither is an error (ERR_CONFIG). When
// ref is set, the referenced schema file is loaded relative to configPath. The
// returned error is meant for ERR_CONFIG.
//
// Treating an empty inline schema (zero tables) as "absent" is what stops a
// config that omits both from running with no schema at all — the prior
// silent-degradation bug.
func ResolveSchema(configPath, ref string, inline compat.Schema) (compat.Schema, error) {
	hasInline := len(inline.Tables) > 0
	hasRef := ref != ""
	switch {
	case hasInline && hasRef:
		return compat.Schema{}, errors.New("config must specify exactly one of schema or schema_ref, not both")
	case !hasInline && !hasRef:
		return compat.Schema{}, errors.New("config must specify exactly one of schema or schema_ref")
	case hasRef:
		return decodeSchemaRef(configPath, ref)
	default:
		return inline, nil
	}
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