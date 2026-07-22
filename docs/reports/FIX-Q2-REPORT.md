# FIX-Q2 — Close AUDIT2-B Hallazgos 1 & 2 (silent float precision loss + spurious DECIMAL ConflictError)

Scope: `compat/capture.go`, `compat/store.go`, and their tests (`compat/capture_test.go`,
`compat/store_test.go`, `compat/replicate_test.go`). No file outside the permitted perimeter
was modified. Baseline `go build ./... && go vet ./... && go test ./...` was green before and
is green after.

## Root cause (shared)

`captureJSONExpression` journaled every non-binary column with `CAST(col AS TEXT)`. On
SQLite, `CAST(REAL AS TEXT)` renders with the engine's own ~15-significant-digit formatter,
which is **not** a faithful round-trip of the stored float64 bit pattern. Two bugs followed:

- **Hallazgo 1 (FLOAT, silent loss):** the journal stored an already-rounded float, the
  destination stored that rounded value permanently, and no error/conflict was raised.
  `canonicalValue`'s `normalizeFloat` reconciles *formatting*, not *lost precision* — once
  SQLite's CAST dropped the digits, they were gone.
- **Hallazgo 2 (DECIMAL, spurious conflict):** a DECIMAL column on a native NUMERIC-affinity
  table stores fractional values as REAL, so the journal's CAST truncated while the
  destination driver reconstructed the full float64. `rowsEqual` then compared
  `"123456789012346.0"` against `"1.2345678901234567e+14"` and raised a replication-halting
  `ConflictError`. The `DecimalType` branch of `canonicalValue` had no reconciliation step at
  all (it passed raw text through), so even pure formatting divergence would trip it.

## Fix

### 1. Lossless capture for float64 storage (`compat/capture.go`)

`captureJSONExpression` now chooses the SQL encoding per type family instead of a blanket
`CAST(col AS TEXT)`:

- **`FloatType` (SQLite):** `printf('%!.17g', col)`. 17 significant digits is the IEEE-754
  double round-trip bound: any float64 has a ≤17-significant-digit decimal that parses back
  to the identical bit pattern, so the journal now carries a faithful float64
  representation. `canonicalValue`'s existing `normalizeFloat` then maps both the capture text
  and the destination driver's float64 to the same shortest form, so capture and destination
  converge with no precision loss.
- **`DecimalType` (SQLite):** `CASE typeof(col) WHEN 'real' THEN printf('%!.17g', col) ELSE
  CAST(col AS TEXT) END`. The REAL-storage case (the AUDIT2-B scenario — a native
  NUMERIC-affinity table) gets the faithful round-trip form; TEXT- and INTEGER-stored
  decimals (our own DDL maps `DecimalType` → `TEXT`, and arbitrary-precision user data) pass
  through `CAST` verbatim, preserving every digit.
- **Postgres branches:** unchanged (plain `CAST`). PostgreSQL 12+ uses the Ryu shortest
  round-trip formatter for `float8` text output by default (`extra_float_digits` defaults to
  `1`), so `CAST(double precision AS TEXT)` round-trips; `NUMERIC` preserves arbitrary
  precision in `CAST`. The repo targets PostgreSQL 17, so this holds. (Verified by reading
  PostgreSQL's `float8out`/`d2s.c` and the `extra_float_digits` documentation — no live
  Postgres was available in this environment. Caveat: a pre-12 target would need
  `extra_float_digits=3`; out of scope here.)

### 2. Conditional float reconciliation for DECIMAL (`compat/store.go`)

The `DecimalType` branch of `canonicalValue` no longer passes raw text through unconditionally.
A DECIMAL value is reconciled through `normalizeFloat` **only when it is a genuine float64
storage representation**, and is otherwise preserved verbatim. The exact criterion:

- A value arriving as a Go `float64`/`float32` (the shape a REAL column takes when scanned back
  through `database/sql`) is normalized to the shortest form.
- A value arriving as **text** is normalized iff `isCompactFloatText(text)` is true: it looks
  like a float (contains `.`, `e`, or `E` — this excludes pure-integer text), parses as a
  finite `float64`, and carries **at most 17 significant digits** (the IEEE-754 double
  round-trip bound, which is exactly what `printf('%!.17g')` emits).
- Everything else is returned verbatim. Decimals with **18+ significant digits are
  arbitrary-precision text** (e.g. a 38-digit value) that never round-trips through a single
  float64; they fail the ≤17-significant-digit test and are preserved byte-for-byte.
  Pure-integer decimals are preserved exactly (not rewritten in exponential notation).

This makes the two producers of "decimal float text" — the capture trigger's
`printf('%!.17g')` and the destination driver's `float64` — converge on the same Go shortest
form, so `rowsEqual` no longer raises a spurious `ConflictError`, while arbitrary-precision
TEXT storage is untouched.

### Why the ≤17-significant-digit gate is the right separator

17 significant digits is simultaneously (a) the bound that guarantees a float64 round-trips,
and (b) a clean separator from arbitrary-precision decimals, which by definition carry *more*
precision than a float64 can hold (≥18 significant digits). The capture emission
(`printf('%!.17g')`) is bounded at 17 significant digits, so it always falls on the
float side of the gate; arbitrary-precision data always falls on the preserve side. The two
classes do not overlap at this boundary.

### modernc printf caveat (documented, not a regression)

modernc.org/sqlite's `printf('%g')` is **not** a faithful C-printf: plain `printf('%.17g', x)`
caps at ~15–16 significant digits and does **not** round-trip. The `!` alternate-form-2 flag
(`%!.17g`) is required to get the true 17-significant-digit form. A broad deterministic sweep
(400k+ values) found `printf('%!.17g')` round-trips for all realistic magnitudes; it fails for
~100 extreme power-of-ten magnitudes (e.g. `1e-306`, `1e-290`) at *every* precision tested
(17/18/19/20/25/30) — a modernc limitation, not a precision-choice issue. These extreme
magnitudes are not realistic for FLOAT/DECIMAL application data, and the **old `CAST` path was
lossy for them too**, so this is not a regression. For the AUDIT2-B repro value
(`1.2345678901234567e+14`, magnitude e+14) and all existing test values, `%!.17g` is lossless.
Hex-encoding the IEEE-754 bits (the audit's alternative suggestion, analogous to
`BinaryType`) was rejected because modernc lacks a float-to-hex format (`printf('%a', ...)`
returns NULL in modernc), so there is no SQL-side lossless encoding available beyond
`printf('%!.17g')`.

## Regression tests added

`compat/replicate_test.go`:
- `TestApplyChangesPreservesHighPrecisionFloat` — BUG 1: replicates a FLOAT column with value
  `1.2345678901234567e+14` (insert + update); asserts the destination canonical value equals
  the source canonical value byte-for-byte (`1.2345678901234567e+14`, no loss), no conflict,
  and idempotent replay.
- `TestApplyChangesDecimalRealStorageNoSpuriousConflict` — BUG 2: a native NUMERIC-affinity
  table (fractional DECIMAL stored as REAL) replicated with capture across insert + update;
  asserts no spurious `ConflictError` and idempotent replay.
- `TestApplyChangesPreservesArbitraryPrecisionDecimal` — non-regression: a 38-significant-digit
  DECIMAL stored as TEXT arrives at the destination byte-identical.

`compat/store_test.go`:
- `TestCanonicalDecimalReconcilesFloatStorage` — unit guard for `canonicalValue` DECIMAL: a
  driver `float64` and the `printf('%!.17g')` capture text converge; a 38-digit
  arbitrary-precision value is preserved verbatim; a high-magnitude rounded text and its driver
  float64 converge; a pure-integer decimal is preserved.

`compat/capture_test.go`:
- `TestCompileCaptureTriggersUsesRoundTripFloatEncodingOnSQLite` — guards the compiled trigger
  SQL: SQLite uses `printf('%!.17g')` for FLOAT and the `typeof`-gated form for DECIMAL;
  Postgres keeps plain `CAST` and never uses `printf`.

Pre-existing tests were not modified or deleted.

## Definition of done — status

1. **BUG 1 regression test:** `TestApplyChangesPreservesHighPrecisionFloat` — green. The
   destination canonical value is exactly `1.2345678901234567e+14`, matching the source, with
   no conflict.
2. **BUG 2 regression test:** `TestApplyChangesDecimalRealStorageNoSpuriousConflict` — green,
   no spurious `ConflictError`; and `TestApplyChangesPreservesArbitraryPrecisionDecimal` —
   green, the 38-digit TEXT decimal arrives byte-identical.
3. `go build ./... && go vet ./... && go test ./...` — all green. Pre-existing tests untouched.
4. **Already-installed capture triggers (upgrade route):** `InstallChangeCapture` re-runs
   `compileCaptureTriggers`, which emits `DROP TRIGGER IF EXISTS <name>` followed by
   `CREATE TRIGGER <name> ...` on SQLite (and `DROP TRIGGER IF EXISTS ... ON <table>` +
   `CREATE OR REPLACE FUNCTION` + `CREATE TRIGGER` on Postgres). Re-executing
   `InstallChangeCapture` therefore **replaces** the previously installed triggers in place —
   that is the upgrade route: existing deployments pick up the new `printf('%!.17g')`
   encoding the next time `InstallChangeCapture` runs. The `__compat_change_journal` rows
   written by the *old* triggers before the upgrade remain readable; only rows captured
   *after* the upgrade use the new encoding, which is the desired cutover boundary.