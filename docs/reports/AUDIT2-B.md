# AUDIT2-B — compat/capture.go, journal.go, replicate.go(store.go), verify.go, inspect.go, spec.go, schema.go, features.go, runtime.go

Scope: read-only audit of the listed files only. Baseline `go build ./...`, `go vet ./...` green before and after (no files modified; a temporary `compat/zz_audit_tmp2_test.go` was created to demonstrate findings and deleted afterward — confirmed via `git status --short compat/`, which shows no changes from this session).

## Method note

All findings below were reproduced with real `go test` runs against SQLite `:memory:` databases (package `compat`, calling private functions directly). Output is pasted per finding. Anything requiring a live PostgreSQL server (GUC transaction-locality, `atttypmod` subquery, `prokind` filter, PG timestamp CAST text formats) was **not** executed — no Postgres instance is available in this environment — and is marked **NO VERIFICADA (por lectura)**.

---

## Hallazgo 1 — ALTA — Silent precision loss replicating high-precision FLOAT columns (compat/capture.go:132-152, compat/store.go:363-369)

**What happens:** `captureJSONExpression` journals every non-binary column via `CAST(col AS TEXT)`. On SQLite, `CAST(REAL AS TEXT)` renders with SQLite's own ~15-significant-digit formatter, which is *not* a faithful round-trip of the stored float64 bit pattern (unlike Go's `strconv.FormatFloat(-1)`, used by `stringify`/`normalizeFloat` on the live-row path). `normalizeFloat` parses whatever text it is given and re-emits a canonical form, but it cannot recover digits that SQLite's `CAST(...AS TEXT)` already dropped — it just makes the *already-rounded* value look consistent afterwards.

Concretely: source inserts `123456789012345.6789` (float64 stores as `1.2345678901234567e+14`). Capture journals `CAST(flt AS TEXT)` = `"123456789012346.0"` (SQLite's own rounding — 15 sig figs, and note it even rounds the visible digit up). `ApplyChanges` inserts that *already-rounded* text into the destination. The destination ends up permanently storing `1.23456789012346e+14`, a different float64 than the source ever held — **with no error, no `ConflictError`, no warning**. The corruption is only detectable later via an explicit `VerifySnapshots`/`SnapshotDigest` comparison of source vs. destination (which *would* catch it, since export reads raw driver values on both sides, not through `CAST(...AS TEXT)`).

**Evidence** (`go test ./compat/ -run TestZZAuditFloatSilentPrecisionLossViaCaptureTextCast -v`):
```
source-stored (as inserted, pre-capture-cast) = 1.2345678901234567e+14
destination-stored (post replication)          = 1.23456789012346e+14
--- FAIL: TestZZAuditFloatSilentPrecisionLossViaCaptureTextCast (0.00s)
    SILENT DATA LOSS: replicated float value 1.23456789012346e+14 does not equal the source's own stored value 1.2345678901234567e+14 -- no error/conflict was raised
```

**Root cause:** `captureJSONExpression` (capture.go) uses the generic `CAST(value AS TEXT)` path for `FloatType` instead of a lossless textual encoding (e.g. `printf('%.17g', col)` on SQLite, or hex-encoding the IEEE-754 bits like `BinaryType` already does). This is a capture-vs-canonicalValue inconsistency for the `FloatType` family specifically — the two producers of "float text" (capture's SQL-side `CAST`, and Go's `stringify`/`normalizeFloat` used for the live row) are not equivalent, and `normalizeFloat`'s job (introduced as part of this scope's recent float-canonicalization work) only reconciles *formatting*, not *lost precision*.

**Scope note:** this is real-world-relevant only for float values whose decimal representation needs >15 significant digits (very large magnitudes or very high-precision fractions). Ordinary application floats (currency-like, typical measurements) are unaffected. Not evaluated against Postgres capture (`CAST(double precision AS TEXT)` in PG typically preserves 17 digits — likely does **not** share this bug, but NOT VERIFICADA, no live Postgres available).

---

## Hallazgo 2 — ALTA — DecimalType has no float-style canonicalization at all → spurious `ConflictError` for high-precision/high-magnitude DECIMAL columns (compat/store.go:280-281 `canonicalValue`, case `DecimalType`)

**What happens:** Unlike `FloatType` (which calls `normalizeFloat`), the `DecimalType` branch of `canonicalValue` does nothing but pass the raw text through (`return Value{Kind: DecimalValue, Value: text}, nil`). For ordinary small decimals SQLite happens to render `CAST(...AS TEXT)` and Go's `fmt.Sprint(float64)` identically (both tests below with `1.50`/`1.5` passed), so this is invisible in common cases. But once a DECIMAL column holds a value whose float64 storage needs >15 significant digits, the two producers diverge as raw, un-normalized strings — one is SQLite's rounded/rendered text (from capture's `CAST(...AS TEXT)`), the other is Go's full round-trip `fmt.Sprint`/exponential notation (from the live-row scan in `loadRow`/`exportTable`). These are never reconciled, so a subsequent UPDATE/DELETE on such a row raises a **`ConflictError`** even when nothing is actually wrong — a full, if noisy, replication-halting false positive (safer than Hallazgo 1's silent loss, but still a correctness bug in the same code family, and a bigger blast radius since DECIMAL is a common column type for money/quantities that may exceed 15 significant digits for large aggregates or high scale).

**Evidence** (`go test ./compat/ -run TestZZAuditDecimalHighPrecisionDivergence -v`):
```
case 0 input="123456789012345.6789" castText="123456789012346.0" raw=1.2345678901234567e+14(float64)
    captureCanonical={Kind:decimal Value:123456789012346.0} rawCanonical={Kind:decimal Value:1.2345678901234567e+14}
    --- MISMATCH
case 2 input="99999999999999.99" castText="100000000000000.0" raw=9.999999999999998e+13(float64)
    captureCanonical={Kind:decimal Value:100000000000000.0} rawCanonical={Kind:decimal Value:9.999999999999998e+13}
    --- MISMATCH
case 1 ("0.100000000000000001") and case 3 (19-digit integer, stored as SQLite INTEGER not REAL) matched fine.
```
Also confirmed that simply reusing `normalizeFloat` (the fix already applied to `FloatType`) would **not** resolve this — it converges formatting, not the underlying lost precision:
```
normalizeFloat("123456789012346.0")     = "1.23456789012346e+14"
normalizeFloat("1.2345678901234567e+14") = "1.2345678901234567e+14"
-- still different
```

**Root cause:** same as Hallazgo 1 — capture's generic `CAST(col AS TEXT)` is lossy for high-magnitude REAL-affinity storage on SQLite, and `DecimalType` additionally lacks any reconciliation step, so even values that *don't* hit the CAST precision loss but merely format differently (SQLite text vs. Go text) would already diverge before considering precision loss.

---

## Hallazgo 3 — MEDIA — `ApplyChangesTolerant`'s "already applied" proof is pure final-state equality; it cannot distinguish legitimate snapshot-overlap from an unrelated destination write that happens to coincide (compat/store.go:186-231, documented trade-off, confirmed behaviorally)

This is the exact mechanism the task asked to interrogate ("¿puede marcar como aplicado algo que NO debería?"). Confirmed: yes, by design. If the destination row's current *full* state coincidentally equals `change.After` (Insert) or equals `change.After` (Update) for *any* reason — not necessarily because the snapshot carried this exact change — tolerant mode treats it as already-applied and records it as such in `__compat_applied_changes`, permanently skipping re-validation of that sequence.

**Evidence** (`go test ./compat/ -run TestZZAuditTolerantInsertMasksGenuineDivergence -v`): a destination row written by an unrelated process (not from this source's snapshot at all) with the same primary key and coincidentally-identical column values was silently accepted and marked applied, with no error raised.

This is already called out in the existing docstring (`store.go:38-56`) as an intentional trade-off ("the tolerant mode is a catch-up convenience, not a bypass... Any other divergence remains a strict ConflictError"), and is only exploitable when the *entire* row (all columns) coincidentally matches, which is unlikely for wide tables but easy for narrow ones (e.g. a single-boolean-column table, or a status-enum table with few distinct states). Flagging as MEDIA rather than ALTA because it is a known, documented, and inherent limitation of "final-state equality as proof," not an implementation defect — but it is worth the maintainers' explicit sign-off since it is easy to construct in a two-state (boolean/enum) table.

No interaction bug found with the dedup table itself: `markChangeApplied` after a tolerant skip is intentional (matches the docstring), and reapplication after a skip correctly short-circuits via `changeWasApplied` without re-entering `applyChange` (`TestZZAuditTolerantDeleteAlreadyGone`, both passes green). Multi-step chains (skip-insert → real update → real delete on the same PK within one tolerant call) were also exercised end-to-end and behaved correctly (`TestZZAuditTolerantMultiStepChain`, green) — a skipped insert does not corrupt subsequent Before/After validation because every step still reads live `tx` state via `loadRow`, never the skipped change's cached values.

---

## Hallazgos BAJA

- **BAJA** — compat/inspect.go: Postgres-specific SQL correctness (correlated `atttypmod` subquery scoped by `table_schema`/`relname`, the `prokind IN ('f','p')` filter excluding aggregates/window functions, `pg_get_triggerdef`/`pg_get_functiondef` calls) is architecturally sound by inspection but **NO VERIFICADA** — no live PostgreSQL instance available in this environment to execute it.
- **BAJA** — compat/replicate.go / store.go: transaction-local GUC suppression (`compat.suppress`) correctness under concurrent Postgres connections is **NO VERIFICADA** — this needs a live multi-connection Postgres test (the repo's own comments point to `e2e/suppress_test.go` for that, out of this audit's scope/files).
- **BAJA** — compat/journal.go `decodeCapturedRow`: silently `continue`s (skips) a column whose key is absent from the captured JSON payload, rather than erroring. In practice `captureJSONExpression` always emits every table column, so this is unreachable in the current code path, but it is a silent-skip pattern worth a defensive comment or an explicit error if a future schema-drift scenario (capturing with a stale trigger before a column was added) is ever possible.
- **BAJA** — the float/decimal precision-loss root cause (CAST(...AS TEXT) on SQLite) could in principle also affect `VectorType` components (which are floats, canonicalized the same way via `normalizeFloat` inside `canonicalVectorValue`), but F32 vector components have far fewer significant decimal digits than a float64 REAL column, making this practically very unlikely to trigger; not independently reproduced, flagging as a note only.

## Cobertura

Existing test suite for these files is solid for the "documented" tolerant-mode behaviors (insert-skip, update-skip, genuine-conflict, dedup-still-idempotent) and for the float `"1.0"` vs `"1"` normalization case that motivated `normalizeFloat`. Gaps found and closed only for the duration of this audit (temp file, now deleted): tolerant-delete-already-gone (uncovered before this audit), a full skip→update→delete chain, and — the main new findings — high-magnitude float/decimal precision behavior, which had no coverage at all in either `store_test.go` or `replicate_test.go` (only the exact `1.0`/`1` case is tested).

## Trade-offs

Hallazgos 1 and 2 share one root fix opportunity: replace the generic `CAST(col AS TEXT)` in `captureJSONExpression` for `FloatType`/`DecimalType` with a lossless encoding on SQLite (e.g. `printf('%.17g', col)`, or reuse the existing hex-blob pattern already used for `BinaryType`) so the capture-journal text and the live-driver text always describe the *same* float64 exactly. Hallazgo 3 is a documented, inherent trade-off of "final-state equality" reconciliation and likely needs a product decision (e.g. requiring a minimum set of "high cardinality" columns, or accepting the risk as-is for the target use case) rather than a pure code fix.

---

**Conteo por severidad:** ALTA: 2, MEDIA: 1, BAJA: 3

**Hallazgos ALTA (1 línea c/u):**
1. `captureJSONExpression`/`canonicalValue` (capture.go, store.go): SQLite's `CAST(REAL AS TEXT)` silently loses precision on high-magnitude FLOAT values, permanently corrupting replicated data with no error raised.
2. `canonicalValue` DecimalType (store.go:280-281): missing `normalizeFloat`-style canonicalization causes spurious `ConflictError` (and would mask the same precision loss as #1) for high-precision/high-magnitude DECIMAL columns.
