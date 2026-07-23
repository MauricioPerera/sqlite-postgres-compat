# FIX-R3-DOCS-REPORT — Corrección de staleness de docs (AUDIT3-C)

Dev efímero sin memoria previa. HEAD `d48eae5` (rama `main`, árbol limpio
antes de empezar). Scope: 3 fixes de docs derivados de
[AUDIT3-C.md](AUDIT3-C.md) (MEDIA #1, BAJA #1, BAJA #2). No se tocó ningún
archivo `.go` ni nada fuera de los 4 docs listados + este reporte. Otros 2
devs trabajan en paralelo sobre `compat/**`; su zona es READ-ONLY aquí.

## Verificación previa (conteo de tests)

Comando pedido:

```
grep -rhE "^func Test[A-Z]" e2e/*.go | grep -v TestMain | wc -l  →  33
```

Coincide con el esperado (33). Desglose por archivo:
- `e2e/cutover_test.go`: 14
- `e2e/suppress_test.go`: 3
- `e2e/system_test.go`: 16

El test intencional fallido confirmado en `e2e/system_test.go:1012`:
`TestSystemClaimsExactCoverageForRequiredFeatureFamilies`. Resultado real en
HEAD: **33 top-level (32 PASS + 1 FAIL intencional)**. Sin discrepancia con la
consigna.

## Fix 1 — MEDIA #1: conteo de tests E2E (28 → 33)

Docs vivas actualizadas de «28 / 27 superadas» a «33 / 32 superadas».

- **README.md:57**: «28 pruebas de nivel superior, 27 superadas» → «33
  pruebas de nivel superior, 32 superadas».
- **docs/TESTING.md:48** (§ Conteo vigente): «tiene 28 pruebas…» → «tiene 33
  pruebas…»; «Hoy son 27 superadas» → «Hoy son 32 superadas».

### VALIDATION_REPORT.md — dejado intacto (decisión fundada)

`docs/reports/VALIDATION_REPORT.md:65` aún dice «27 pruebas superiores superadas
y 1 fallida, sobre 28 pruebas de nivel superior (27 PASS + 1 FAIL…)». **No se
modificó**, conforme al criterio de la consigna: los reportes históricos son
inmutables; README/TESTING son docs vivas.

Razón: el archivo lleva cabecera `Fecha: 2026-07-22` y se lee como una
instantánea histórica de la última validación («Informe de validación
integral»), no como doc viva. Reescribir su conteo alteraría el registro
histórico (diría que en la validación del 2026-07-22 había 33 tests, lo cual
no es cierto: en ese momento había 28). Las docs vivas (README/TESTING) ya
reflejan el conteo vigente correcto, y ambas apuntan a este reporte como
detalle histórico, sin contradecirlo en lo que afirma de su propia fecha. La
consigna ofrecía dos caminos (nota fechada o intacto + justificación); se
elige el segundo, alineado con la inmutabilidad de los reportes históricos.

## Fix 2 — BAJA #1: prefijo `compat-cutover: ` en el ejemplo de stderr

`contracts/migration.contract.example.md` §2 (Verdicts, paso 2, línea 83):

- Antes: `Expected stderr: progress lines `audit: ...`, `capture: ...`,
  `snapshot: ...`, `catch-up: ...`.`
- Ahora: `Expected stderr: progress lines, each prefixed with
  `compat-cutover: `: `compat-cutover: audit: ...`, `compat-cutover: capture:
  ...`, `compat-cutover: snapshot: ...`, `compat-cutover: catch-up: ...`.`

Refleja la salida literal observada (`logf` en `cmd/compat-cutover/main.go`
prefija cada línea con `compat-cutover: `). Idioma y registro del archivo
(inglés, prosa técnica) preservados.

## Fix 3 — BAJA #2: documentar `scripts/check.ps1`

`scripts/check.ps1` (agregado en `c09ee8f`) es el gate de calidad local:
`gofmt -l .` (debe ser vacío), `go vet ./...`, `go test ./... -count=1`.
Uso: antes de commitear. Documentado en ambos lados:

- **README.md:71** (§ Estructura del repo): `scripts/` ahora reza «gate de
  calidad local (check.ps1) y batería integral E2E (test-system.ps1)» (antes
  sólo mencionaba `test-system.ps1`).
- **docs/TESTING.md** (§ Pruebas unitarias y análisis estático): añadido un
  párrafo + bloque `powershell` con `.\scripts\check.ps1`, indicando que exige
  `gofmt -l .` vacío, `go vet ./...` y `go test ./... -count=1`, y que se corre
  antes de commitear. Estilo del archivo (encabezados en español, bloques
  `powershell`) preservado.

## Definición de hecho — verificación

```
$ grep -n "33" README.md docs/TESTING.md
README.md:57:...33 pruebas de nivel superior, 32 superadas...
docs/TESTING.md:54:La batería E2E tiene 33 pruebas de nivel superior...

$ grep -rn "28 pruebas\|27 superadas\|28 top-level\|27 passed" README.md docs/TESTING.md || echo SIN_STALE
SIN_STALE

$ grep -n "compat-cutover: " contracts/migration.contract.example.md
83:- **Expected stderr**: progress lines, each prefixed with `compat-cutover: `: ...

$ grep -n "check.ps1" README.md docs/TESTING.md
README.md:71:scripts/           # gate de calidad local (check.ps1) y batería integral E2E (test-system.ps1)
docs/TESTING.md:15:.\scripts\check.ps1

$ go build ./...
BUILD_OK
```

Todos los greps de presencia y ausencia pasan; `go build ./...` verde (smoke;
no se tocó código).

## Alcance de archivos

Modificados (sólo estos, dentro del permitido):
- `README.md`
- `docs/TESTING.md`
- `contracts/migration.contract.example.md`

Intacto a propósito: `docs/reports/VALIDATION_REPORT.md` (reporte histórico
inmutable; ver Fix 1).

`git status --short`: `M README.md`, `M contracts/migration.contract.example.md`,
`M docs/TESTING.md` (más este reporte nuevo). Ningún `.go` tocado. No se hizo
`git add`/`commit`.