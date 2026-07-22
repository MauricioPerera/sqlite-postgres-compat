package cliout

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"example.com/sqlite-postgres-compat/compat"
)

// captureStdout runs fn with os.Stdout swapped for a pipe and returns everything
// fn wrote. The functions under test write directly to the package-level
// os.Stdout, so this is the only way to observe their output in-process. Tests
// using it must not run in parallel (they mutate a process-global).
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	done := make(chan string)
	go func() {
		buf, _ := io.ReadAll(r)
		done <- string(buf)
	}()
	fn()
	w.Close()
	os.Stdout = orig
	return <-done
}

func TestExitCode(t *testing.T) {
	if got := ExitCode(ErrUsage); got != 2 {
		t.Errorf("ExitCode(ErrUsage) = %d, want 2", got)
	}
	for _, code := range []ErrorCode{
		ErrConfig, ErrAuditNotExact, ErrConnectSource, ErrConnectDestination,
		ErrSchema, ErrSnapshot, ErrReplicationConflict, ErrCapture,
		ErrVerifyDiverged, ErrInternal,
	} {
		if got := ExitCode(code); got != 1 {
			t.Errorf("ExitCode(%s) = %d, want 1", code, got)
		}
	}
}

func TestEmitError(t *testing.T) {
	out := captureStdout(t, func() {
		EmitError(ErrConfig, "boom")
	})
	var env errorEnvelope
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &env); err != nil {
		t.Fatalf("stdout not a single JSON line: %v\nraw=%q", err, out)
	}
	if env.Status != "error" || env.Code != ErrConfig || env.Message != "boom" {
		t.Errorf("envelope = %+v, want {error ERR_CONFIG boom}", env)
	}
	if !strings.HasSuffix(out, "\n") {
		t.Errorf("stdout must end with newline, got %q", out)
	}
}

// TestEmitErrorEmbeddedNewline verifies the one-line contract: a message with a
// newline is JSON-escaped, so stdout stays a single parseable line.
func TestEmitErrorEmbeddedNewline(t *testing.T) {
	out := captureStdout(t, func() {
		EmitError(ErrInternal, "first\nsecond")
	})
	if strings.Count(out, "\n") != 1 {
		t.Fatalf("stdout must be exactly one line, got %q", out)
	}
	var env errorEnvelope
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &env); err != nil {
		t.Fatalf("unmarshal: %v raw=%q", err, out)
	}
	if env.Message != "first\nsecond" {
		t.Errorf("message = %q, want first\\nsecond", env.Message)
	}
}

func TestEmitErrorFrom(t *testing.T) {
	t.Run("non-nil", func(t *testing.T) {
		out := captureStdout(t, func() {
			EmitErrorFrom(ErrSchema, errors.New("bad schema"))
		})
		var env errorEnvelope
		if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &env); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if env.Code != ErrSchema || env.Message != "bad schema" {
			t.Errorf("envelope = %+v", env)
		}
	})
	t.Run("nil", func(t *testing.T) {
		out := captureStdout(t, func() {
			EmitErrorFrom(ErrInternal, nil)
		})
		var env errorEnvelope
		if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &env); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if env.Code != ErrInternal || env.Message != string(ErrInternal) {
			t.Errorf("nil err envelope = %+v, want message %q", env, ErrInternal)
		}
	})
}

func TestEmitJSON(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		type obj struct {
			A string `json:"a"`
			B int    `json:"b"`
		}
		out := captureStdout(t, func() {
			if err := EmitJSON(obj{A: "x", B: 2}); err != nil {
				t.Fatalf("EmitJSON: %v", err)
			}
		})
		var got obj
		if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &got); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if got.A != "x" || got.B != 2 {
			t.Errorf("got %+v", got)
		}
		if !strings.HasSuffix(out, "\n") {
			t.Errorf("missing trailing newline: %q", out)
		}
	})
	t.Run("unmarshalable", func(t *testing.T) {
		if err := EmitJSON(make(chan int)); err == nil {
			t.Errorf("EmitJSON(chan) = nil, want error")
		}
	})
}

func TestSplitArgs(t *testing.T) {
	t.Run("known flags and positionals", func(t *testing.T) {
		present, positional, unexpected, ok := SplitArgs([]string{"--dry-run"}, []string{"--dry-run", "cfg.json"})
		if !ok || unexpected != "" {
			t.Fatalf("ok=%v unexpected=%q", ok, unexpected)
		}
		if !present["--dry-run"] {
			t.Errorf("present[--dry-run]=false, want true")
		}
		if len(positional) != 1 || positional[0] != "cfg.json" {
			t.Errorf("positional=%v", positional)
		}
	})
	t.Run("unknown flag rejected", func(t *testing.T) {
		_, _, unexpected, ok := SplitArgs(nil, []string{"--bogus"})
		if ok {
			t.Errorf("ok=true, want false")
		}
		if unexpected != "--bogus" {
			t.Errorf("unexpected=%q, want --bogus", unexpected)
		}
	})
	t.Run("empty known flags accepts only positionals", func(t *testing.T) {
		present, positional, _, ok := SplitArgs(nil, []string{"a.json", "b.json"})
		if !ok {
			t.Fatalf("ok=false")
		}
		if len(present) != 0 {
			t.Errorf("present=%v, want empty", present)
		}
		if len(positional) != 2 {
			t.Errorf("positional=%v", positional)
		}
	})
	t.Run("flag never consumes following token", func(t *testing.T) {
		_, positional, _, ok := SplitArgs([]string{"--dry-run"}, []string{"--dry-run", "--bogus"})
		// --bogus is unknown even though it follows --dry-run: value-less flags.
		if ok {
			t.Errorf("ok=true, want false (unknown flag --bogus)")
		}
		_ = positional
	})
}

func writeSchemaFile(t *testing.T, path string) {
	t.Helper()
	schema := `{"tables":[{"name":"entries","columns":[{"name":"id","type":{"family":"integer"}}]}]}`
	if err := os.WriteFile(path, []byte(schema), 0o644); err != nil {
		t.Fatalf("write schema: %v", err)
	}
}

// inlineSchema returns a minimal valid Schema with one table, for the inline
// path of ResolveSchema.
func inlineSchema() compat.Schema {
	return compat.Schema{
		Tables: []compat.Table{{
			Name: "entries",
			Columns: []compat.Column{{
				Name: "id",
				Type: compat.Type{Family: compat.IntegerType},
			}},
		}},
	}
}

func TestDecodeFileStrict(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cfg.json")

	t.Run("happy", func(t *testing.T) {
		if err := os.WriteFile(path, []byte(`{"tables":[]}`), 0o644); err != nil {
			t.Fatal(err)
		}
		var s compat.Schema
		if err := DecodeFileStrict(path, &s); err != nil {
			t.Fatalf("DecodeFileStrict: %v", err)
		}
		if len(s.Tables) != 0 {
			t.Errorf("tables=%v, want empty", s.Tables)
		}
	})
	t.Run("unknown field rejected", func(t *testing.T) {
		if err := os.WriteFile(path, []byte(`{"tables":[],"bogus":1}`), 0o644); err != nil {
			t.Fatal(err)
		}
		var s compat.Schema
		if err := DecodeFileStrict(path, &s); err == nil {
			t.Errorf("DecodeFileStrict unknown field = nil, want error")
		}
	})
	t.Run("missing file", func(t *testing.T) {
		var s compat.Schema
		if err := DecodeFileStrict(filepath.Join(dir, "nope.json"), &s); err == nil {
			t.Errorf("missing file = nil, want error")
		}
	})
}

func TestResolveSchema(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(configPath, []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Run("inline only", func(t *testing.T) {
		got, err := ResolveSchema(configPath, "", inlineSchema())
		if err != nil {
			t.Fatalf("ResolveSchema: %v", err)
		}
		if len(got.Tables) != 1 || got.Tables[0].Name != "entries" {
			t.Errorf("got %+v", got)
		}
	})
	t.Run("ref only, relative path resolved beside config", func(t *testing.T) {
		writeSchemaFile(t, filepath.Join(dir, "schema.json"))
		got, err := ResolveSchema(configPath, "schema.json", compat.Schema{})
		if err != nil {
			t.Fatalf("ResolveSchema: %v", err)
		}
		if len(got.Tables) != 1 || got.Tables[0].Name != "entries" {
			t.Errorf("got %+v", got)
		}
	})
	t.Run("both is an error", func(t *testing.T) {
		_, err := ResolveSchema(configPath, "schema.json", inlineSchema())
		if err == nil {
			t.Fatalf("both = nil, want error")
		}
		if !strings.Contains(err.Error(), "not both") {
			t.Errorf("err=%q, want mention of not both", err)
		}
	})
	t.Run("neither is an error", func(t *testing.T) {
		_, err := ResolveSchema(configPath, "", compat.Schema{})
		if err == nil {
			t.Fatalf("neither = nil, want error")
		}
		if !strings.Contains(err.Error(), "exactly one") {
			t.Errorf("err=%q, want mention of exactly one", err)
		}
	})
	t.Run("unreadable ref is an error", func(t *testing.T) {
		_, err := ResolveSchema(configPath, "missing.json", compat.Schema{})
		if err == nil {
			t.Fatalf("unreadable ref = nil, want error")
		}
		if !strings.Contains(err.Error(), "schema_ref") {
			t.Errorf("err=%q, want schema_ref prefix", err)
		}
	})
}

func TestReplicationCode(t *testing.T) {
	if got := ReplicationCode(&compat.ConflictError{Table: "entries"}); got != ErrReplicationConflict {
		t.Errorf("ConflictError -> %s, want %s", got, ErrReplicationConflict)
	}
	if got := ReplicationCode(errors.New("some other failure")); got != ErrInternal {
		t.Errorf("other -> %s, want %s", got, ErrInternal)
	}
}

func TestParseArgsStrict_Happy(t *testing.T) {
	present, positional := ParseArgsStrict([]string{"--dry-run"}, []string{"--dry-run", "cutover.json"}, 1,
		"hint", "unexpected flag %q", "needs one arg")
	if !present["--dry-run"] {
		t.Errorf("present[--dry-run]=false")
	}
	if len(positional) != 1 || positional[0] != "cutover.json" {
		t.Errorf("positional=%v", positional)
	}

	// No flags, single positional.
	_, positional = ParseArgsStrict(nil, []string{"contract.json"}, 1, "h", "uf %q", "cm")
	if len(positional) != 1 || positional[0] != "contract.json" {
		t.Errorf("positional=%v", positional)
	}
}

// runSubprocess re-executes the test binary so a helper that calls os.Exit can be
// observed in a child process without killing the test runner. The child runs the
// named test, sees marker and runs fn instead. It returns the child's exit code,
// stdout and stderr.
func runSubprocess(t *testing.T, marker string, fn func()) (int, string, string) {
	t.Helper()
	if os.Getenv("CLIOUT_SUBPROCESS") == marker {
		fn()
		return 0, "", ""
	}
	cmd := exec.Command(os.Args[0], "-test.run=^"+t.Name()+"$", "-test.v=false")
	cmd.Env = append(os.Environ(), "CLIOUT_SUBPROCESS="+marker)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	code := 0
	if runErr != nil {
		if ee, ok := runErr.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else {
			t.Fatalf("cmd.Run: %v", runErr)
		}
	}
	return code, stdout.String(), stderr.String()
}

// envelopeFrom parses the first JSON line of child stdout into the typed
// errorEnvelope so assertions compare decoded field values, not raw escaped
// JSON text.
func envelopeFrom(t *testing.T, stdout string) errorEnvelope {
	t.Helper()
	var env errorEnvelope
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &env); err != nil {
		t.Fatalf("child stdout not a JSON envelope: %v\nraw=%q", err, stdout)
	}
	return env
}

func TestParseArgsStrict_UnexpectedFlagExits2(t *testing.T) {
	code, stdout, stderr := runSubprocess(t, "unexpected", func() {
		ParseArgsStrict(nil, []string{"--bogus"}, 1, "uso: prog <cfg>", "prog: unexpected flag %q", "prog needs one arg")
	})
	if code != 2 {
		t.Errorf("exit code = %d, want 2\nstdout=%q stderr=%q", code, stdout, stderr)
	}
	env := envelopeFrom(t, stdout)
	if env.Status != "error" || env.Code != ErrUsage || env.Message != `prog: unexpected flag "--bogus"` {
		t.Errorf("envelope = %+v", env)
	}
	if !strings.Contains(stderr, "uso: prog <cfg>") {
		t.Errorf("stderr missing usage hint: %q", stderr)
	}
}

func TestParseArgsStrict_WrongCountExits2(t *testing.T) {
	code, stdout, stderr := runSubprocess(t, "count", func() {
		ParseArgsStrict(nil, []string{}, 1, "uso: prog <cfg>", "prog: unexpected flag %q", "prog needs one arg")
	})
	if code != 2 {
		t.Errorf("exit code = %d, want 2", code)
	}
	env := envelopeFrom(t, stdout)
	if env.Code != ErrUsage || env.Message != "prog needs one arg" {
		t.Errorf("envelope = %+v", env)
	}
	if !strings.Contains(stderr, "uso: prog <cfg>") {
		t.Errorf("stderr missing usage hint for count error: %q", stderr)
	}
}

func TestDie_Exits1(t *testing.T) {
	code, stdout, stderr := runSubprocess(t, "die", func() {
		Die(ErrConfig, errors.New("boom"))
	})
	if code != 1 {
		t.Errorf("exit code = %d, want 1\nstdout=%q stderr=%q", code, stdout, stderr)
	}
	env := envelopeFrom(t, stdout)
	if env.Status != "error" || env.Code != ErrConfig || env.Message != "boom" {
		t.Errorf("envelope = %+v", env)
	}
	if !strings.Contains(stderr, "boom") {
		t.Errorf("stderr missing boom: %q", stderr)
	}
}

func TestDie_UsageExits2(t *testing.T) {
	code, stdout, _ := runSubprocess(t, "die-usage", func() {
		Die(ErrUsage, fmt.Errorf("unexpected flag %q", "--x"))
	})
	if code != 2 {
		t.Errorf("exit exit = %d, want 2", code)
	}
	env := envelopeFrom(t, stdout)
	if env.Code != ErrUsage || env.Message != `unexpected flag "--x"` {
		t.Errorf("envelope = %+v", env)
	}
}
