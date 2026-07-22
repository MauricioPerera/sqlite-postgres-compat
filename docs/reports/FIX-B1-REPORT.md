# FIX-B1 — Catalog expression language extensions

Three small extensions to the shared catalog expression grammar (`compat/sqlparse.go`)
with identical semantics on SQLite and PostgreSQL (`compat/ddl.go`). No public signatures
changed. No files outside the allowed set were touched.

## What changed

### 1. `||` concatenation (kind `concat`)

- New precedence level `{{"||", "concat"}}` inserted **between the comparison/LIKE
  level and the +/- level** (sqlparse.go, `levels`).
- Compiles to `||` on both engines via the existing binary-operator path
  (`operators["concat"] = "||"`), producing a fully parenthesized `(left || right)`.
- Associativity: **left**. `a || b || c` → `concat(concat(a, b), c)`, because
  `splitCatalogOperator` scans right-to-left and splits on the rightmost `||`.

### 2. `a NOT LIKE b` infix

- The LIKE split left a trailing `NOT` on the left side. `stripTrailingNot` detects a
  trailing NOT keyword (word-bounded, via `hasKeywordSuffix`) and folds the result into
  `not(like(...))`, matching the already-working prefix form `NOT a LIKE b`.
- The prefix form is untouched: it is still handled by the `index == 2` NOT branch
  before any operator level runs.

### 3. Scalar functions

- Added to the allowlist: `length`, `abs`, `trim` (1 arg), `coalesce` (variadic, ≥1),
  `replace` (exactly 3 args).
- `coalesce`/`replace` are multi-argument: new `splitTopLevelCommas` splits on top-level
  commas (depth- and quote-aware, so `replace(a, ',', b)` survives) and
  `parseFunctionArguments` parses each. Single-arg functions keep the prior single-parse path.
- Compiler emits them by name, upper-cased, on both engines: `LENGTH(...)`, `ABS(...)`,
  `TRIM(...)`, `COALESCE(...)` (args joined by `, `), `REPLACE(...)` (3 args).
- `trim` is supported only in its 1-arg form (trim spaces). The 2-arg form
  (`trim(X, Y)`) is intentionally not added: Postgres lacks the `trim(s, c)` syntax and it
  would diverge — out of scope per the brief.

## Trade-offs

- **`||` precedence** — chosen as its own level between comparison and +/- (looser than
  `+`/`-`, tighter than comparison). Rationale: putting it in the same level as `+`/`-`
  would give rightmost-operator-wins semantics for mixed `a || b + c`, which is
  surprising. A dedicated level yields a clean grouping. Because the compiler always emits
  fully parenthesized binary forms, the chosen level only affects how *unparenthesized*
  source maps to the AST; both engines then execute the identical parenthesized SQL, so
  runtime semantics are identical across engines regardless of each engine's native
  precedence. `a || b + c` → `concat(a, add(b, c))` (matches SQLite's native precedence;
  differs from Postgres native for the bare form, but the emitted SQL is explicit).
- `coalesce` accepts ≥1 argument (matches both engines, which accept a single arg).

## Definition-of-done evidence (real output)

### `go build ./...`
```
---BUILD OK---
```

### `go vet ./...`
```
---VET OK---
```

### `go test ./... -count=1`
```
?   	example.com/sqlite-postgres-compat/cmd/compat-audit	[no test files]
?   	example.com/sqlite-postgres-compat/cmd/compat-copy	[no test files]
ok  	example.com/sqlite-postgres-compat/compat	2.043s
EXIT=0
```

### New parse tests (AST asserted) — all PASS
- `TestParseCatalogExpressionConcat`: `a || b` → `concat(a,b)`; `a || b || c` →
  left-assoc; `a || b = c` → `eq(concat(a,b),c)`; `a || b + c` → `concat(a, add(b,c))`;
  string-literal chain.
- `TestParseCatalogExpressionNotLike`: `name NOT LIKE 'x%'` → `not(like(name,'x%'))`;
  `NOT name LIKE 'x%'` → `not(like(...))` (no regression); combined with `AND`.
- `TestParseCatalogExpressionScalarFunctions`: `length(a) = 5`, `abs(a)`, `trim(a)`,
  `coalesce(a, b, 'x')`, `replace(a, 'x', 'y')`, nested `coalesce(a, trim(b))`,
  `coalesce(a, '')`.
- `TestParseCatalogExpressionRejectsUnlistedFunction`: `substr(a, 1, 2)` rejected;
  `replace(a, 'x')` (wrong arity) rejected.

### New compile tests (both engines) — all PASS
- `TestCompileConcatForBothEngines`: `("first" || (' ' || "last"))` on SQLite and Postgres.
- `TestCompileScalarFunctionsForBothEngines`: `LENGTH("code")`, `ABS("value")`,
  `TRIM("code")`, `COALESCE("code", '')`, `REPLACE("code", '-', '_')` on both engines.
- `TestCompileNotLikeForBothEngines`: `(NOT ("code" LIKE 'x%'))` on SQLite and
  `(NOT ("code" ILIKE 'x%'))` on Postgres (reuses the existing like fix; not duplicated).

Pre-existing tests were neither modified nor deleted.