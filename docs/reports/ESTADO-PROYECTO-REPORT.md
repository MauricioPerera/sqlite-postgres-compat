# ESTADO-PROYECTO — Análisis del árbol de trabajo sin commitear

HEAD: `d1eded4` "docs: add second audit round reports"
Fecha del análisis: 2026-07-22
Alcance: READ-ONLY. Único archivo escrito por este análisis: `docs/reports/ESTADO-PROYECTO-REPORT.md`.

Resumen ejecutivo: el diff son **tres fixes de auditoría** (Q1, Q2, Q3) ya documentados en los tres reportes `FIX-Q*-REPORT.md` nuevos. `go build ./...` OK; `go test ./...` verde en **dos corridas forzadas** (`-count=1`). Código + tests + docs alineados y coherentes; sin TODO/FIXME nuevos. **Listo para commitear como batch.**

---

## 1. ¿Qué contiene el diff sin commitear?

`git diff --stat` (15 archivos trackeados modificados, +850 / −38):

```
 AGENTS.md                                   |   8 +-
 cmd/compat-audit/main.go                    |  15 +-
 cmd/compat-copy/main.go                     |  18 +-
 cmd/compat-cutover/main.go                  |  24 +-
 cmd/internal/cliout/cliout.go               |  91 +++++++
 compat/capture.go                           |  31 ++-
 compat/capture_test.go                      |  39 +++
 compat/replicate_test.go                    | 190 +++++++++++++++
 compat/sqlparse.go                          |   8 +-
 compat/sqlparse_test.go                     |  32 +++
 compat/store.go                             |  82 +++++++
 compat/store_test.go                        |  59 +++++
 contracts/migration.contract.example.md     |   6 +-
 docs/USAGE.md                               |  22 +-
 e2e/cutover_test.go                         | 263 +++++++++++++++++++++
 15 files changed, 850 insertions(+), 38 deletions(-)
```

Archivos nuevos no trackeados: `docs/reports/FIX-Q1-REPORT.md`, `FIX-Q2-REPORT.md`, `FIX-Q3-REPORT.md` y `examples/schema.example.json` (este último referenciado por el contrato example).

Los cambios se agrupan en **tres fixes** que corresponden exactamente a los tres reportes `FIX-Q*-REPORT.md`:

### FIX-Q1 — Hex literals como two's-complement con signo (como SQLite)
Archivos: `compat/sqlparse.go` (+8), `compat/sqlparse_test.go` (+32).
- `catalogHexLiteral` parseaba hex con `ParseUint` y emitía el decimal **sin signo**. SQLite evalúa hex como int64 con signo, así que un valor con el bit alto seteado se traducía mal: `0xFFFFFFFFFFFFFFFF` (SQLite `-1`) salía `18446744073709551615`.
- Fix: reinterpretar el `uint64` como `int64` y emitir con `strconv.FormatInt`. Out-of-range (>16 dígitos) sin cambios.
- Tests: 4 casos nuevos al table-driven (`-1`, `INT64_MIN`, `INT64_MAX`, `0x10→16`). Corresponde a un hallazgo de auditoría (catálogo de expresiones).

### FIX-Q2 — Pérdida silenciosa de precisión FLOAT + ConflictError espurio en DECIMAL
Archivos: `compat/capture.go` (+31), `compat/store.go` (+82), y tests `compat/capture_test.go` (+39), `compat/store_test.go` (+59), `compat/replicate_test.go` (+190).
- **Hallazgo 1 (FLOAT):** `captureJSONExpression` journalizaba cada columna no-binaria con `CAST(col AS TEXT)`. En SQLite, `CAST(REAL AS TEXT)` renderiza con ~15 dígitos significativos y **pierde precisión** (no es round-trip del float64). Fix: para `FloatType` en SQLite usar `printf('%!.17g', col)` (17 dígitos = bound de round-trip IEEE-754). Postgres sin cambios (su `float8out` ya emite shortest round-trip).
- **Hallazgo 2 (DECIMAL):** una columna DECIMAL sobre tabla nativa con afinidad NUMERIC guarda fracciones como REAL, así que el `CAST` truncaba el journal mientras el driver del destino reconstruía el float64 completo → `rowsEqual` levantaba `ConflictError` espurio. Fix: para `DecimalType` en SQLite emitir `CASE typeof(col) WHEN 'real' THEN printf('%!.17g', col) ELSE CAST(col AS TEXT) END` (sólo REAL usa printf; TEXT/INTEGER pasan verbatim preservando precisión arbitraria). En `store.go`, `canonicalValue` rama `DecimalType` ahora reconcilia vía `normalizeFloat` **sólo** cuando llega como `float64`/`float32` del driver o como texto float compacto con ≤17 dígitos significativos (`isCompactFloatText`); el resto se preserva byte-for-byte.
- Tests: `TestApplyChangesPreservesHighPrecisionFloat`, `TestApplyChangesDecimalRealStorageNoSpuriousConflict`, `TestApplyChangesPreservesArbitraryPrecisionDecimal` (replicate); `TestCanonicalDecimalReconcilesFloatStorage` (store); `TestCompileCaptureTriggersUsesRoundTripFloatEncodingOnSQLite` (capture).
- Trade-off documentado: `printf('%!.17g')` en modernc.org/sqlite no round-trip para ~100 magnitudes extremas (p. ej. `1e-306`) — pero el `CAST` anterior también era lossy ahí, así que **no es regresión**; hex-encode de bits descartado porque modernc carece de `printf('%a')`.

### FIX-Q3 — `ERR_USAGE` cubre flags desconocidos + `schema_ref` real + rechazar keys desconocidas
Archivos: `cmd/**` (3 CLIs), `cmd/internal/cliout/cliout.go` (+91, nuevo paquete de helpers), `e2e/cutover_test.go` (+263), `AGENTS.md` (+8), `docs/USAGE.md` (+22), `contracts/migration.contract.example.md` (+6), `examples/schema.example.json` (nuevo).
- **BUG 1:** un flag desconocido (`--bogus`) caía al path posicional y fallaba `ERR_CONFIG`/exit 1 en vez de `ERR_USAGE`/exit 2 como prometían los docs. Fix: `cliout.SplitArgs(knownFlags, args)` particiona flags booleanos reconocidos de posicionales; cualquier token con `-` no reconocido → `ERR_USAGE` exit 2. `compat-audit`/`compat-copy`: `SplitArgs(nil,…)`; `compat-cutover`: `SplitArgs([]string{"--dry-run"},…)`.
- **BUG 2:** `schema_ref` estaba documentado como alternativa a `schema` inline pero ningún CLI lo parseaba — la key se dropeaba en silencio y se corría con schema vacío. Fix: `cliout.ResolveSchema(configPath, ref, inline)` exige **exactamente uno** de `schema` (con ≥1 tabla) o `schema_ref` (no vacío); resuelve la ruta **relativa al archivo de config**; ilegible/JSON inválido → `ERR_CONFIG`. Ambos CLIs con schema (`compat-cutover`, `compat-copy`) ganan campo `SchemaRef` y llaman a `ResolveSchema`.
- **Guard de raíz:** toda config se decodifica con `json.Decoder.DisallowUnknownFields()` (`cliout.DecodeFileStrict`) → key desconocida = `ERR_CONFIG` explícito, no drop silencioso.
- Tests e2e nuevos: `TestCLIRejectsUnknownFlagAsUsage` (cutover/copy/audit → exit 2 + `ERR_USAGE`), `TestCutoverRejectsUnknownConfigKey`, `TestCutoverRejectsSchemaAndSchemaRef`, `TestCutoverRejectsMissingSchemaAndSchemaRef`, `TestCutoverWithSchemaRefSucceeds` (cutover real contra PostgreSQL → `status=ready`). Nota: el helper `runBuiltCLI` buildea el binario (no `go run`, que en Windows colapsa exit≠0 a 1) para asertar el exit-2 verdadero.
- Docs alineadas: `AGENTS.md` §8/§8.1, `docs/USAGE.md` (tabla ERR_USAGE/ERR_CONFIG + bloque `schema_ref`), `contracts/migration.contract.example.md` (`schema_ref` ahora apunta a `examples/schema.example.json` — un `compat.Schema` bare, no un config completo que rompería `DisallowUnknownFields`).

### Correspondencia diff ↔ reportes
| Reporte | Tema | Archivos del diff |
|---|---|---|
| FIX-Q1 | hex con signo | `compat/sqlparse.go`, `compat/sqlparse_test.go` |
| FIX-Q2 | precisión FLOAT/DECIMAL | `compat/capture.go`, `compat/store.go` + 3 archivos de test |
| FIX-Q3 | ERR_USAGE / schema_ref / DisallowUnknownFields | `cmd/**`, `cmd/internal/cliout/cliout.go`, `e2e/cutover_test.go`, `AGENTS.md`, `docs/USAGE.md`, `contracts/…`, `examples/schema.example.json` |

Cada reporte declara su perímetro y **coincide** con los archivos efectivamente modificados. `compat/` no fue tocado por Q3; `cmd/` no fue tocado por Q1/Q2 — los tres fixes son disjuntos en archivos.

---

## 2. ¿El árbol compila y la suite pasa?

Entorno: `go1.26.4 windows/amd64`.

### `go build ./...`
```
=====BUILD=====
BUILD_EXIT=0
```
Build OK (exit 0).

### `go test ./...` — corrida 1 (forzada, `-count=1`)
```
=====TEST RUN 1 (-count=1)=====
?   	example.com/sqlite-postgres-compat/cmd/compat-audit	[no test files]
?   	example.com/sqlite-postgres-compat/cmd/compat-copy	[no test files]
?   	example.com/sqlite-postgres-compat/cmd/compat-cutover	[no test files]
?   	example.com/sqlite-postgres-compat/cmd/internal/cliout	[no test files]
ok  	example.com/sqlite-postgres-compat/compat	2.363s
TEST1_EXIT=0
```

### `go test ./...` — corrida 2 (forzada, `-count=1`)
```
=====TEST RUN 2 (-count=1)=====
?   	example.com/sqlite-postgres-compat/cmd/compat-audit	[no test files]
?   	example.com/sqlite-postgres-compat/cmd/compat-copy	[no test files]
?   	example.com/sqlite-postgres-compat/cmd/compat-cutover	[no test files]
?   	example.com/sqlite-postgres-compat/cmd/internal/cliout	[no test files]
ok  	example.com/sqlite-postgres-compat/compat	2.253s
TEST2_EXIT=0
```

**Suite unitaria verde en las dos corridas, sin flaky detectado** (tiempos 2.36s / 2.25s, resultados idénticos).

Nota importante sobre cobertura: la primer corrida con `go test ./...` a secas devolvió `(cached)`; se re-corrió con `-count=1` para forzar ejecución real y detectar flaky. **La suite e2e (`e2e/cutover_test.go`, +263 líneas de este diff) NO fue ejecutada aquí**: requiere `-tags=e2e` y `COMPAT_POSTGRES_DSN` contra un PostgreSQL real. `go test ./...` no la levanta (build tag). El reporte FIX-Q3 documenta que la e2e corrió verde (32 PASS + 1 FAIL intencional documentado) en el entorno donde se generó. **Esa verificación no fue reproducida en este análisis** — ver §4 pendientes.

`cmd/**` y `cmd/internal/cliout` no tienen archivos de test propios (`[no test files]`); su cobertura vive en `e2e/`.

---

## 3. ¿Los cambios son completos y coherentes (código + tests + docs)?

**Sí.** Verificado punto por punto:

- **Código ↔ tests:** cada cambio de comportamiento tiene test que lo cubre.
  - Q1: 4 casos hex con signo en el table-driven existente.
  - Q2: 3 tests de replicación + 1 unit de `canonicalValue` + 1 test de SQL de trigger.
  - Q3: 5 tests e2e (flag desconocido ×3 CLIs, key desconocida, ambos schema, ninguno, schema_ref feliz).
- **Código ↔ docs:** `AGENTS.md` §8/§8.1 y `docs/USAGE.md` actualizan la tabla de códigos (`ERR_USAGE` ahora cubre flags inesperados; `ERR_CONFIG` documenta `DisallowUnknownFields` + la restricción `schema`/`schema_ref`) y las líneas de config de `compat-copy`/`compat-cutover` (`schema | schema_ref`, exactly-one, resolución relativa al config). El grep `schema_ref` mostró código y docs alineados (referenciado en FIX-Q3 §2.5).
- **Contrato ↔ ejemplo:** `contracts/migration.contract.example.md` ahora apunta `schema_ref` a `examples/schema.example.json`, que es un `compat.Schema` bare válido (`entries` con `id`+`title`, PK) — coherente con `DisallowUnknownFields` al decodear como `Schema` (antes apuntaba a `examples/migration.example.json`, un config completo que habría fallado).
- **Sin TODO/FIXME/XXX/HACK nuevos** en el diff (grep confirmó: ninguno).
- **Sin firmaturas públicas rotas:** Q1 no cambia firma de `catalogHexLiteral`; Q2 agrega helpers internos (`float64Value`, `isCompactFloatText`, `significantDigits`); Q3 agrega helpers exportados en `cliout` y un campo `SchemaRef` a structs de config privados de los CLIs.
- **Build verde** confirma que no hay referencias rotas entre paquetes (los 3 CLIs usan los nuevos helpers `cliout.SplitArgs`/`DecodeFileStrict`/`ResolveSchema`).

Observaciones menores (no bloqueantes):
- `e2e/cutover_test.go` termina sin newline final (`\ No newline at end of file`). Preexistía así; el diff lo preserva. Cosmético.
- Q3 cambia la semántica de `--dry-run`: ahora se acepta en cualquier posición de args (antes sólo `args[0]`). Es un superconjunto del comportamiento previo; los tests existentes pasan `--dry-run` antes del config, así que nada dependía del match posicional-only. Documentado en FIX-Q3 §3.
- Los warnings `LF will be replaced by CRLF` de git son artefacto de Windows (autocrlf), no del diff.

Nada a medias. Los tres fixes están cerrados, cada uno con su reporte de Definition-of-Done y su suite.

---

## 4. Recomendación: ¿listo para commitear como batch?

**Sí, el diff está listo para commitear como batch.** Build OK, suite unitaria verde 2× sin flaky, código+tests+docs coherentes, sin TODO ni referencias rotas, perímetros disjuntos entre los tres fixes.

Sugerencia de commits: como los tres fixes son disjuntos en archivos y temas, lo más limpio son **tres commits** (uno por Q), pero un batch único también es aceptable dado que forman la misma ronda de auditoría y comparten los tres reportes `FIX-Q*-REPORT.md`.

Opción A — tres commits (recomendado, historial más legible):
```
fix(sqlparse): reinterpret hex literals as signed int64 like SQLite

Closes AUDIT2-A hex-literal finding (FIX-Q1): catalogHexLiteral emitted
the unsigned decimal, so a high-bit-set hex literal was silently
mistranslated (0xFFFFFFFFFFFFFFFF -> 18446744073709551615 instead of -1).
Parse as uint64, reinterpret as int64, emit with FormatInt. Out-of-range
behavior unchanged. Adds 4 signed-hex table-driven cases.

Files: compat/sqlparse.go, compat/sqlparse_test.go
```
```
fix(capture,store): lossless float capture + reconcile DECIMAL float storage

Closes AUDIT2-B Hallazgos 1 & 2 (FIX-Q2). SQLite CAST(REAL AS TEXT) loses
~15-sig-digit precision and truncated REAL-stored DECIMALs, raising a
spurious ConflictError. FLOAT and REAL-stored DECIMAL now capture via
printf('%!.17g') (typeof-gated for DECIMAL); canonicalValue reconciles
float64/compact-float-text through normalizeFloat while preserving
arbitrary-precision (18+ sig digits) and pure-integer decimals verbatim.
Postgres unchanged. Adds replicate + canonicalValue + trigger-SQL tests.

Files: compat/capture.go, compat/store.go, compat/{capture,store,replicate}_test.go
```
```
feat(cli): ERR_USAGE for unknown flags, real schema_ref, reject unknown keys

Closes AUDIT2-C (FIX-Q3). Unknown flags (--bogus) now ERR_USAGE/exit 2 via
cliout.SplitArgs instead of falling through to ERR_CONFIG/exit 1. schema_ref
is now parsed (resolved relative to the config file) with exactly-one-of
schema|schema_ref enforced. All configs decode with DisallowUnknownFields so
an unknown key is ERR_CONFIG, not a silent drop. Adds cliout helpers, SchemaRef
fields, 5 e2e tests, and aligns AGENTS.md/USAGE.md/contract example +
examples/schema.example.json.

Files: cmd/**, cmd/internal/cliout/cliout.go, e2e/cutover_test.go,
       AGENTS.md, docs/USAGE.md, contracts/migration.contract.example.md,
       examples/schema.example.json
```
Plus un commit (o parte del de Q3) para los tres reportes:
```
docs: add FIX-Q1/Q2/Q3 audit fix reports
```

Opción B — batch único:
```
fix: close AUDIT2 round — signed hex, lossless float/decimal, CLI usage/schema_ref

Three disjoint audit fixes (FIX-Q1/Q2/Q3): signed two's-complement hex
literals; lossless FLOAT capture and DECIMAL float-storage reconciliation
via printf('%!.17g') with arbitrary-precision preservation; ERR_USAGE on
unknown flags, real schema_ref support, and DisallowUnknownFields config
decoding. Build + unit suite green; e2e suite documented green in reports.
```

### Qué quedaría pendiente después
1. **Correr la suite e2e** (`go test -tags=e2e ./e2e -count=1 -timeout 600s` con `COMPAT_POSTGRES_DSN` contra un PostgreSQL 17 real). Este análisis **no la ejecutó** (sin DSN garantizado en este entorno). Es la única verificación no reproducida; los +263 líneas de `e2e/cutover_test.go` dependen de ella. Hasta no correrla, la cobertura de Q3 (exit-2, `schema_ref` real, `DisallowUnknownFields`) está validada sólo por el reporte FIX-Q3, no por este análisis.
2. `go vet ./...` no fue corrido aquí (los reportes FIX-Q* lo declaran verde); conviene incluirlo en el gate de commit.
3. Decidir Opción A (3 commits) vs B (1 batch). Recomendado A por legibilidad del historial y porque los perímetros son disjuntos.
4. Los archivos `??` fuera del repo (`../.chrome-pdf-profile/`, `../lazyssh/`, etc.) son ruido del directorio padre y **no** deben entrar en el commit — stagear sólo lo de este diff.
5. Cosmético opcional: agregar newline final a `e2e/cutover_test.go`.

---

## Verificación de no-modificación
`git status --short` final muestra exactamente los mismos 15 archivos modificados + los 4 no trackeados (`FIX-Q{1,2,3}-REPORT.md`, `examples/schema.example.json`) que al inicio, más este nuevo `docs/reports/ESTADO-PROYECTO-REPORT.md`. Ningún otro archivo fue tocado; no se ejecutó `git add/commit/checkout/stash/restore`.