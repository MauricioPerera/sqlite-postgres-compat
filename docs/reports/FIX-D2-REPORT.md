# FIX-D2 — Alineación docs ↔ código + AGENTS.md + contrato de migración

Date: 2026-07-22.
Alcance: documentación alineada al código real (sin tocar código/tests/go.mod/examples/experiments), nuevo `AGENTS.md`, nuevo `contracts/migration.contract.example.md`.

Archivos modificados/creados:
- `README.md` (editado)
- `docs/USAGE.md` (editado)
- `docs/COMPATIBILITY.md` (editado)
- `docs/TESTING.md` (editado)
- `docs/ARCHITECTURE.md` (editado)
- `docs/OPERATIONS.md` (editado)
- `docs/reports/VALIDATION_REPORT.md` (reescrito como informe vigente)
- `AGENTS.md` (nuevo, inglés)
- `contracts/migration.contract.example.md` (nuevo, inglés)

No se modificó código, tests, `go.mod`, `examples/` ni `experiments/`.

## 1. Greps de presencia

```
$ grep -n "ILIKE" docs/COMPATIBILITY.md docs/USAGE.md
docs/COMPATIBILITY.md:33:`LIKE` se compila a `ILIKE` en PostgreSQL para preservar la semántica de SQLite (insensible a mayúsculas/minúsculas en ASCII); es el mapeo pragmático estándar y se acepta como compromiso conocido (ILIKE pliega todo el rango Unicode, SQLite sólo ASCII).
docs/USAGE.md:204:Las rutinas canónicas se ejecutan en una transacción. Las acciones admitidas son `INSERT`, `UPDATE` y `DELETE` ... `LIKE` se compila a `ILIKE` en PostgreSQL para preservar la semántica de SQLite. ...

$ grep -n "canonical_vectors" README.md docs/COMPATIBILITY.md
README.md:7:... tipos canónicos escalares y `vector(N)` (feature `canonical_vectors`), ...
docs/COMPATIBILITY.md:50:- `canonical_vectors`.

$ grep -rn "coalesce" docs/
docs/COMPATIBILITY.md:35:Funciones escalares admitidas (allowlist exacta): agregadas `count`, `sum`, `avg`, `min`, `max` (aceptan `*` o una expresión); de caja `lower`, `upper` (una expresión); `length`, `abs`, `trim` (una expresión); `coalesce` (al menos un argumento); `replace` (exactamente tres argumentos). ...
docs/reports/VALIDATION_REPORT.md:34:- Allowlist de funciones escalares ampliada: `length`, `abs`, `coalesce`, `trim`, `replace` además de `count`, `sum`, `avg`, `min`, `max`, `lower`, `upper`.

$ grep -rn "23\|24" docs/TESTING.md docs/reports/VALIDATION_REPORT.md
docs/TESTING.md:48:La batería E2E tiene 24 pruebas de nivel superior distribuidas en tres archivos: `e2e/system_test.go`, `e2e/suppress_test.go` y `e2e/cutover_test.go`. Hoy son 23 superadas y 1 fallida de forma intencional (`TestSystemClaimsExactCoverageForRequiredFeatureFamilies`). ...
docs/reports/VALIDATION_REPORT.md:65:El resultado de la batería es 23 pruebas superiores superadas y 1 fallida, sobre 24 pruebas de nivel superior (23 PASS + 1 FAIL intencional, `TestSystemClaimsExactCoverageForRequiredFeatureFamilies`). ...

$ grep -n "AGENTS" README.md
71:- [Especificación para agentes/LLMs](AGENTS.md): gramática canónica, CLIs y flujo de migración.
```

Conteo real verificado contra el código (24 funciones `Test*` en `e2e/`, excluyendo `TestMain`):

```
$ grep -rh "^func Test" e2e/*.go | grep -v TestMain | wc -l
24
```

## 2. Greps de ausencia

```
$ grep -rn "solo INSERT\|only insert" docs/ | grep -vi report
(no hits — exit 1)

$ grep -n "15 pruebas\|15 superiores\|15 tests" docs/reports/VALIDATION_REPORT.md
(no hits — exit 1)
```

Ninguna mención de "solo INSERT" / "only insert" contradice las rutinas UPDATE/DELETE. La frase "15 pruebas" ya no aparece en `VALIDATION_REPORT.md`.

## 3. AGENTS.md: niveles de precedencia y allowlist transcritos del parser

### Fragmento del parser (`compat/sqlparse.go`, líneas 22–30 y 36–42)

```go
levels := [][]catalogOperator{
    {{"OR", "or"}},                                                          // 0
    {{"AND", "and"}},                                                        // 1
    {{"IS NOT NULL", "is_not_null"}, {"IS NULL", "is_null"}},               // 2
    {{"<=", "lte"}, {">=", "gte"}, {"<>", "ne"}, {"!=", "ne"}, {"=", "eq"}, {"<", "lt"}, {">", "gt"}, {"LIKE", "like"}}, // 3
    {{"||", "concat"}},                                                      // 4
    {{"+", "add"}, {"-", "sub"}},                                            // 5
    {{"*", "mul"}, {"/", "div"}},                                            // 6
}
// NOT se maneja en index==2 (nivel IS NULL), antes de splitCatalogOperator:
if index == 2 && hasKeywordPrefix(text, "NOT") {
    argument, err := parseCatalogExpression(strings.TrimSpace(text[3:]))
    ...
    return Expression{Kind: "not", Args: []Expression{argument}}, nil
}
```

Allowlist del parser (`compat/sqlparse.go`, líneas 99–135):

```go
case "count", "sum", "avg", "min", "max", "lower", "upper":  // '*' o una expr
case "length", "abs", "trim":                                  // una expr
case "coalesce":                                               // ≥1 argumento
case "replace":                                                // exactamente 3
default: return ..., fmt.Errorf("unsupported catalog function %q", function)
```

### Sección correspondiente de `AGENTS.md` (§3)

```
Operator precedence, loosest to tightest:
1. OR
2. AND
3. NOT — handled between AND and the IS NULL/comparison levels, so NOT a = b parses as not(eq(a, b)).
4. IS NULL / IS NOT NULL
5. comparisons: <=, >=, <>, !=, =, <, >, LIKE
6. || (concatenation)
7. +, -
8. *, / (tightest)

Function allowlist (exact):
count, sum, avg, min, max   — * or one expression
lower, upper                — one expression
length, abs, trim           — one expression
coalesce                    — at least one argument
replace                     — exactly three arguments
Every other function name is rejected with `unsupported catalog function %q`.
```

Coincidencia: el orden de niveles (OR → AND → NOT → IS NULL → comparaciones → `||` → `+`/`-` → `*`/`/`) y la allowlist de funciones coinciden exactamente con `compat/sqlparse.go`. El mapeo `LIKE`→`ILIKE` coincide con `compat/ddl.go:291-296` y `compat/runtime.go:156-160`.

## 4. Comandos/corridos sin Postgres

```
$ go test ./...
?       example.com/sqlite-postgres-compat/cmd/compat-audit       [no test files]
?       example.com/sqlite-postgres-compat/cmd/compat-copy        [no test files]
?       example.com/sqlite-postgres-compat/cmd/compat-cutover     [no test files]
ok      example.com/sqlite-postgres-compat/compat   (cached)
TEST_EXIT=0

$ go vet ./...
VET_EXIT=0

$ go run ./cmd/compat-audit ./examples/contract.example.json
[{"feature":"tables","status":"exact"},{"feature":"canonical_foreign_keys","status":"exact"},{"feature":"transactions","status":"exact"}]
AUDIT_EXIT=0
```

Claims que exigen PostgreSQL (no corridas aquí, marcadas según evidencia):
- Cutover SQLite→PG con `status=ready`: según evidencia de `docs/reports/FIX-P2-REPORT.md` y `docs/reports/FIX-P1-REPORT.md`.
- Supresión anti-eco transaccional (GUC `compat.suppress`, aislamiento MVCC): según evidencia de `e2e/suppress_test.go` (`TestSuppressIsolationDoesNotLeakAcrossConnections`).
- Catch-up tolerante y divergencia genuina: según evidencia de `e2e/cutover_test.go`.
- Validación vectorial libSQL/pgvector: según evidencia de `docs/reports/VECTOR-COMPAT-REPORT.md`.
- Inspección `F32_BLOB(N)` de libSQL y `vector` de pgvector: según `compat/inspect.go` (líneas 142, 319–348, 372–418, 733–734) y `docs/reports/VECTOR-COMPAT-REPORT.md`.

## 5. build / vet / test verdes (sin tocar código)

```
$ go build ./... && go vet ./... && go test ./...
?       example.com/sqlite-postgres-compat/cmd/compat-audit       [no test files]
?       example.com/sqlite-postgres-compat/cmd/compat-copy        [no test files]
?       example.com/sqlite-postgres-compat/cmd/compat-cutover     [no test files]
ok      example.com/sqlite-postgres-compat/compat   (cached)
BUILD_EXIT=0  VET_EXIT=0  TEST_EXIT=0
```

## Hallazgos docs ↔ código

No se encontró ninguna contradicción que requiriera cambiar código. Toda la alineación se hizo ajustando la docs a lo que el código hace hoy:

- Rutinas: la docs decía "comandos parametrizados de inserción"; el código (`compat/sqlroutine.go`, `compat/runtime.go`) soporta `INSERT`/`UPDATE`/`DELETE`. Docs corregida.
- Like→ILIKE: documentado en `COMPATIBILITY.md`, `USAGE.md` y `AGENTS.md` según `compat/ddl.go:286-296`.
- Allowlist de funciones: documentada en `COMPATIBILITY.md` y `AGENTS.md` según `compat/sqlparse.go` y `compat/ddl.go:315-341`.
- Tipos `vector(N)` y `canonical_vectors`: documentados en `README.md`, `COMPATIBILITY.md`, `ARCHITECTURE.md`, `VALIDATION_REPORT.md`, `AGENTS.md` según `compat/schema.go:49,207-214` y `compat/ddl.go:133-140,173-180`.
- Supresión anti-eco transaccional (GUC local en Postgres): documentada en `README.md`, `USAGE.md`, `ARCHITECTURE.md`, `OPERATIONS.md`, `TESTING.md`, `VALIDATION_REPORT.md` según `compat/replicate.go:135-163` y `e2e/suppress_test.go`.
- `ConflictError` con `Expected`/`Actual`: documentado en `README.md`, `USAGE.md`, `ARCHITECTURE.md`, `OPERATIONS.md` según `compat/replicate.go:15-29`.
- `Version{0,0,0}` inválida: documentado en `USAGE.md`, `OPERATIONS.md`, `AGENTS.md` según `compat/spec.go:23-33`.
- Cutover / `ApplyChangesTolerant`: documentado en `README.md`, `USAGE.md`, `TESTING.md`, `VALIDATION_REPORT.md`, `AGENTS.md` según `cmd/compat-cutover/main.go` y `compat/replicate.go:38-59`.
- Conteo E2E: 24 tests (23 PASS + 1 FAIL intencional), actualizado en `TESTING.md` y `VALIDATION_REPORT.md` según `e2e/{system,suppress,cutover}_test.go`.
- `VALIDATION_REPORT.md`: reescrito como informe vigente (2026-07-22, PostgreSQL 17.10), cita `VECTOR-COMPAT-REPORT.md`, `FIX-P1/P2-REPORT.md` y los tests e2e.