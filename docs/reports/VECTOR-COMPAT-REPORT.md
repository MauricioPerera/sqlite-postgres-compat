# Vector Compatibility Matrix — libSQL (sqld) ↔ pgvector via the `compat` layer

Date: 2026-07-22
Module under test: `experiments/vector/` (module `example.com/vector-exp`, `replace` → root `example.com/sqlite-postgres-compat`).
Infra (remote, ~100–300ms RTT):

- libSQL / sqld with native vectors: `http://<test-host>:8081` (`VECTOR_LIBSQL_URL`).
- PostgreSQL 17 + pgvector 0.8.5: `postgres://postgres:***@<test-host>:5434/postgres?sslmode=disable` (`VECTOR_PG_DSN`).

The `compat` layer has **no `vector` type family** (see `compat/schema.go` `TypeFamily` constants). This experiment characterizes what does and does not cross the layer, with executed evidence for every cell. Tables use the `vexp_` prefix and are cleaned up best-effort at the end of each test.

## Summary matrix

Rows = capability. Columns = verdict.

| # | Capability | libSQL side | pgvector side | Verdict | Evidence |
|---|-------------|-------------|---------------|---------|----------|
| 1 | Native vector type | `F32_BLOB(3)` + `vector32('[...]')` | `vector(3)` | **FUNCIONA** (direct, outside `compat`) | §A.1, §A.2 |
| 2 | Schema inspection of a vector column | — | — | **NO SOPORTADO** (misread as `binary`, no error) | §B |
| 3 | Snapshot, binary route (`F32_BLOB` bytes → `bytea`) | export OK | import OK, bytes identical | **FUNCIONA** (as raw bytes; not a vector) | §C |
| 4 | `bytea` castable to `vector` in PG | — | — | **NO SOPORTADO** | §C (literal error) |
| 5 | Snapshot / replication, text route (`'[1,2,3]'` text → text) | export + incremental capture OK | import / `ApplyChanges` OK, text usable | **FUNCIONA** (text is the interoperable carrier) | §D |
| 6 | ANN index on a vector column / expression | `libsql_vector_idx` + `vector_top_k` (direct) | expression index `((v::vector))` fails; `((v::vector(3)))` works | **FUNCIONA CON PASO MANUAL** | §A.1, §D |
| 7 | Distance / vector functions via `compat` query translation | — | — | **NO SOPORTADO** (catalog parser rejects `vector_distance_cos`) | §E |

Legend: **FUNCIONA** = end-to-end through `compat` or natively as documented. **FUNCIONA CON PASO MANUAL** = works only with an explicit, non-`compat` step (e.g. dimension cast, native ANN index). **NO SOPORTADO** = the `compat` layer cannot represent or translate it; demonstrated by an asserted error, not skipped.

The interoperable path is **text**: store the vector as canonical text `'[1,2,3]'`, replicate it through `compat` (snapshot or incremental capture), and cast to `vector` on the PG side. The native `F32_BLOB`/`bytea` binary path preserves bytes but is not usable as a pgvector vector.

## A. Sanity per engine (direct, no `compat`)

### A.1 libSQL — `TestSanityDirectLibSQL`

Creates `vexp_san_l(id INTEGER PRIMARY KEY, v F32_BLOB(3))`, a `libsql_vector_idx` index, two rows, computes `vector_distance_cos` and an ANN `vector_top_k` retrieval.

```
=== RUN   TestSanityDirectLibSQL
    vector_test.go:185: libsql vector_distance_cos([4,5,6],[1,2,3]) = 0.025368154048919678
    vector_test.go:195: libsql vector_top_k(...,1) -> id=1 (nearest to [1,2,3])
--- PASS: TestSanityDirectLibSQL (0.90s)
```

Index DDL that worked (note: `USING libsql_vector_idx(col)` is rejected by sqld; the working form wraps the function in the column list):

```
CREATE INDEX vexp_san_l_idx ON vexp_san_l(libsql_vector_idx(v))
```

### A.2 pgvector — `TestSanityDirectPgvector`

```
=== RUN   TestSanityDirectPgvector
    vector_test.go:216: pgvector [4,5,6] <=> [1,2,3] = 0.025368153802923787
    vector_test.go:220: pgvector DSN: postgres://postgres:***@<test-host>:5434/postgres?sslmode=disable
--- PASS: TestSanityDirectPgvector (1.01s)
```

Both engines agree on the cosine distance (~0.02536815) for the same pair, confirming the reference values used downstream.

## B. Inspection of a native `F32_BLOB` column — `TestInspectF32BlobMapsToBinaryFamily`

A table `vexp_inspect(id INTEGER PRIMARY KEY, v F32_BLOB(3))` is created on sqld, then `compat.Store.InspectSchema` is run with a hand-built store (`Store{Target: Target{Engine: SQLite, Version: {1,0,0}}, DB: <libsql conn>}`) exactly as required. `pragma_table_xinfo` returns the declared type `F32_BLOB(3)`; `sqliteTypeFamily` matches the substring `"BLOB"` and maps it to `BinaryType`. There is **no error** and **no `vector` family** — the column is silently misread as binary.

```
=== RUN   TestInspectF32BlobMapsToBinaryFamily
    vector_test.go:248: InspectSchema Exact=false unresolved=1
    vector_test.go:250:   unresolved: kind=index name=vt_idx reason=expression, collation or predicate is outside the canonical index grammar
    vector_test.go:268: inspected family of vexp_inspect.v (declared F32_BLOB(3)) = "binary"
--- PASS: TestInspectF32BlobMapsToBinaryFamily (1.35s)
```

> The `unresolved` entry `vt_idx` is a pre-existing expression/vector index left in the shared sqld catalog by prior verification work (not created by this test; outside the `vexp_` prefix). It is surfaced by `InspectSchema` because the inspector scans the whole catalog. It is unrelated to the `vexp_inspect.v` column finding, which is the asserted result: **family = `binary`**.

Conclusion: vector columns cannot be inspected as vectors through `compat`; they collapse to `binary`.

## C. Binary route — `TestSnapshotBinaryRoute`

Canonical schema with a `BinaryType` column. libSQL stores `vector32('[1,2,3]')` as a 12-byte `F32_BLOB` (3 × float32 little-endian: `1.0, 2.0, 3.0` = `[0 0 128 63 0 0 0 64 0 0 64 64]`). `ExportSnapshot` reads the `[]byte`, base64-encodes it (`AACAPwAAAEAAAEBA`); `ImportSnapshot` decodes it back into a PG `bytea`. The bytes arrive identical.

```
=== RUN   TestSnapshotBinaryRoute
    vector_test.go:300: libSQL raw F32_BLOB bytes (len=12): [0 0 128 63 0 0 0 64 0 0 64 64]
    vector_test.go:322: exported canonical value for v: kind=binary base64=AACAPwAAAEAAAEBA
    vector_test.go:327: decoded snapshot bytes (len=12): [0 0 128 63 0 0 0 64 0 0 64 64]
    vector_test.go:333: ImportSnapshot to PG bytea OK
    vector_test.go:340: PG bytea bytes (len=12): [0 0 128 63 0 0 0 64 0 0 64 64]
    vector_test.go:349: bytes identical libSQL -> PG bytea: PASS
    vector_test.go:356: bytea::vector error (literal): "ERROR: cannot cast type bytea to vector (SQLSTATE 42846)"
--- PASS: TestSnapshotBinaryRoute (1.70s)
```

`bytea` is **not** castable to pgvector `vector`:

```
ERROR: cannot cast type bytea to vector (SQLSTATE 42846)
```

So the binary route preserves bytes but does **not** produce a usable vector on the PG side.

## D. Text route — `TestTextReplicationRoute`

Canonical schema with a `TextType` column holding `'[1,2,3]'`. The test exercises the **incremental** path: `InstallChangeCapture` on sqld, a post-capture `INSERT`, `ReadCapturedChanges`, then `ApplyChanges` on PG (table pre-created with `ApplySchema`). The sqld-generated capture triggers (with the `__compat_capture_state` suppress subquery, `strftime` timestamp, `json_object` payload) install and fire correctly on sqld.

```
=== RUN   TestTextReplicationRoute
    vector_test.go:401: InstallChangeCapture on sqld: OK (triggers installed)
    vector_test.go:407: capture cursor (max prior sequence) = 2
    vector_test.go:416: captured 1 change(s) from sqld:
    vector_test.go:418:   change[0] seq=3 kind=insert table=vexp_txt after=map[id:{integer 2} v:{text [4,5,6]}]
    vector_test.go:432: ApplyChanges on PG: OK
    vector_test.go:439: PG vexp_txt id=2 v="[4,5,6]"
    vector_test.go:449: PG v::vector <=> '[1,2,3]' = 0.025368153802923787
    vector_test.go:455: expression ANN index ((v::vector)) error (literal): "ERROR: column does not have dimensions (SQLSTATE 22023)"
    vector_test.go:466: expression ANN index ((v::vector(3)) hnsw vector_cosine_ops): CREATED (manual dimension step)
--- PASS: TestTextReplicationRoute (3.32s)
```

Findings:

- Incremental capture + replication through `compat` **works** on sqld→PG (triggers fire, change is journaled, applied, text arrives verbatim).
- The replicated text is usable as a vector in PG: `v::vector <=> '[1,2,3]'::vector` returns the same ~0.02536815 distance.
- An ANN index on the bare expression `((v::vector))` is **rejected** by pgvector (no dimensions). With the manual step of an explicit dimension, `((v::vector(3)))`, an HNSW `vector_cosine_ops` index is **created**. That dimension cast is outside `compat` (the layer carries plain text), so this cell is **FUNCIONA CON PASO MANUAL**.

> Snapshot fallback: the test is written so that if `InstallChangeCapture` ever fails on sqld, it falls back to `ExportSnapshot`/`ImportSnapshot` and records the trigger error literally (no `t.Skip`). In this run the triggers succeeded, so the fallback path was not taken; it remains in `runTextSnapshotFallback` as documented insurance.

## E. Function translation — `TestCatalogParserRejectsVectorDistanceCos`

A view `vexp_fn_v AS SELECT vector_distance_cos(v, vector32('[1,2,3]')) AS d FROM vexp_fn_t` is created on sqld. `InspectSchema` parses view bodies through the bounded catalog grammar (`parseCatalogSelect` → `parseCatalogExpression`). `vector_distance_cos` is not in the function allowlist, so the parser rejects it explicitly:

```
=== RUN   TestCatalogParserRejectsVectorDistanceCos
    vector_test.go:529:   unresolved: kind=index name=vt_idx reason=expression, collation or predicate is outside the canonical index grammar
    vector_test.go:529:   unresolved: kind=view name=vexp_fn_v reason=unsupported catalog function "vector_distance_cos"
    vector_test.go:538: catalog rejection reason (literal): "unsupported catalog function \"vector_distance_cos\""
    vector_test.go:542: CONFIRMED: vector query translation does not exist in the compat layer
--- PASS: TestCatalogParserRejectsVectorDistanceCos (1.65s)
```

Literal reason: `unsupported catalog function "vector_distance_cos"`. This documents that **vector query translation does not exist** in `compat`: any view/routine referencing `vector_distance_cos`, `vector32`, `vector_extract`, etc. is unresolved, never silently translated.

## Full test run (verbatim)

Command: `cd experiments/vector && go test ./... -v -count=1 -timeout 600s`

```
=== RUN   TestSanityDirectLibSQL
    vector_test.go:185: libsql vector_distance_cos([4,5,6],[1,2,3]) = 0.025368154048919678
    vector_test.go:195: libsql vector_top_k(...,1) -> id=1 (nearest to [1,2,3])
--- PASS: TestSanityDirectLibSQL (0.90s)
=== RUN   TestSanityDirectPgvector
    vector_test.go:216: pgvector [4,5,6] <=> [1,2,3] = 0.025368153802923787
    vector_test.go:220: pgvector DSN: postgres://postgres:***@<test-host>:5434/postgres?sslmode=disable
--- PASS: TestSanityDirectPgvector (1.01s)
=== RUN   TestInspectF32BlobMapsToBinaryFamily
    vector_test.go:248: InspectSchema Exact=false unresolved=1
    vector_test.go:250:   unresolved: kind=index name=vt_idx reason=expression, collation or predicate is outside the canonical index grammar
    vector_test.go:268: inspected family of vexp_inspect.v (declared F32_BLOB(3)) = "binary"
--- PASS: TestInspectF32BlobMapsToBinaryFamily (1.35s)
=== RUN   TestSnapshotBinaryRoute
    vector_test.go:300: libSQL raw F32_BLOB bytes (len=12): [0 0 128 63 0 0 0 64 0 0 64 64]
    vector_test.go:322: exported canonical value for v: kind=binary base64=AACAPwAAAEAAAEBA
    vector_test.go:327: decoded snapshot bytes (len=12): [0 0 128 63 0 0 0 64 0 0 64 64]
    vector_test.go:333: ImportSnapshot to PG bytea OK
    vector_test.go:340: PG bytea bytes (len=12): [0 0 128 63 0 0 0 64 0 0 64 64]
    vector_test.go:349: bytes identical libSQL -> PG bytea: PASS
    vector_test.go:356: bytea::vector error (literal): "ERROR: cannot cast type bytea to vector (SQLSTATE 42846)"
--- PASS: TestSnapshotBinaryRoute (1.70s)
=== RUN   TestTextReplicationRoute
    vector_test.go:401: InstallChangeCapture on sqld: OK (triggers installed)
    vector_test.go:407: capture cursor (max prior sequence) = 2
    vector_test.go:416: captured 1 change(s) from sqld:
    vector_test.go:418:   change[0] seq=3 kind=insert table=vexp_txt after=map[id:{integer 2} v:{text [4,5,6]}]
    vector_test.go:432: ApplyChanges on PG: OK
    vector_test.go:439: PG vexp_txt id=2 v="[4,5,6]"
    vector_test.go:449: PG v::vector <=> '[1,2,3]' = 0.025368153802923787
    vector_test.go:455: expression ANN index ((v::vector)) error (literal): "ERROR: column does not have dimensions (SQLSTATE 22023)"
    vector_test.go:466: expression ANN index ((v::vector(3)) hnsw vector_cosine_ops): CREATED (manual dimension step)
--- PASS: TestTextReplicationRoute (3.32s)
=== RUN   TestCatalogParserRejectsVectorDistanceCos
    vector_test.go:529:   unresolved: kind=index name=vt_idx reason=expression, collation or predicate is outside the canonical index grammar
    vector_test.go:529:   unresolved: kind=view name=vexp_fn_v reason=unsupported catalog function "vector_distance_cos"
    vector_test.go:538: catalog rejection reason (literal): "unsupported catalog function \"vector_distance_cos\""
    vector_test.go:542: CONFIRMED: vector query translation does not exist in the compat layer
--- PASS: TestCatalogParserRejectsVectorDistanceCos (1.65s)
PASS
ok  	example.com/vector-exp	12.956s
```

All 6 tests PASS. Tests that document an expected incompatibility (§B misread family, §C bytea-not-castable, §D dimensionless index rejection, §E function rejection) **assert the expected error and pass** — none use `t.Skip`.

## Root module unchanged — build & vet proof

The root module (`example.com/sqlite-postgres-compat`) was not modified. Command run from repo root:

`go build ./... && go vet ./...`

```
===BUILD===
build exit=0
===VET===
vet exit=0
```

## Files touched

- `experiments/vector/vector_test.go` — new test suite (6 tests).
- `experiments/vector/go.mod` / `go.sum` — added `require example.com/sqlite-postgres-compat` (via `replace ../..`) and transitive deps (`pgx/v5`, etc.) through `go mod tidy`.
- `experiments/vector/probe.go` — retained (connectivity probe; serves as the package's non-test file).
- `VECTOR-COMPAT-REPORT.md` — this report.

Nothing under `compat/`, `cmd/`, `e2e/`, `docs/`, or the root `go.mod` was touched.

---

# Post-VectorType validation

Date: 2026-07-22. This section supersedes the pre-VectorType summary above. The `compat` package now has a first-class canonical `vector` family (`Type{Family: VectorType, Arguments: []int{N}}`): SQLite DDL → `TEXT`, Postgres DDL → `vector(N)`, `canonicalValue` normalizes `'[1, 2.0, 3]'` → `"[1,2,3]"`, the SQLite inspector recognizes `F32_BLOB(N)`, the Postgres inspector targets `udt_name='vector'` with dimension via `atttypmod`, and the `canonical_vectors` feature is inferred. The tests below validate all of that end-to-end against the **live** remote engines (same infra, `vexp2_` prefix, best-effort cleanup). No `t.Skip`; every incompatibility is asserted and recorded.

## Revised matrix (what changed verdict)

| # | Capability | libSQL side | pgvector side | Verdict (post) | Pre-VectorType verdict | Evidence |
|---|-------------|-------------|---------------|---------|---------|----------|
| 1 | Native vector type | `F32_BLOB(3)` + `vector32('[...]')` | `vector(3)` | **FUNCIONA** (direct, outside `compat`) — unchanged | FUNCIONA | §A.1, §A.2 |
| 2 | Schema inspection of a vector column | `F32_BLOB(3)` → family `vector`, args `[3]` | `udt_name='vector'`, `atttypmod=3` (confirmed direct) | **FUNCIONA** (libSQL and PG) — **CHANGED from NO SOPORTADO**; PG was temporarily broken by the `atttypmod`-from-`information_schema.columns` regression (W3), now corrected | NO SOPORTADO (misread as binary) | §PT-5, §PT-4 |
| 3 | Snapshot, binary route (`F32_BLOB` bytes → `bytea`) | export OK | import OK, bytes identical | **FUNCIONA** (as raw bytes; not a vector) — unchanged | FUNCIONA | §C |
| 4 | `bytea` castable to `vector` in PG | — | — | **NO SOPORTADO** — unchanged | NO SOPORTADO | §C |
| 5 | Snapshot / replication, canonical-vector route | export OK (canonical `vector` value) | import → **native `vector(3)` column**; `emb <=> '[1,2,3]'::vector` **direct on the column, no manual cast** | **FUNCIONA** (native vector column, direct distance) — **CHANGED: column-side manual cast eliminated** | FUNCIONA (text carrier, needed `v::vector` cast) | §PT-1, §PT-2, §PT-3 |
| 6 | ANN index on a vector column / expression | `libsql_vector_idx` + `vector_top_k` (direct) | `CREATE INDEX ... USING hnsw (emb vector_cosine_ops)` **directly on the native column, no cast** | **FUNCIONA CON PASO MANUAL** (still a manual DDL outside `compat`) — verdict same, **manual step simplified** (no dimension cast) | FUNCIONA CON PASO MANUAL (`(v::vector(3))` expression) | §A.1, §PT-2 |
| 7 | Distance / vector functions via `compat` query translation | — | — | **NO SOPORTADO** (catalog parser rejects `vector_distance_cos`) — unchanged | NO SOPORTADO | §E |

**Cells that improved thanks to the first-class type:** row **2** (inspection: `F32_BLOB(N)`/`vector(N)` now resolve to the canonical `vector` family with dimension `N`, no longer a silent `binary` misread; on PG the `atttypmod` source was corrected in W3 so `InspectSchema` now returns `family=vector, args=[3]` against real PG17) and row **5** (the snapshot now lands as a **native `vector(N)` column** on PG, so the distance query runs **directly on the column** — the manual `v::vector` cast the pre-VectorType text route required is gone). Row **6** keeps its "CON PASO MANUAL" verdict (the `compat` layer still does not emit ANN indexes) but the manual step is now `hnsw (emb ...)` directly on the native column instead of the `(v::vector(3))` expression. Rows 1, 3, 4, 7 are unchanged.

The interoperable path is now the **canonical vector type**: declare `emb vector(3)` in the canonical schema, let `compat` carry the canonical text `"[1,2,3]"` across, and PG materializes a real `vector(3)` column — usable for direct `<=>` distance and direct HNSW indexing with no per-query cast.

## PT-1. APPLY — `TestPostVectorTypeApplySchema`

Canonical schema `(id integer PK, emb vector(3))`. `compat.ApplySchema` against the real PG `Store` emits a real `vector(3)` column (verified via `pg_attribute`: `typname='vector'`, `atttypmod=3`). The same canonical schema applied through a hand-built libSQL `Store` (`Engine: SQLite`) emits a `TEXT` column (verified via `pragma_table_xinfo`).

```
=== RUN   TestPostVectorTypeApplySchema
    postvectortype_test.go:167: PG ApplySchema (canonical vector(3)): OK
    postvectortype_test.go:169: PG vexp2_apply.emb pg_type="vector" atttypmod=3
    postvectortype_test.go:182: libSQL ApplySchema (TEXT carrier for vector): OK
    postvectortype_test.go:188: libSQL vexp2_apply.emb declared type = "TEXT"
--- PASS: TestPostVectorTypeApplySchema (2.02s)
```

PG column is `vector` with `atttypmod=3`; libSQL column is `TEXT`. Confirmed.

## PT-2. SNAPSHOT + PT-6. VERIFY — `TestPostVectorTypeSnapshotAndVerify`

Two rows `[1,2,3]` and `[4,5,6]` inserted into the libSQL `TEXT` column; `ExportSnapshot` canonicalizes them to `{vector [1,2,3]}` / `{vector [4,5,6]}`; `ImportSnapshot` to PG creates a **native `vector(3)`** column and stores real vectors. The distance is then computed **directly on the column** (`emb <=> '[1,2,3]'::vector`, no `emb::vector` cast) — the advance vs the pre-VectorType matrix. An HNSW index is created **directly on `emb`** with no dimension cast. `VerifySnapshots` between the libSQL source snapshot and a PG re-export is `Equivalent=true` (both sides canonicalize to the same `[1,2,3]`/`[4,5,6]` text → identical sha256 digest).

```
=== RUN   TestPostVectorTypeSnapshotAndVerify
    postvectortype_test.go:227: exported 2 rows from libSQL:
    postvectortype_test.go:230:   snap row[0] id={integer 1} emb={vector [1,2,3]}
    postvectortype_test.go:230:   snap row[1] id={integer 2} emb={vector [4,5,6]}
    postvectortype_test.go:241: ImportSnapshot to PG (native vector(3) column): OK
    postvectortype_test.go:244: PG vexp2_snap.emb pg_type="vector" atttypmod=3
    postvectortype_test.go:257: PG emb <=> '[1,2,3]' (direct on native vector column, no cast) = 0.025368153802923787
    postvectortype_test.go:268: native ANN index (emb hnsw vector_cosine_ops): CREATED directly on native vector column, no cast
    postvectortype_test.go:276: re-exported 2 rows from PG:
    postvectortype_test.go:278:   pgSnap row[0] id={integer 1} emb={vector [1,2,3]}
    postvectortype_test.go:278:   pgSnap row[1] id={integer 2} emb={vector [4,5,6]}
    postvectortype_test.go:284: VerifySnapshots source=b98da5dd9bf94b28873ceca9410a0a3d3dfcb222f53b4047153757edc856c3c8 equivalent=true
--- PASS: TestPostVectorTypeSnapshotAndVerify (2.61s)
```

Direct distance `0.025368153802923787` (matches the §A reference); `Equivalent=true` with identical digests.

## PT-3. INCREMENTAL REPLICATION — `TestPostVectorTypeIncrementalReplication`

`InstallChangeCapture` on libSQL (vector schema → `TEXT` column), a post-capture `INSERT` of `[4,5,6]`, `ReadCapturedChanges` → `ApplyChanges` to the real PG. The captured change carries `emb={vector [4,5,6]}` (canonicalized with the declared dimension 3). On PG the row lands in the native `vector(3)` column; the distance query runs directly on the column. No dimension-mismatch error from the in-flight `capture.go`/`replicate.go` work appeared in this run.

```
=== RUN   TestPostVectorTypeIncrementalReplication
    postvectortype_test.go:319: InstallChangeCapture on sqld: OK (triggers installed)
    postvectortype_test.go:323: capture cursor (max prior sequence) = 0
    postvectortype_test.go:334: captured 1 change(s) from sqld:
    postvectortype_test.go:336:   change[0] seq=1 kind=insert table=vexp2_repl after=map[emb:{vector [4,5,6]} id:{integer 2}]
    postvectortype_test.go:351: captured insert id=2 emb={vector [4,5,6]}
    postvectortype_test.go:366: ApplyChanges on PG: OK
    postvectortype_test.go:372: PG vexp2_repl id=2 emb="[4,5,6]"
    postvectortype_test.go:379: PG emb <=> '[1,2,3]' (replicated row, direct on native column) = 0.025368153802923787
--- PASS: TestPostVectorTypeIncrementalReplication (3.79s)
```

Incremental capture → replication through `compat` produces a real, directly-queryable PG vector.

## PT-4. PG INSPECTION (the `atttypmod` supposition, now corrected) — `TestPostVectorTypeInspectPG` + `TestPostVectorTypeInspectPGNonVector`

A `vector(3)` table is created **directly** on PG (outside `compat`), then `compat.InspectSchema` is run.

**History (W3 fix).** As committed after the VectorType addition, `inspectPostgres` selected `atttypmod` from `information_schema.columns`, which has **no `atttypmod` column** in PG17 — the query raised `42703` and `InspectSchema` returned an error instead of family `vector`/args `[3]`. That broke inspection against **any** real PG table, not just vector ones. W3 corrected the source: `atttypmod` is now fetched, only for `udt_name='vector'` columns, via a scalar subquery against `pg_attribute` correlated on `(schema, table, column)` with `NOT attisdropped` (see `FIX-W3-REPORT.md` for the full query and rationale). A second latent bug surfaced once the column query succeeded — the routines query called `pg_get_function_result` over pgvector's `avg(vector)` aggregate in `public` (`42809`) — and was corrected in the same file by restricting `prokind` to `('f','p')`.

**Current results:**

1. **The supposition is confirmed at the SQL level.** `pg_attribute.atttypmod` for `vexp2_inspg.v` is `3` — pgvector stores the dimension verbatim in `atttypmod`. Validated directly, independent of the wrapper.

2. **Happy path: `InspectSchema` now returns the vector column as `Family=vector, Arguments=[3]`** against real PG17, and the non-vector `id` PK column inspects as `integer`. `InspectSchema` no longer errors; the pgvector support functions installed in `public` are surfaced as `unresolved` routines (correct — they are outside the canonical grammar), not as a hard failure. A companion test, `TestPostVectorTypeInspectPGNonVector`, inspects a plain table `(id INT PK, name TEXT, qty INT, price NUMERIC(38,18), created TIMESTAMP)` with no vector column and asserts `integer`/`text`/`integer`/`decimal`/`timestamp` — guarding the no-vector path the original regression had broken.

```
=== RUN   TestPostVectorTypeInspectPG
    postvectortype_test.go:432: raw pg_attribute.atttypmod for vexp2_inspg.v = 3
    postvectortype_test.go:436: CONFIRMED (direct pg_attribute): atttypmod=3 IS the pgvector dimension for vector(3)
    postvectortype_test.go:446: InspectSchema Exact=false unresolved=114
    postvectortype_test.go:448:   unresolved: kind=routine name=array_to_halfvec reason=routine return type "halfvec" is outside the canonical command grammar
    ... (pgvector support functions in public: array_to_*, cosine_distance, halfvec_*, vector_*, etc. — correctly unresolved, not a failure)
    postvectortype_test.go:461: inspected vexp2_inspg.v family="vector" args=[3] (raw atttypmod=3)
    postvectortype_test.go:470: inspected vexp2_inspg.id family="integer" (non-vector column inspects correctly)
--- PASS: TestPostVectorTypeInspectPG (1.97s)
=== RUN   TestPostVectorTypeInspectPGNonVector
    postvectortype_test.go:488: InspectSchema (no-vector) Exact=false unresolved=114
    ... (same pgvector support functions, correctly unresolved)
    postvectortype_test.go:503: inspected vexp2_inspn.id family="integer"
    postvectortype_test.go:503: inspected vexp2_inspn.name family="text"
    postvectortype_test.go:503: inspected vexp2_inspn.qty family="integer"
    postvectortype_test.go:503: inspected vexp2_inspn.price family="decimal"
    postvectortype_test.go:503: inspected vexp2_inspn.created family="timestamp"
--- PASS: TestPostVectorTypeInspectPGNonVector (1.82s)
```

The `unresolved=114` is the shared `public` schema carrying pgvector 0.8.5's support functions (installed by `CREATE EXTENSION vector`); they are not modeled by the canonical routine grammar and are reported as unresolved rather than translated — this is the designed behavior, not a regression. The asserted columns (`v` → `vector`/`[3]`; `id` → `integer`; the no-vector table's columns) are correct.

## PT-5. libSQL NATIVE INSPECTION — `TestInspectF32BlobMapsToVectorFamily` (updated §B)

A table `vexp_inspect(id INTEGER PRIMARY KEY, v F32_BLOB(3))` is created directly on sqld and inspected through a hand-built libSQL `Store`. **Post-VectorType**, `sqliteTypeFamily` matches `F32_BLOB` before the generic `BLOB` rule and returns `VectorType`, and `parseTypeArguments` extracts the declared dimension. The column is now inspected as `family="vector" args=[3]` (`Exact=true`, `unresolved=0`) — contrast with the pre-VectorType §B, which misread it as `binary` with no error. The old `TestInspectF32BlobMapsToBinaryFamily` asserted `binary` and now fails against the new code, so it was updated in place to assert the corrected `vector`/`[3]` behavior.

```
=== RUN   TestInspectF32BlobMapsToVectorFamily
    vector_test.go:254: InspectSchema Exact=true unresolved=0
    vector_test.go:274: inspected family of vexp_inspect.v (declared F32_BLOB(3)) = "vector" args=[3]
--- PASS: TestInspectF32BlobMapsToVectorFamily (1.06s)
```

## Full test run (verbatim, post-VectorType)

Command: `cd experiments/vector && go test ./... -v -count=1 -timeout 600s` — 11 tests (6 prior, with §B updated, + 4 Post-VectorType + 1 W3 non-vector guard), all PASS, no `t.Skip`. (The 114 `unresolved: kind=routine ...` lines emitted by the two PG inspection tests are pgvector's support functions in `public` and are elided below for readability; they are shown in full in §PT-4.)

```
=== RUN   TestPostVectorTypeApplySchema
    postvectortype_test.go:167: PG ApplySchema (canonical vector(3)): OK
    postvectortype_test.go:169: PG vexp2_apply.emb pg_type="vector" atttypmod=3
    postvectortype_test.go:182: libSQL ApplySchema (TEXT carrier for vector): OK
    postvectortype_test.go:188: libSQL vexp2_apply.emb declared type = "TEXT"
--- PASS: TestPostVectorTypeApplySchema (2.02s)
=== RUN   TestPostVectorTypeSnapshotAndVerify
    postvectortype_test.go:227: exported 2 rows from libSQL:
    postvectortype_test.go:230:   snap row[0] id={integer 1} emb={vector [1,2,3]}
    postvectortype_test.go:230:   snap row[1] id={integer 2} emb={vector [4,5,6]}
    postvectortype_test.go:241: ImportSnapshot to PG (native vector(3) column): OK
    postvectortype_test.go:244: PG vexp2_snap.emb pg_type="vector" atttypmod=3
    postvectortype_test.go:257: PG emb <=> '[1,2,3]' (direct on native vector column, no cast) = 0.025368153802923787
    postvectortype_test.go:268: native ANN index (emb hnsw vector_cosine_ops): CREATED directly on native vector column, no cast
    postvectortype_test.go:276: re-exported 2 rows from PG:
    postvectortype_test.go:278:   pgSnap row[0] id={integer 1} emb={vector [1,2,3]}
    postvectortype_test.go:278:   pgSnap row[1] id={integer 2} emb={vector [4,5,6]}
    postvectortype_test.go:284: VerifySnapshots source=b98da5dd9bf94b28873ceca9410a0a3d3dfcb222f53b4047153757edc856c3c8 equivalent=true
--- PASS: TestPostVectorTypeSnapshotAndVerify (2.61s)
=== RUN   TestPostVectorTypeIncrementalReplication
    postvectortype_test.go:319: InstallChangeCapture on sqld: OK (triggers installed)
    postvectortype_test.go:323: capture cursor (max prior sequence) = 0
    postvectortype_test.go:334: captured 1 change(s) from sqld:
    postvectortype_test.go:336:   change[0] seq=1 kind=insert table=vexp2_repl after=map[emb:{vector [4,5,6]} id:{integer 2}]
    postvectortype_test.go:351: captured insert id=2 emb={vector [4,5,6]}
    postvectortype_test.go:366: ApplyChanges on PG: OK
    postvectortype_test.go:372: PG vexp2_repl id=2 emb="[4,5,6]"
    postvectortype_test.go:379: PG emb <=> '[1,2,3]' (replicated row, direct on native column) = 0.025368153802923787
--- PASS: TestPostVectorTypeIncrementalReplication (3.79s)
=== RUN   TestPostVectorTypeInspectPG
    postvectortype_test.go:432: raw pg_attribute.atttypmod for vexp2_inspg.v = 3
    postvectortype_test.go:436: CONFIRMED (direct pg_attribute): atttypmod=3 IS the pgvector dimension for vector(3)
    postvectortype_test.go:446: InspectSchema Exact=false unresolved=114
    ... (114 unresolved routine entries: pgvector support functions in public — elided; see §PT-4)
    postvectortype_test.go:461: inspected vexp2_inspg.v family="vector" args=[3] (raw atttypmod=3)
    postvectortype_test.go:470: inspected vexp2_inspg.id family="integer" (non-vector column inspects correctly)
--- PASS: TestPostVectorTypeInspectPG (1.97s)
=== RUN   TestPostVectorTypeInspectPGNonVector
    postvectortype_test.go:486: InspectSchema (no-vector) Exact=false unresolved=114
    ... (114 unresolved routine entries: pgvector support functions in public — elided; see §PT-4)
    postvectortype_test.go:503: inspected vexp2_inspn.id family="integer"
    postvectortype_test.go:503: inspected vexp2_inspn.name family="text"
    postvectortype_test.go:503: inspected vexp2_inspn.qty family="integer"
    postvectortype_test.go:503: inspected vexp2_inspn.price family="decimal"
    postvectortype_test.go:503: inspected vexp2_inspn.created family="timestamp"
--- PASS: TestPostVectorTypeInspectPGNonVector (1.82s)
=== RUN   TestSanityDirectLibSQL
    vector_test.go:185: libsql vector_distance_cos([4,5,6],[1,2,3]) = 0.025368154048919678
    vector_test.go:195: libsql vector_top_k(...,1) -> id=1 (nearest to [1,2,3])
--- PASS: TestSanityDirectLibSQL (0.81s)
=== RUN   TestSanityDirectPgvector
    vector_test.go:216: pgvector [4,5,6] <=> [1,2,3] = 0.025368153802923787
    vector_test.go:220: pgvector DSN: postgres://postgres:***@<test-host>:5434/postgres?sslmode=disable
--- PASS: TestSanityDirectPgvector (1.05s)
=== RUN   TestInspectF32BlobMapsToVectorFamily
    vector_test.go:254: InspectSchema Exact=true unresolved=0
    vector_test.go:274: inspected family of vexp_inspect.v (declared F32_BLOB(3)) = "vector" args=[3]
--- PASS: TestInspectF32BlobMapsToVectorFamily (1.06s)
=== RUN   TestSnapshotBinaryRoute
    vector_test.go:310: libSQL raw F32_BLOB bytes (len=12): [0 0 128 63 0 0 0 64 0 0 64 64]
    vector_test.go:332: exported canonical value for v: kind=binary base64=AACAPwAAAEAAAEBA
    vector_test.go:337: decoded snapshot bytes (len=12): [0 0 128 63 0 0 0 64 0 0 64 64]
    vector_test.go:343: ImportSnapshot to PG bytea OK
    vector_test.go:350: PG bytea bytes (len=12): [0 0 128 63 0 0 0 64 0 0 64 64]
    vector_test.go:359: bytes identical libSQL -> PG bytea: PASS
    vector_test.go:366: bytea::vector error (literal): "ERROR: cannot cast type bytea to vector (SQLSTATE 42846)"
--- PASS: TestSnapshotBinaryRoute (1.68s)
=== RUN   TestTextReplicationRoute
    vector_test.go:411: InstallChangeCapture on sqld: OK (triggers installed)
    vector_test.go:417: capture cursor (max prior sequence) = 0
    vector_test.go:426: captured 1 change(s) from sqld:
    vector_test.go:428:   change[0] seq=1 kind=insert table=vexp_txt after=map[id:{integer 2} v:{text [4,5,6]}]
    vector_test.go:442: ApplyChanges on PG: OK
    vector_test.go:449: PG vexp_txt id=2 v="[4,5,6]"
    vector_test.go:459: PG v::vector <=> '[1,2,3]' = 0.025368153802923787
    vector_test.go:465: expression ANN index ((v::vector)) error (literal): "ERROR: column does not have dimensions (SQLSTATE 22023)"
    vector_test.go:476: expression ANN index ((v::vector(3)) hnsw vector_cosine_ops): CREATED (manual dimension step)
--- PASS: TestTextReplicationRoute (3.30s)
=== RUN   TestCatalogParserRejectsVectorDistanceCos
    vector_test.go:539:   unresolved: kind=view name=vexp_fn_v reason=unsupported catalog function "vector_distance_cos"
    vector_test.go:548: catalog rejection reason (literal): "unsupported catalog function \"vector_distance_cos\""
    vector_test.go:552: CONFIRMED: vector query translation does not exist in the compat layer
--- PASS: TestCatalogParserRejectsVectorDistanceCos (0.99s)
PASS
ok  	example.com/vector-exp	23.854s
```

## Root module — build, vet & test proof (post-W3)

The W3 fix touches `compat/inspect.go` (column-query `atttypmod` source + routines `prokind` filter), so the root module is re-validated. Command run from repo root: `go build ./... && go vet ./... && go test ./... -count=1`

```
===BUILD===
BUILD_EXIT=0
===VET===
VET_EXIT=0
===TEST===
?   	example.com/sqlite-postgres-compat/cmd/compat-audit	[no test files]
?   	example.com/sqlite-postgres-compat/cmd/compat-copy	[no test files]
ok  	example.com/sqlite-postgres-compat/compat	2.335s
TEST_EXIT=0
```

Pre-existing root-module tests were not modified; `compat` passes green.

## Files touched (post-VectorType + W3)

- `compat/inspect.go` — W3: column query fetches `atttypmod` from `pg_attribute` (scalar subquery, `udt_name='vector'` only, correlated on schema+table+column, `NOT attisdropped`) instead of the non-existent `information_schema.columns.atttypmod`; routines query restricted to `prokind IN ('f','p')` so `pg_get_function_result`/`pg_get_functiondef` are not called over pgvector's `avg(vector)` aggregate in `public`.
- `experiments/vector/postvectortype_test.go` — `TestPostVectorTypeInspectPG` rewritten to assert the happy path (`Family=vector`, `Arguments=[3]`, `id` → `integer`); new `TestPostVectorTypeInspectPGNonVector` guards the no-vector table path the original regression had broken; shared `findColumn` helper added.
- `experiments/vector/vector_test.go` — `TestInspectF32BlobMapsToBinaryFamily` updated in place to `TestInspectF32BlobMapsToVectorFamily` (asserts the corrected `vector`/`[3]` inspection) + `dropCompatMetadata` call to force catalog inspection.
- `VECTOR-COMPAT-REPORT.md` — Post-VectorType section + W3 correction (§PT-4, matrix cell #2, verbatim run, root proof, files list).
- `FIX-W3-REPORT.md` — W3 fix report (this work).