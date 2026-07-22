# FIX-W3 — `compat.InspectSchema` regression against real PostgreSQL 17

Date: 2026-07-22. Module: `example.com/sqlite-postgres-compat` (repo root), experiment module `example.com/vector-exp` under `experiments/vector/`.

Infra (remote, live, used for validation):

- PostgreSQL 17 + pgvector 0.8.5: `postgres://postgres:***@<test-host>:5434/postgres?sslmode=disable` (`VECTOR_PG_DSN`).
- libSQL / sqld with native vectors: `http://<test-host>:8081` (`VECTOR_LIBSQL_URL`).

## 1. Root cause

`compat/inspect.go:inspectPostgres` selected `atttypmod` from `information_schema.columns`. That view has **no `atttypmod` column** in PostgreSQL 17, so the column query raised `42703` (`column "atttypmod" does not exist`) and `InspectSchema` returned an error. Because the column query is the first thing `inspectPostgres` runs, this broke `InspectSchema` against **any** real PG table — vector or not. The regression was introduced when `VectorType` was added (the mapping `postgresType(dataType, udtName, atttypmod)` is correct; only the **source** of `atttypmod` was wrong).

The semantic supposition was already validated directly against real PG: `pg_attribute.atttypmod = 3` for a `vector(3)` column (pgvector stores the dimension verbatim in `atttypmod`).

## 2. The fix

### 2a. Column query — `atttypmod` sourced from `pg_attribute` (the requested fix)

`atttypmod` is fetched only for `udt_name='vector'` columns, via a scalar subquery correlated on `(schema, table, column)` against `pg_attribute` joined to `pg_class` and `pg_namespace`. The scan order and the rest of the flow are unchanged.

Final column query (`compat/inspect.go`):

```sql
SELECT c.table_name, c.column_name, c.data_type, c.udt_name,
    CASE WHEN c.udt_name = 'vector'
        THEN COALESCE((SELECT a.atttypmod
            FROM pg_attribute a
            JOIN pg_class cl ON cl.oid = a.attrelid
            JOIN pg_namespace n ON n.oid = cl.relnamespace
            WHERE n.nspname = c.table_schema AND cl.relname = c.table_name AND a.attname = c.column_name AND NOT a.attisdropped), -1)
        ELSE -1 END AS atttypmod,
    c.is_nullable, c.column_default, c.is_identity, c.is_generated
FROM information_schema.columns c
WHERE c.table_schema = current_schema() AND c.table_name NOT IN ($1, $2, $3, $4)
AND c.table_name IN (SELECT table_name FROM information_schema.tables WHERE table_schema = current_schema() AND table_type = 'BASE TABLE')
ORDER BY c.table_name, c.ordinal_position
-- $1..$4 = schemaMetadataTable, appliedChangesTable, captureStateTable, changeJournalTable
```

Why this is correct:

- **`atttypmod` source.** `information_schema.columns` does not expose `atttypmod` in PG17; `pg_attribute.atttypmod` is where it actually lives. The scalar subquery reads it from there.
- **Dropped columns.** A dropped column keeps its `attname` in `pg_attribute` but is marked `attisdropped = true` (and gets a negative `attnum`). `NOT a.attisdropped` selects only the live attribute, so a dropped-then-recreated same-named column resolves to the live one. Within one table, two live columns cannot share an `attname` (PG enforces uniqueness among non-dropped attributes), so the scalar subquery returns exactly one row for a live column and `COALESCE(..., -1)` covers the not-found case.
- **Schemas.** The subquery correlates on `n.nspname = c.table_schema` (the row's own schema), not a bare `current_schema()`. The outer query already restricts `c.table_schema = current_schema()`, but keying the correlation on `c.table_schema` makes the subquery correct by construction regardless of the outer filter, and means a column is only ever matched against the attribute of the table in **that** schema.
- **Homonym tables in other schemas.** Because the join binds `pg_namespace.nspname` to `c.table_schema` AND `pg_class.relname` to `c.table_name`, a table with the same name in a different schema cannot contribute its `pg_attribute` row to this column. The match is unique per `(schema, table, column)`.
- **Non-vector columns.** `atttypmod` is only consumed by `postgresType` when `udtName == "vector"`; for every other column the `CASE` returns `-1` and the subquery is not evaluated, so there is no per-row catalog cost for the common case and no behavior change for non-vector types.

### 2b. Routines query — `prokind` filter (additional in-scope, same-file fix)

Fixing 2a let the column query succeed, which exposed a **second latent bug** in the same function: the routines query called `pg_get_function_result(proc.oid)` / `pg_get_functiondef(proc.oid)` over **every** `pg_proc` row in `current_schema()`. pgvector 0.8.5 installs an `avg(vector)` **aggregate** (`prokind='a'`) in the `public` schema, and `pg_get_function_result` raises `42809` (`"avg" is an aggregate function`) on an aggregate — aborting the whole inspection with a different error than the one W3 was scoped to. Confirmed by probing `pg_proc` in `public`:

```
proc name="avg" kind="a" ns="public"      <- aggregate; pg_get_function_result errors here
proc name="array_to_vector" kind="f" ns="public"
proc name="cosine_distance" kind="f" ns="public"
... (pgvector support functions, all kind="f")
```

The routines query now restricts to plain functions and procedures:

```sql
SELECT proc.proname, proc.prosrc, pg_get_function_arguments(proc.oid), COALESCE(pg_get_function_result(proc.oid), ''), lang.lanname, proc.prokind::text, pg_get_functiondef(proc.oid)
FROM pg_proc proc
JOIN pg_namespace ns ON ns.oid = proc.pronamespace
JOIN pg_language lang ON lang.oid = proc.prolang
WHERE ns.nspname = current_schema()
AND proc.prokind IN ('f', 'p')
AND NOT EXISTS (SELECT 1 FROM pg_trigger trg WHERE trg.tgfoid = proc.oid)
ORDER BY proc.proname
```

Rationale: the canonical routine grammar models functions (`'f'`) and procedures (`'p'`) only — aggregates (`'a'`) and window functions (`'w'`) are not routines `compat` represents, and `pg_get_function_result`/`pg_get_functiondef` are only valid for `'f'`/`'p'`. This is a principled filter, not a behavior change for modeled routines. No public signatures changed; both edits are inside `compat/inspect.go` (the single file the task scoped for the fix).

## 3. Validation

### 3a. Experiment module — `cd experiments/vector && go test ./... -v -count=1 -timeout 600s`

11 tests, all PASS against the live remote engines (PG 17 + pgvector, libSQL). Includes the PG inspection happy path for a vector table (`TestPostVectorTypeInspectPG`: `v` → `Family=vector, Arguments=[3]`, `id` → `integer`) and a no-vector table (`TestPostVectorTypeInspectPGNonVector`: `integer`/`text`/`integer`/`decimal`/`timestamp`). The 114 `unresolved: kind=routine ...` entries emitted by the two PG inspection tests are pgvector's support functions installed in `public` by `CREATE EXTENSION vector` — they are outside the canonical routine grammar and are reported as `unresolved` (the designed behavior), not a hard failure. They are elided below with an explicit count; the full list is in `VECTOR-COMPAT-REPORT.md` §PT-4.

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
--- PASS: TestPostVectorTypeSnapshotAndVerify (2.53s)
=== RUN   TestPostVectorTypeIncrementalReplication
    postvectortype_test.go:319: InstallChangeCapture on sqld: OK (triggers installed)
    postvectortype_test.go:323: capture cursor (max prior sequence) = 0
    postvectortype_test.go:334: captured 1 change(s) from sqld:
    postvectortype_test.go:336:   change[0] seq=1 kind=insert table=vexp2_repl after=map[emb:{vector [4,5,6]} id:{integer 2}]
    postvectortype_test.go:351: captured insert id=2 emb={vector [4,5,6]}
    postvectortype_test.go:366: ApplyChanges on PG: OK
    postvectortype_test.go:372: PG vexp2_repl id=2 emb="[4,5,6]"
    postvectortype_test.go:379: PG emb <=> '[1,2,3]' (replicated row, direct on native column) = 0.025368153802923787
--- PASS: TestPostVectorTypeIncrementalReplication (3.75s)
=== RUN   TestPostVectorTypeInspectPG
    postvectortype_test.go:432: raw pg_attribute.atttypmod for vexp2_inspg.v = 3
    postvectortype_test.go:436: CONFIRMED (direct pg_attribute): atttypmod=3 IS the pgvector dimension for vector(3)
    postvectortype_test.go:446: InspectSchema Exact=false unresolved=114
    ... 114 unresolved kind=routine entries (pgvector support functions in public: array_to_*, cosine_distance, halfvec_*, vector_*, etc.) — full list identical to VECTOR-COMPAT-REPORT.md §PT-4, elided here ...
    postvectortype_test.go:451: inspected vexp2_inspg.v family="vector" args=[3] (raw atttypmod=3)
    postvectortype_test.go:464: inspected vexp2_inspg.id family="integer" (non-vector column inspects correctly)
--- PASS: TestPostVectorTypeInspectPG (1.97s)
=== RUN   TestPostVectorTypeInspectPGNonVector
    postvectortype_test.go:486: InspectSchema (no-vector) Exact=false unresolved=114
    ... 114 unresolved kind=routine entries (pgvector support functions in public) — elided, see §PT-4 ...
    postvectortype_test.go:503: inspected vexp2_inspn.id family="integer"
    postvectortype_test.go:503: inspected vexp2_inspn.name family="text"
    postvectortype_test.go:503: inspected vexp2_inspn.qty family="integer"
    postvectortype_test.go:503: inspected vexp2_inspn.price family="decimal"
    postvectortype_test.go:503: inspected vexp2_inspn.created family="timestamp"
--- PASS: TestPostVectorTypeInspectPGNonVector (1.82s)
=== RUN   TestSanityDirectLibSQL
    vector_test.go:185: libsql vector_distance_cos([4,5,6],[1,2,3]) = 0.025368154048919678
    vector_test.go:195: libsql vector_top_k(...,1) -> id=1 (nearest to [1,2,3])
--- PASS: TestSanityDirectLibSQL (0.85s)
=== RUN   TestSanityDirectPgvector
    vector_test.go:216: pgvector [4,5,6] <=> [1,2,3] = 0.025368153802923787
    vector_test.go:220: pgvector DSN: postgres://postgres:***@<test-host>:5434/postgres?sslmode=disable
--- PASS: TestSanityDirectPgvector (0.96s)
=== RUN   TestInspectF32BlobMapsToVectorFamily
    vector_test.go:254: InspectSchema Exact=true unresolved=0
    vector_test.go:274: inspected family of vexp_inspect.v (declared F32_BLOB(3)) = "vector" args=[3]
--- PASS: TestInspectF32BlobMapsToVectorFamily (0.99s)
=== RUN   TestSnapshotBinaryRoute
    vector_test.go:310: libSQL raw F32_BLOB bytes (len=12): [0 0 128 63 0 0 0 64 0 0 64 64]
    vector_test.go:332: exported canonical value for v: kind=binary base64=AACAPwAAAEAAAEBA
    vector_test.go:337: decoded snapshot bytes (len=12): [0 0 128 63 0 0 0 64 0 0 64 64]
    vector_test.go:343: ImportSnapshot to PG bytea OK
    vector_test.go:350: PG bytea bytes (len=12): [0 0 128 63 0 0 0 64 0 0 64 64]
    vector_test.go:359: bytes identical libSQL -> PG bytea: PASS
    vector_test.go:366: bytea::vector error (literal): "ERROR: cannot cast type bytea to vector (SQLSTATE 42846)"
--- PASS: TestSnapshotBinaryRoute (1.71s)
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
--- PASS: TestTextReplicationRoute (3.38s)
=== RUN   TestCatalogParserRejectsVectorDistanceCos
    vector_test.go:539:   unresolved: kind=view name=vexp_fn_v reason=unsupported catalog function "vector_distance_cos"
    vector_test.go:548: catalog rejection reason (literal): "unsupported catalog function \"vector_distance_cos\""
    vector_test.go:552: CONFIRMED: vector query translation does not exist in the compat layer
--- PASS: TestCatalogParserRejectsVectorDistanceCos (0.97s)
PASS
ok  	example.com/vector-exp	23.854s
```

### 3b. Root module — `go build ./... && go vet ./... && go test ./... -count=1` (from repo root)

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

## 4. Files touched

- `compat/inspect.go` — column query `atttypmod` source (2a) + routines `prokind IN ('f','p')` filter (2b).
- `experiments/vector/postvectortype_test.go` — `TestPostVectorTypeInspectPG` rewritten to assert the happy path; new `TestPostVectorTypeInspectPGNonVector` (no-vector guard); shared `findColumn` helper.
- `VECTOR-COMPAT-REPORT.md` — §PT-4 and matrix cell #2 updated (finding → corrected, with historical note); verbatim run, root proof and files list refreshed.
- `FIX-W3-REPORT.md` — this report.

No public signatures changed. No files outside the repo were touched. DSN password masked in all pasted output.