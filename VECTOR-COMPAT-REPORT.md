# Vector Compatibility Matrix — libSQL (sqld) ↔ pgvector via the `compat` layer

Date: 2026-07-22
Module under test: `experiments/vector/` (module `example.com/vector-exp`, `replace` → root `example.com/sqlite-postgres-compat`).
Infra (remote, ~100–300ms RTT):

- libSQL / sqld with native vectors: `http://31.220.22.176:8081` (`VECTOR_LIBSQL_URL`).
- PostgreSQL 17 + pgvector 0.8.5: `postgres://postgres:***@31.220.22.176:5434/postgres?sslmode=disable` (`VECTOR_PG_DSN`).

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
    vector_test.go:220: pgvector DSN: postgres://postgres:***@31.220.22.176:5434/postgres?sslmode=disable
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
    vector_test.go:220: pgvector DSN: postgres://postgres:***@31.220.22.176:5434/postgres?sslmode=disable
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