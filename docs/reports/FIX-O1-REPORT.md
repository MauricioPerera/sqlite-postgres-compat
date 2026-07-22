# FIX-O1 — Reorganización de la raíz

Orden de la raíz del repo sin tocar código funcional ni tests: los reportes de
trabajo pasan a `docs/reports/` y los ejemplos `.example.json` pasan a
`examples/`. Todas las referencias (comandos de README/docs, links markdown y
comentarios en `compat/*.go`) se actualizaron a las rutas nuevas.

## Movimientos

Reportes (`*.md`) movidos con `git mv` a `docs/reports/`:

- `AUDIT-A-REPORT.md`, `AUDIT-B-REPORT.md`, `AUDIT-C-REPORT.md`
- `FIX-F1-REPORT.md`, `FIX-F2-REPORT.md`
- `FIX-M1-REPORT.md` … `FIX-M4-REPORT.md`
- `FIX-B1-REPORT.md` … `FIX-B4-REPORT.md`
- `FIX-W1-REPORT.md`, `FIX-W1B-REPORT.md`, `FIX-W3-REPORT.md`
- `FIX-P1-REPORT.md`, `FIX-P2-REPORT.md`
- `VECTOR-COMPAT-REPORT.md`, `VALIDATION_REPORT.md`

Ejemplos movidos con `git mv` a `examples/`:

- `contract.example.json`, `migration.example.json`, `cutover.example.json`

No se editó el contenido de ningún reporte movido. Se crearon `docs/reports/README.md`
(índice por ciclo) y este reporte.

## Referencias actualizadas

Comandos y prosa en README/docs (`.\contract.example.json` → `.\examples\contract.example.json`, etc.):

- `README.md:22` — `go run ./cmd/compat-audit .\examples\contract.example.json`
- `README.md:25` — `edita examples/migration.example.json con DSN reales`
- `README.md:28` — `go run ./cmd/compat-copy .\examples\migration.example.json`
- `docs/USAGE.md:6` — `go run ./cmd/compat-audit .\examples\contract.example.json`
- `docs/USAGE.md:23` — `Configura examples/migration.example.json:`
- `docs/USAGE.md:49` — `go run ./cmd/compat-copy .\examples\migration.example.json`
- `docs/USAGE.md:163` — `Configura examples/cutover.example.json:`

Links markdown a `VALIDATION_REPORT.md` (ruta relativa correcta desde cada archivo):

- `README.md:70` — `[Informe de la última validación](docs/reports/VALIDATION_REPORT.md)`
- `docs/COMPATIBILITY.md:60` — `[VALIDATION_REPORT.md](reports/VALIDATION_REPORT.md)`
- `docs/TESTING.md:46` — `[VALIDATION_REPORT.md](reports/VALIDATION_REPORT.md)`

Comentarios en `compat/*.go` (solo líneas de comentario):

- `compat/ddl.go:136` — `validated against libSQL/sqld and pgvector in docs/reports/VECTOR-COMPAT-REPORT.md:`
- `compat/inspect.go:322` — `silently misread as binary (see docs/reports/VECTOR-COMPAT-REPORT.md §B).`
- `compat/sqlroutine.go:103` — `docs/reports/FIX-B4-REPORT.md: comparisons and logical composition against columns,`

Búsqueda adicional (`grep -rn` por cada nombre movido sobre `README.md docs compat cmd e2e scripts experiments`,
incluso extensiones `.ps1/.ts/.js/.yaml/.yml/.toml`): sin referencias no listadas fuera de las corregidas.

## Definición de hecho

### 1. `ls` de la raíz

```
README.md
cmd/
compat/
docs/
e2e/
examples/
experiments/
go.mod
go.sum
scripts/
```

Quedan solo `README.md`, `go.mod`, `go.sum` y directorios (`cmd`, `compat`, `docs`,
`e2e`, `examples`, `experiments`, `scripts`).

### 2. Referencias restantes por archivo movido

`grep -rn "<nombre>" README.md docs docs/reports compat cmd e2e scripts experiments`.

Hits **fuera de `docs/reports`** (todos con la ruta nueva):

- `VALIDATION_REPORT.md`:
  - `README.md:70` → `docs/reports/VALIDATION_REPORT.md`
  - `docs/COMPATIBILITY.md:60` → `reports/VALIDATION_REPORT.md`
  - `docs/TESTING.md:46` → `reports/VALIDATION_REPORT.md`
- `contract.example.json`:
  - `README.md:22` → `.\examples\contract.example.json`
  - `docs/USAGE.md:6` → `.\examples\contract.example.json`
- `migration.example.json`:
  - `README.md:25` → `examples/migration.example.json`
  - `README.md:28` → `.\examples\migration.example.json`
  - `docs/USAGE.md:23` → `examples/migration.example.json`
  - `docs/USAGE.md:49` → `.\examples\migration.example.json`
- `cutover.example.json`:
  - `docs/USAGE.md:163` → `examples/cutover.example.json`
- `VECTOR-COMPAT-REPORT.md`:
  - `compat/ddl.go:136` → `docs/reports/VECTOR-COMPAT-REPORT.md`
  - `compat/inspect.go:322` → `docs/reports/VECTOR-COMPAT-REPORT.md`
- `FIX-B4-REPORT.md`:
  - `compat/sqlroutine.go:103` → `docs/reports/FIX-B4-REPORT.md`

El resto de los nombres movidos (`AUDIT-A/B/C`, `FIX-F1/F2`, `FIX-M1..M4`,
`FIX-B1..B3`, `FIX-W1/W1B/W3`, `FIX-P1/P2`) sólo aparecen dentro de
`docs/reports/` (self, índice `docs/reports/README.md` o referencias cruzadas
entre reportes, cuyo contenido no se edita por regla). Ningún hit fuera de
`docs/reports` queda con la ruta vieja.

### 3. `go run ./cmd/compat-audit ./examples/contract.example.json`

```
[{"feature":"tables","status":"exact"},{"feature":"canonical_foreign_keys","status":"exact"},{"feature":"transactions","status":"exact"}]
EXIT=0
```

Corre y termina con exit 0.

### 4. `go build ./... && go vet ./... && go test ./...`

```
BUILD_OK
VET_OK
?       example.com/sqlite-postgres-compat/cmd/compat-audit  [no test files]
?       example.com/sqlite-postgres-compat/cmd/compat-copy   [no test files]
?       example.com/sqlite-postgres-compat/cmd/compat-cutover        [no test files]
ok      example.com/sqlite-postgres-compat/compat     (cached)
TEST_EXIT=0
```

Build, vet y tests verdes. (`e2e/` usa el build tag `//go:build e2e` y se ejecuta vía
`scripts/test-system.ps1` con PostgreSQL real; no forma parte de `go test ./...`,
igual que en el baseline.)

### 5. `git status --short` final

```
 M README.md
 M compat/ddl.go
 M compat/inspect.go
 M compat/sqlroutine.go
 M docs/COMPATIBILITY.md
 M docs/TESTING.md
 M docs/USAGE.md
R  AUDIT-A-REPORT.md -> docs/reports/AUDIT-A-REPORT.md
R  AUDIT-B-REPORT.md -> docs/reports/AUDIT-B-REPORT.md
R  AUDIT-C-REPORT.md -> docs/reports/AUDIT-C-REPORT.md
R  FIX-B1-REPORT.md -> docs/reports/FIX-B1-REPORT.md
R  FIX-B2-REPORT.md -> docs/reports/FIX-B2-REPORT.md
R  FIX-B3-REPORT.md -> docs/reports/FIX-B3-REPORT.md
R  FIX-B4-REPORT.md -> docs/reports/FIX-B4-REPORT.md
R  FIX-F1-REPORT.md -> docs/reports/FIX-F1-REPORT.md
R  FIX-F2-REPORT.md -> docs/reports/FIX-F2-REPORT.md
R  FIX-M1-REPORT.md -> docs/reports/FIX-M1-REPORT.md
R  FIX-M2-REPORT.md -> docs/reports/FIX-M2-REPORT.md
R  FIX-M3-REPORT.md -> docs/reports/FIX-M3-REPORT.md
R  FIX-M4-REPORT.md -> docs/reports/FIX-M4-REPORT.md
R  FIX-P1-REPORT.md -> docs/reports/FIX-P1-REPORT.md
R  FIX-P2-REPORT.md -> docs/reports/FIX-P2-REPORT.md
R  FIX-W1-REPORT.md -> docs/reports/FIX-W1-REPORT.md
R  FIX-W1B-REPORT.md -> docs/reports/FIX-W1B-REPORT.md
R  FIX-W3-REPORT.md -> docs/reports/FIX-W3-REPORT.md
R  VALIDATION_REPORT.md -> docs/reports/VALIDATION_REPORT.md
R  VECTOR-COMPAT-REPORT.md -> docs/reports/VECTOR-COMPAT-REPORT.md
R  contract.example.json -> examples/contract.example.json
R  cutover.example.json -> examples/cutover.example.json
R  migration.example.json -> examples/migration.example.json
?? docs/reports/README.md
?? docs/reports/FIX-O1-REPORT.md
```

Todo como rename `R` (sin archivos perdidos). Las dos entradas `??` son los archivos
nuevos (`docs/reports/README.md` y este reporte). Las entradas `??` con prefijo
`../` son dirs/archivos ajenos al repo, preexistentes fuera de scope.