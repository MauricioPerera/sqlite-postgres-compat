# AUDIT4-C — Ronda 4, CONFIRMACIÓN DE CIERRE (auditor C: CLIs / docs / contrato)

Auditor efímero sin memoria previa. HEAD sobre el que se verifica:
`4dba30d` (rama `main`). Scope: los 3 CLIs (`compat-audit`, `compat-copy`,
`compat-cutover`), `cmd/internal/cliout`, contrato machine-facing en
`AGENTS.md` §8/§8.1 + `docs/USAGE.md`, y coherencia docs↔código de
`README.md` / `docs/TESTING.md` / `contracts/migration.contract.example.md`
/ `examples/*.json`. El núcleo SQL (`compat/`) y el runtime los cubren los
auditores A y B — no se duplica; `compat/**` es READ-ONLY aquí (sólo grep
lectivo para confirmar símbolos citados).

## Metodología

- Binarios buildeados con `go build -o /tmp/audit4/compat-{audit,copy,cutover}.exe`
  (no `go run`): en Windows `go run` colapsa exit≠0 a 1, así que los exit codes
  se midieron sobre el binario real. BUILD OK exit=0.
- Cada path ejecutado capturando **stdout y stderr por separado** y el exit code
  real. Configs de prueba en `/tmp/audit4/...` (fuera del repo). Los runs que
  conectan SQLite se ejecutaron desde un cwd temporal para no ensuciar el repo.
- `compat/` no se auditó línea por línea (scope A/B); sólo se confirmaron los
  símbolos del contrato (p.ej. `compileType` en `ddl.go:182`, `Schema.Validate`
  en `schema.go:213`).
- Lo que exige PostgreSQL vivo: **NO VERIFICADO** (sin PG en este entorno).
- `gofmt -l` sobre `.go` trackeados = 0; `go vet ./...` exit 0; conteo e2e = 33.
- `git status` final esperado: sólo `docs/reports/AUDIT4-C.md` (mío). Véase
  “Observación ambiental” para el archivo `compat/zz_audit4_test.go` (no mío).

---

## PARTE 1 — Confirmación de cierre (hallazgos previos de mi scope)

Para cada hallazgo de AUDIT2-C y AUDIT3-C en scope CLIs/docs/contrato, se
re-verificó sobre HEAD con la misma metodología. Salidas reales pegadas.

### Tabla de confirmación

| # | Hallazgo previo (ronda) | Verificación sobre HEAD (comando + salida + exit reales) | Estado |
|---|---|---|---|
| AUDIT2-C #1 (ALTA) | `ERR_USAGE` no cubría “unexpected flag”; el flag caía al path posicional | `compat-audit --bogus cfg` → exit **2**, stdout `{"status":"error","code":"ERR_USAGE","message":"compat-audit: unexpected flag \"--bogus\""}` (A3). `compat-audit -x foo` → exit 2 ídem (A4). `compat-copy --bogus …` → exit 2 ERR_USAGE (C1). `compat-cutover --bogus …` y `--dry-run --bogus …` → exit 2 ERR_USAGE (U1/U2). Implementado en `cliout.SplitArgs`/`ParseArgsStrict` (`cliout.go:134-179`): todo token con prefijo `-` que no es flag reconocido → `ERR_USAGE` exit 2, nunca posicional. | **CERRADO** |
| AUDIT2-C #2 (ALTA) | `schema_ref` documentado pero no implementado; se dropeaba en silencio y corría sin schema | `schema_ref` real en `cliout.ResolveSchema` (`cliout.go:224-237`) + campo `schema_ref` en `migrationConfig`/`cutoverConfig`. **Exactly-one-of**: ambos → exit 1 `ERR_CONFIG` “config must specify exactly one of schema or schema_ref, not both” (U5); ninguno → exit 1 `ERR_CONFIG` “…exactly one of schema or schema_ref” (U6). **DisallowUnknownFields** en config: key desconocida `weirdkey` → exit 1 `ERR_CONFIG` “json: unknown field” (U7); en el archivo `schema_ref`: `refunknown.json` con `boguskey` → exit 1 `ERR_CONFIG` “schema_ref \”refunknown.json\": json: unknown field” (U10). **Resolución relativa al config** (no al cwd): config en `/tmp/audit4/ctroot/cutover.json` con `schema_ref:"examples/schema.example.json"`, corrido desde el raíz del repo (cwd ≠ dir del config), resuelve el `examples/` junto al config, audit exact, llega a `ERR_CONNECT_DESTINATION` (ct2). **Ilegible/JSON inválido**: `missing.json` → ERR_CONFIG (U8); `broken.json` → ERR_CONFIG “invalid character” (U9). | **CERRADO** |
| AUDIT2-C #3 (MEDIA) | `compat-copy` no emitía findings en `ERR_AUDIT_NOT_EXACT` | Fix M3. `compat-copy` con `required_features=["tables","views"]`: stdout = envelope `{"status":"error","code":"ERR_AUDIT_NOT_EXACT","message":"feature \"views\" is unknown..."}`; **stderr** = `<err>` + `[{"feature":"tables","status":"exact"},{"feature":"views","status":"unknown",...},...]` + `<err>`; exit 1 (C4). Estructura idéntica a `compat-cutover` (U4): mismo patrón `<err>\n<findings>\n<err>` en stderr y envelope en stdout. Código: `cmd/compat-copy/main.go:45-54`. | **CERRADO** |
| AUDIT2-C #4 (MEDIA) | Type family no soportada no la atrapa `Schema.Validate`; aparece tarde como `ERR_SNAPSHOT` | **Fuera de mi scope primario** (raíz en `compat/schema.go`/`ddl.go` = núcleo SQL, auditor A). Verificado el lado **doc/contrato**: `AGENTS.md` §1:29 dice «Any other family is rejected by `compileType` with `type family %q is not supported by %s`» — **exacto**: el rechazo vive en `compat/ddl.go:182` (`compileType`), NO en `Schema.Validate` (`schema.go:213` sólo chequea `Family==""` y la dimensión de `vector`). El doc atribuye correctamente el rechazo a `compileType`, así que **no hay mismatch doc↔código en mi scope**. El comportamiento persiste en la capa `compat/` (un typo de `family` pasa `Validate` y surfacea en `ImportSnapshot`→`ERR_SNAPSHOT`); su cierre corresponde al auditor A. | **PERSISTE (compat/, scope A)**; doc coherente |
| AUDIT2-C #5 (MEDIA) | Regla general de §8.1 (“sobre cualquier result JSON”) no valía para cutover diverged (1 sola línea con `code` embebido, sin envelope) ni para copy diverged (sólo envelope, sin report) | Fix M3 (docs reescritos a 3 casos). `AGENTS.md` §8.1 (181-205) y `docs/USAGE.md` (231-257) ahora describen los 3 casos reales: (a) envelope simple por defecto; (b) **cutover diverged** = una sola línea JSON en stdout, el `cutoverReport` con `code` embebido, **sin** envelope aparte — coincide con `cmd/compat-cutover/main.go:184-195` (`EmitJSON(out)` + `os.Exit(1)`, sin `Die`); (c) **copy not-exact/diverged** = payload JSON estructurado a **stderr** + envelope en stdout — coincide con `cmd/compat-copy/main.go:45-54` y `89-98`. Not-exact verificado en vivo (C4/U4); diverged estructural (emisión runtime necesita PG → NO VERIFICADO). | **CERRADO** (docs); diverged runtime NO VERIFICADO |
| AUDIT2-C #6 (BAJA) | Resultado negativo: sin leak de credenciales | Re-confirmado. DSN destino `postgres://user:password@localhost:5432/database` → mensaje de error `failed to connect to \`user=user database=database\`: ...` (ex1/ex2): **la password no aparece** (pgx la redacta). DSN SQLite sin password. Ningún path a stdout/stderr imprime el DSN crudo. | **CERRADO** (negativo se mantiene) |
| AUDIT2-C #7 (BAJA) | Test e2e con nombre/comentario engañoso (`TestCutoverAuditCLIErrorCodeOnNotExact` ejercitaba `compat-audit`) | Fix (rename `c51b49a`). `grep` en `e2e/`: sólo existe `TestAuditCLIErrorCodeOnNotExact` (`e2e/cutover_test.go:537`, comentario “verifies that compat-audit emits a…”); el viejo `TestCutoverAuditCLIErrorCodeOnNotExact` **ausente**. | **CERRADO** |
| AUDIT3-C MEDIA #1 | Conteo e2e documentado stale (28 vs 33 real) | Fix FIX-R3. `README.md:57` reza «33 pruebas de nivel superior, 32 superadas y 1 fallida»; `docs/TESTING.md:54` reza «tiene 33 pruebas… Hoy son 32 superadas y 1 fallida». Conteo real: `grep -rhE "^func Test[A-Z]" e2e/*.go \| grep -v TestMain \| wc -l` = **33** (cutover_test 14 + suppress_test 3 + system_test 16). `grep` de “28 pruebas/27 superadas” en README/TESTING → SIN_STALE. | **CERRADO** (ver BAJA nueva #1: el reporte histórico enlazado sigue en 28) |
| AUDIT3-C BAJA #1 | Prefijo `compat-cutover:` omitido en el ejemplo de stderr del contrato | Fix FIX-R3. `contracts/migration.contract.example.md:83` ahora: «Expected stderr: progress lines, each prefixed with `compat-cutover: `: `compat-cutover: audit: ...`, …». Salida real observada (ct2, ex2): `compat-cutover: audit: exact coverage for 8 required features` / `…for 3 required features` — prefijo presente, `logf` (`cmd/compat-cutover/main.go:320-322`). | **CERRADO** |
| AUDIT3-C BAJA #2 | `scripts/check.ps1` nuevo sin documentar | Fix FIX-R3. `README.md:71` reza «gate de calidad local (check.ps1) y batería integral E2E (test-system.ps1)»; `docs/TESTING.md:12-16` documenta `.\scripts\check.ps1` (bloque powershell) como gate que exige `gofmt -l .` vacío, `go vet ./...` y `go test ./... -count=1`. | **CERRADO** |

**Resumen Parte 1:** 9 de 10 hallazgos previos de mi scope **CERRADOS** sobre
HEAD con evidencia ejecutada; 1 **PERSISTE** pero fuera de mi scope primario
(AUDIT2-C #4, raíz en `compat/` núcleo SQL — auditor A), con el lado
doc/contrato verificado **coherente** (el doc atribuye el rechazo de family a
`compileType`, que es exactamente lo que hace el código).

---

## PARTE 2 — Barrido final de coherencia

Con todos los fixes aplicados, se buscó cualquier afirmación en `AGENTS.md`,
`docs/USAGE.md`, `docs/TESTING.md`, `README.md` o
`contracts/migration.contract.example.md` que el código actual no cumpla, en
particular las zonas tocadas por fixes recientes (docs de findings/stderr,
tabla de códigos de error, ejemplos ejecutables — se corrió el contrato de
ejemplo con `compat-audit` real y los `examples/*` con `compat-copy`/`--dry-run`).
Y el inverso: comportamientos nuevos sin documentar.

### Hallazgos nuevos

#### BAJA #1 (nueva) — README/TESTING enlazan a VALIDATION_REPORT.md como fuente del conteo vigente, pero ese reporte sigue en 28/27/1

**Docs:** `README.md:57` afirma «33 pruebas de nivel superior, 32 superadas y
1 fallida … Detalle completo en [VALIDATION_REPORT.md]». `docs/TESTING.md:56`
afirma «El conteo vigente y las familias restantes se registran en
VALIDATION_REPORT.md». Ambas frases presentan a `VALIDATION_REPORT.md` como la
fuente del **conteo vigente** (33/32).

**Código/report enlazado:** `docs/reports/VALIDATION_REPORT.md:65` sigue
diciendo «27 pruebas superiores superadas y 1 fallida, sobre 28 pruebas de
nivel superior (27 PASS + 1 FAIL intencional…)». El conteo real (HEAD) es 33
(32 PASS + 1 FAIL intencional).

**Naturaleza:** NO es un mismatch doc↔código (el código tiene 33, y README/TESTING
lo dicen bien). Es una **incoherencia doc↔doc**: las docs vivas (33/32) apuntan
a un reporte (28/27/1) que las contradice, presentándolo como “detalle completo
/ conteo vigente” sin aclarar que es una **instantánea histórica fechada
(2026-07-22)**. Un agente que siga el enlace esperando el detalle de los 33
encuentra 28.

**Contexto (no es un descuido):** FIX-R3 lo dejó intacto a propósito, con
justificación: los reportes históricos son inmutables (en esa fecha había 28).
Es un trade-off **documentado y deliberado**, no una regresión. Se flaggea como
residual de Ronda 4 para que sea visible: la tensión es entre “inmutabilidad del
reporte histórico” y “las docs vivas lo citan como fuente vigente”.

**Severidad:** BAJA (incoherencia doc↔doc; no induce a una acción incorrecta
sobre el código; el conteo vigente en las docs vivas es correcto).

**Resolución posible (no aplicada — READ-ONLY):** una nota fechada en
README/TESTING («VALIDATION_REPORT refleja la validación del 2026-07-22 con
28 tests; el conteo vigente es el de arriba»), o actualizar el reporte. Elección
del usuario.

#### Observación (no hallazgo — asimetría cosmética de redacción)

`docs/USAGE.md:9` (intro de `compat-audit`) dice «código de salida 2 si el número
de argumentos no es exactamente uno» y **omite** “o flag inesperado”, mientras
que copy (52) y cutover (207) sí lo incluyen. La fila `ERR_USAGE` de la tabla
(`USAGE.md:248`) cubre el flag para los 3 CLIs, y `AGENTS.md` §8:177 también.
No es una afirmación falsa (USAGE.md:9 no claims que los flags se acepten); el
código sí rechaza flags en audit (A3/A4 → exit 2). Asimetría de redacción, no
defecto. **No se cuenta como hallazgo.**

### Pregunta del marcador del journal (FIX-R1) — análisis

La consigna pregunta si el marcador `\x01real` del journal (FIX-R1) es visible
para un consumidor externo del journal y, si el journal es API documentada,
debería estar documentado en `AGENTS.md`.

**Verificado sobre el código:**
- El marcador `realDecimalMarker = "\x01real"` (`compat/capture.go:29`) lo
  prefija el trigger de captura SQLite sólo en la rama `typeof='real'` de
  `DECIMAL` (`capture.go:189`).
- Al decodificar, `decodeCapturedRow` (`capture.go:257`) llama
  `canonicalValue(family, source)` por columna, y para `DecimalType`
  `canonicalValue` (`store.go:288-294`) invoca `cutRealDecimalMarker`
  (`store.go:430-437`) que **strips** el prefijo antes de construir el
  `compat.Value` público.
- El tipo público expuesto es `Change.After Row` con `Row = map[string]Value`
  (`journal.go:34,46-56`). El `Value` final **no** lleva el marcador.

**Conclusión:** el marcador es un **centinela interno de la capa de captura**,
stripped durante el decode, **no visible** para un consumidor de la API Go
documentada (`ReadCapturedChanges` → `[]Change`). El formato crudo del journal
(la tabla interna de captura y su columna `after_row` JSON) **no es una API
machine-facing documentada**: `AGENTS.md`/`USAGE.md` describen la captura como
“triggers internos” y el acceso vía `ReadCapturedChanges`/`ApplyChanges`, nunca
como una tabla para consumir directamente. Por lo tanto **no hay gap de
documentación**: `AGENTS.md` correctamente no documenta el marcador (documentarlo
sería documentar un detalle interno que ningún agente debe consumir crudo).
**Área limpia.**

### Áreas limpias (explícito)

Cumplen el contrato byte-a-comportamiento en HEAD, sin desviación, verificadas
con evidencia ejecutada:

- **Exit codes 0/1/2** y **envelope simple** (stdout, una línea, mensaje
  JSON-encodeado — los `\n`/`\t` embebidos del error de connect quedan como
  `\\n`/`\\t`, no rompen la línea; verificado en ex1/ex2) en los 3 CLIs.
- **`ERR_USAGE`** por conteo incorrecto **y** flag desconocido en los 3 CLIs
  (A2/A3/A4, C1/C2/C3, U1/U2/U3); el flag nunca se trata como config posicional.
- **`--dry-run`** sólo en cutover; rechazado como flag desconocido en audit/copy
  (C3 → exit 2); aceptado en cutover (ct2, ex2).
- **Split stdout/stderr de `ERR_AUDIT_NOT_EXACT`**: audit → findings a **stdout**
  luego envelope (A6: stdout = `[{"feature":"tables","status":"exact"},…{"feature":"views","status":"unknown",...}]` + envelope, stderr = razón, exit 1);
  copy/cutover → findings a **stderr** luego envelope en stdout (C4, U4).
- **Paridad M3** copy↔cutover en findings-stderr de `ERR_AUDIT_NOT_EXACT`
  (mismo patrón `<err>\n<findings>\n<err>` + envelope en stdout).
- **`ERR_CONFIG`**: ilegible, decode, `DisallowUnknownFields` (key desconocida
  en config y en `schema_ref`), exactly-one-of `schema`/`schema_ref`,
  `schema_ref` ilegible/JSON-inválido, `Contract.Validate` (same engine,
  versión `0.0.0`, engine no soportado — estos últimos ya verificados en
  AUDIT3-C y consistentes con HEAD).
- **`schema_ref`**: resolución relativa al config (no al cwd) (ct2),
  exactly-one-of, ilegible/inválido/key-desconocida → `ERR_CONFIG` (U5-U10).
- **`ERR_SCHEMA`** (`Schema.Validate`), **`ERR_CONNECT_SOURCE/DESTINATION`**
  (ex1/ex2 → `ERR_CONNECT_DESTINATION` con dest inalcanzable, exit 1).
- **Ejemplos ejecutables**: `examples/contract.example.json` (audit → arreglo
  idéntico al ejemplo del contrato `[{"feature":"tables","status":"exact"},{"feature":"canonical_foreign_keys","status":"exact"},{"feature":"transactions","status":"exact"}]`, exit 0 — A1, byte-idéntico salvo
  whitespace JSON). `examples/schema.example.json` decodable como `Schema` bare
  vía `schema_ref` (ct2). `examples/migration.example.json` y
  `examples/cutover.example.json` decodifican bajo `DisallowUnknownFields`,
  validan, auditan exact y llegan a connect (ex1/ex2).
- **Contrato de ejemplo ejecutable** (`contracts/migration.contract.example.md`):
  paso 1 (audit) → exit 0 y arreglo esperado (A1); paso 2 prefijo stderr
  `compat-cutover:` presente en vivo (ct2/ex2); frontmatter `schema_ref:
  examples/schema.example.json` resuelve y audita exact (ct2).
- **Rename e2e** (AUDIT2-C #7): `TestAuditCLIErrorCodeOnNotExact` presente, viejo
  nombre ausente. HEAD gofmt-clean (0 archivos), `go vet` limpio.
- **`check.ps1`** documentado en README:71 y TESTING:12-16.
- **Marcador del journal (FIX-R1):** interno, stripped antes de la API pública,
  no es API documentada → sin gap de doc.

### Comportamientos nuevos sin documentar

No se encontró ninguno que un agente necesite saber. Todos los comportamientos
introducidos por fixes recientes (`ERR_USAGE` para flags, `schema_ref` real,
`DisallowUnknownFields`, findings-stderr de copy, diverged una-línea de cutover,
gate `check.ps1`, conteo 33/32, prefijo stderr) están documentados en
`AGENTS.md` §8/§8.1, `docs/USAGE.md`, `README.md` y/o `docs/TESTING.md`. El
marcador `\x01real` es deliberadamente interno (stripped antes de la API
pública) y no requiere documentación machine-facing.

---

## NO VERIFICADOS (requieren PostgreSQL vivo)

Sin PG en este entorno, conforme a la consigna:

- **`ERR_VERIFY_DIVERGED`** runtime: `compat-cutover` (una línea `cutoverReport`
  con `code` embebido, sin envelope) y `compat-copy` (`VerificationReport` a
  stderr + envelope en stdout). Requiere llegar a la fase de verify (ambos
  stores conectados). Las **estructuras** coinciden con el doc
  (`cmd/compat-cutover/main.go:184-195`; `cmd/compat-copy/main.go:89-98`); lo no
  verificado es la emisión runtime.
- **`cutoverReport` exitoso** (`status=ready`, exit 0) y `status=diverged`
  (exit 1).
- **`VerificationReport` exitoso** de `compat-copy`
  (`{"source_digest","destination_digest","equivalent":true}`, exit 0).
- **Plan JSON de `--dry-run` exitoso** (`{"status":"plan","audit":…,
  "source_tables":…,"destination_has_tables":…,"phases":…}`, exit 0): source
  SQLite conecta, pero dest PG inalcanzable → `ERR_CONNECT_DESTINATION`.

---

## Observación ambiental (no es hallazgo — fuera de scope)

`compat/zz_audit4_test.go` apareció en el árbol (untracked). Es WIP de un auditor
A/B concurrente (Ronda 4), **no mío**: `compat/` es READ-ONLY para mí y nunca
escribí ahí. No lo toqué. (Un `compat/zz_audit4a_test.go` apareció y luego fue
retirado por su autor durante la sesión.) HEAD es gofmt-clean; esos `.go`
ajenos son del árbol de trabajo, no del repo. Durante la sesión un run de
cutover conectó un SQLite `source.db` relativo al cwd y creó un `src.db` de 0
bytes en el raíz del repo; **lo removí** (artefacto mío, limpiado). `git status`
final: sólo `docs/reports/AUDIT4-C.md` (mío) + el `zz_audit4_test.go` ajeno.

---

## Conteo final

- **Cierre de previos (mi scope):** 9/10 CERRADOS; 1 PERSISTE pero fuera de
  scope primario (AUDIT2-C #4, `compat/` — auditor A), con lado doc coherente.
- **Hallazgos nuevos (Ronda 4):**
  - ALTA: **0**
  - MEDIA: **0**
  - BAJA: **1** (README/TESTING citan VALIDATION_REPORT como fuente del conteo
    vigente, pero el reporte histórico sigue en 28/27/1 — trade-off deliberado
    de FIX-R3, flaggeado como residual doc↔doc)