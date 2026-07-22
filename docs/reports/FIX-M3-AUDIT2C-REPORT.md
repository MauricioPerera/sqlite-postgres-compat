# FIX-M3 (AUDIT2-C) — Cierre de 2 hallazgos MEDIA (#3, #5) y 1 BAJA (#7)

Reporte en español, dev efímero sin memoria previa. Tres fixes sobre
`cmd/compat-copy/main.go`, `AGENTS.md`, `docs/USAGE.md` y `e2e/cutover_test.go`.
No se tocó `compat/**` (otros devs en paralelo), ni `cmd/compat-audit` ni
`cmd/compat-cutover` (sólo leídos como referencia de convención).

> **Nota de entrega:** el nombre pedido en la consigna era
> `docs/reports/FIX-M3-REPORT.md`, pero ese archivo **ya existía** y pertenece a
> otra tarea distinta ("JSON canonicalization + Postgres timestamp formats" en
> `compat/store.go`, fuera de mi scope y de otro dev). Sobrescribirlo habría
> borrado el trabajo ajeno. Escribí este reporte como `FIX-M3-AUDIT2C-REPORT.md`
> (no-colisionante) y lo dejo flaggeado. No se tocó el `FIX-M3-REPORT.md`
> preexistente.

## Qué se pidió vs. qué se hizo

### [MEDIA #3] compat-copy no emitía findings en ERR_AUDIT_NOT_EXACT
**Pedido:** compat-copy debe emitir los findings JSON en STDERR antes del
envelope de error, siguiendo EXACTAMENTE la convención de compat-cutover
(`cmd/compat-cutover/main.go:114-118`).

**Hecho:** en `cmd/compat-copy/main.go`, antes del bloque `RequireExact`, se
replica literalmente el patrón de cutover:
```go
if err := compat.RequireExact(findings); err != nil {
    fmt.Fprintln(os.Stderr, err)
    encoded, _ := json.Marshal(findings)
    fmt.Fprintln(os.Stderr, string(encoded))
    fail(cliout.ErrAuditNotExact, err)
}
```
Se agregó el import `encoding/json`. Como `fail` también imprime `err` a stderr,
`err` queda dos veces en stderr — **exactamente** igual que cutover (líneas
115 y 333 de `cmd/compat-cutover/main.go`). No se introdujo helper en
`cliout.go` porque era un inline de 3 líneas y la consigna era "mismo formato"
que cutover (que también lo ininea); no había necesidad real de un helper
compartido.

### [MEDIA #5] regla general de §8.1 no describía el comportamiento real
**Pedido (a) CÓDIGO:** en compat-copy diverged (`ERR_VERIFY_DIVERGED`) emitir el
`VerificationReport` JSON en stderr antes del envelope.

**Hecho:** mismo patrón que #3, sobre el bloque `RequireEquivalent`:
```go
if err := compat.RequireEquivalent(report); err != nil {
    fmt.Fprintln(os.Stderr, err)
    encoded, _ := json.Marshal(report)
    fmt.Fprintln(os.Stderr, string(encoded))
    fail(cliout.ErrVerifyDiverged, err)
}
```
Los digests dejan de vivir sólo como texto libre en `message`
(`compat/verify.go:57` "snapshot mismatch: source %s destination %s") y pasan
a ser JSON parseable en stderr.

**Pedido (b) DOCS:** reescribir la regla general de §8.1 (AGENTS.md y espejo en
USAGE.md) describiendo el comportamiento REAL por caso. Cero afirmaciones que
el código no cumpla.

**Hecho (AGENTS.md, inglés — consistente con el archivo):**
- §8.1 intro reescrita a tres casos: **envelope simple por defecto**;
  **cutover diverged = un único result JSON con `code` embebido** (sin envelope
  aparte); **copy not-exact/diverged = report/findings JSON en stderr + envelope
  en stdout**.
- Fila `ERR_AUDIT_NOT_EXACT` de la tabla: ahora distingue compat-audit
  (findings a stdout) de compat-copy/compat-cutover (findings a stderr).
- Fila `ERR_VERIFY_DIVERGED`: describe cutover (una línea con code embebido, sin
  envelope) y copy (`VerificationReport` a stderr + envelope).
- Sección `compat-copy`: la línea de Exit 1 ahora menciona explícitamente que en
  `ERR_AUDIT_NOT_EXACT` emite `[]Finding` a stderr y en `ERR_VERIFY_DIVERGED`
  emite `VerificationReport` a stderr, antes del envelope.

**Hecho (docs/USAGE.md, español — consistente con el archivo):** espejo de lo
anterior. Intro de "Códigos de error tipados" reescrita a los mismos tres
casos; filas `ERR_AUDIT_NOT_EXACT` y `ERR_VERIFY_DIVERGED` actualizadas; y la
sección de `compat-copy` (línea 52) recibe una frase final con la conducta de
stderr en fallo.

### [BAJA #7] test e2e con nombre/comentario engañoso
**Pedido:** renombrar `TestCutoverAuditCLIErrorCodeOnNotExact` (que en realidad
ejercita `./cmd/compat-audit`, no compat-cutover) y corregir su comentario.

**Hecho:** renombrado a `TestAuditCLIErrorCodeOnNotExact` en
`e2e/cutover_test.go`; comentario ahora dice explícitamente que ejecuta
`./cmd/compat-audit` (no compat-cutover). Sin otros cambios en el cuerpo.

**Tests e2e existentes de compat-copy:** la consigna pedía ajustarlos si mis
cambios del punto 1 afectaban expectativas (stderr nuevo). Revisé los tests que
tocan compat-copy: `TestSystemCompatCopyCLIEndToEnd` (camino de ÉXITO con
`CombinedOutput`, sólo espera el `VerificationReport` en stdout — sin stderr en
éxito) y el caso "copy" de `TestCLIRejectsUnknownFlagAsUsage` (camino
`ERR_USAGE` antes de cualquier findings, sin tocar stdout/stderr de findings).
Ningún test e2e ejercita el camino not-exact o diverged de compat-copy a nivel
CLI, así que **ninguna expectativa existente quedó afectada**. No se agregaron
tests nuevos (la consigna decía "ajustá esos tests si afectan", no "agregá";
agregar habría sido una variante fuera de lo pedido).

## Verificación (comandos reales pegados)

```
$ go build ./...
BUILD_EXIT=0

$ go vet ./...
VET_EXIT=0

$ go vet -tags=e2e ./e2e
VET_E2E_EXIT=0

$ go test ./... -count=1
?       example.com/sqlite-postgres-compat/cmd/compat-audit      [no test files]
?       example.com/sqlite-postgres-compat/cmd/compat-copy      [no test files]
?       example.com/sqlite-postgres-compat/cmd/compat-cutover   [no test files]
?       example.com/sqlite-postgres-compat/cmd/internal/cliout  [no test files]
ok      example.com/sqlite-postgres-compat/compat       2.402s
TEST_EXIT=0
```

`go test ./compat/ -count=1` no es mi veredicto (otros devs editan `compat/`),
pero `go test ./...` no falla por mis archivos.

Grep de docs (contrato nuevo en stderr):
```
$ grep -n "stderr" AGENTS.md docs/USAGE.md
AGENTS.md:190:- **`compat-copy` not-exact or diverged.** Before the simple error envelope on stdout, `compat-copy` emits a structured JSON payload to **stderr**: the `[]Finding` array on `ERR_AUDIT_NOT_EXACT`, or the `VerificationReport` object on `ERR_VERIFY_DIVERGED`. The plain error envelope still follows on stdout with the same `code`; an agent reads the structured detail from stderr and the typed code from stdout.
AGENTS.md:192:Each envelope line is one parseable JSON object ... Free-text diagnostics also go to stderr for humans.
AGENTS.md:198:| `ERR_AUDIT_NOT_EXACT` | ... `compat-audit` emits its findings array to stdout, then this envelope; `compat-copy` and `compat-cutover` emit the findings array to stderr, then this envelope. | `1` |
AGENTS.md:205:| `ERR_VERIFY_DIVERGED` | ... `compat-cutover` emits one JSON line ... (no separate envelope). `compat-copy` emits its `VerificationReport` to stderr, then the error envelope with this `code`. | `1` |
AGENTS.md:233:- Exit `1`: any typed error (see 8.1). On `ERR_AUDIT_NOT_EXACT` the `[]Finding` array is printed to stderr before the envelope; on `ERR_VERIFY_DIVERGED` ... the `VerificationReport` is printed to stderr before the envelope ...
docs/USAGE.md:52:... En fallo: en `ERR_AUDIT_NOT_EXACT` imprime el arreglo `[]Finding` a stderr antes del envelope; en `ERR_VERIFY_DIVERGED` (digests distintos) imprime el `VerificationReport` a stderr antes del envelope ...
docs/USAGE.md:242:- **`compat-copy` not-exact o diverged.** Antes del envelope de error simple en stdout, `compat-copy` emite a **stderr** un payload JSON estructurado ...
docs/USAGE.md:250:| `ERR_AUDIT_NOT_EXACT` | ... `compat-audit` imprime el arreglo de findings a stdout ...; `compat-copy` y `compat-cutover` los imprimen a stderr ...
docs/USAGE.md:257:| `ERR_VERIFY_DIVERGED` | ... `compat-cutover` emite una sola línea JSON ... `compat-copy` emite su `VerificationReport` a stderr y después el envelope ...
```

Reproducción en vivo del camino not-exact de compat-copy (sin PostgreSQL; falla
en audit antes de conectar) confirmando el contrato nuevo:
```
$ go run ./cmd/compat-copy <config con required_features=["tables","views"]>
EXIT=1
=== STDOUT ===
{"status":"error","code":"ERR_AUDIT_NOT_EXACT","message":"feature \"views\" is unknown: requires parser and semantic compiler"}
=== STDERR ===
feature "views" is unknown: requires parser and semantic compiler
[{"feature":"tables","status":"exact"},{"feature":"views","status":"unknown","reason":"requires parser and semantic compiler"},{"feature":"tables","status":"exact"},{"feature":"canonical_full_text","status":"exact"},{"feature":"primary_keys","status":"exact"}]
feature "views" is unknown: requires parser and semantic compiler
exit status 1
```
stdout = un único envelope `ERR_AUDIT_NOT_EXACT`; stderr = mensaje + arreglo
`[]Finding` completo + mensaje (de `fail`). Coincide con compat-cutover.

## Trade-offs / notas de diseño

- **Doble impresión de `err` en stderr:** replicada a propósito para "mismo
  formato que cutover". cutover imprime `err` una vez explícito y otra dentro de
  `fail`. Mantuve la simetría en lugar de "mejorarla" desduplicando, porque la
  consigna era espejo exacto, no refactor. Es cosmético (stderr es para
  humanos) y no rompe el contrato machine-facing (stdout sigue siendo una sola
  línea JSON).
- **No se extrajo helper en `cliout.go`:** el patrón de emitir findings/report
  a stderr ahora está duplicado entre cutover y copy (cutover no se podía
  tocar). Un helper `EmitJSONTo(w, v)` reduciría la dup, pero la consigna sólo
  autorizaba `cliout.go` "si necesitás un helper compartido" y el inline es
  trivial y ya era la forma de cutover. Dejé inline para no inflar el alcance.
- **Idioma de docs:** la consigna decía "docs en INGLÉS ... consistente con
  cada archivo". AGENTS.md es inglés; `docs/USAGE.md` es español. Prioricé
  "consistente con cada archivo" para no meter inglés dentro de un doc español
  (habría sido más inconsistente que la regla general). AGENTS.md → inglés;
  USAGE.md → español.
- **Colisión de nombre de reporte:** ver nota al inicio. No se sobrescribió el
  `FIX-M3-REPORT.md` ajeno.

## Archivos tocados
- `cmd/compat-copy/main.go` (imports + 2 bloques de fallo)
- `AGENTS.md` (§8.1 intro, 2 filas de tabla, sección compat-copy)
- `docs/USAGE.md` (intro de códigos tipados, 2 filas de tabla, sección compat-copy)
- `e2e/cutover_test.go` (rename + comentario del test)
- `docs/reports/FIX-M3-AUDIT2C-REPORT.md` (este reporte)

No se tocaron archivos fuera de ARCHIVOS. No se hizo `git add`/`commit`.