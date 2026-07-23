# AUDIT6 — Ronda de confirmación (cierre de AUDIT5 + ataque al código nuevo de los fixes)

Fecha: 2026-07-22
Ámbito: confirmación de los 6 hallazgos de AUDIT5 sobre HEAD + ataque dirigido al código nuevo introducido por FIX-A5-DATE / FIX-A5-BAJA.
Entorno: Windows 11, Go 1.26.4, repo `sqlite-postgres-compat` en `main` (commit `85c2d48`). Árbol limpio al inicio y al fin.
PostgreSQL: **real**, PostgreSQL 17.10 (Alpine) vía `COMPAT_POSTGRES_DSN`. DSN usado: `postgres://postgres:***@31.220.22.176:5434/postgres?sslmode=disable` (password **enmascarado como `***`** en todo este reporte; jamás literal ni en líneas de comando — leído de la env var por el orquestador).
Binarios: `compat` y el orquestador buildeados con `go build -o` (nunca `go run` para asserts de exit code — colapsa códigos en Windows). Bases efímeras `audit6_*` (13 creadas, 13 dropeadas, 0 restantes) y fuentes SQLite / configs temporales en el tempdir del SO, todo borrado al terminar.

## Metodología

Cada claim se ejecutó contra el binario buildeado capturando stdout, stderr y exit code por separado, mediante un orquestador Go autocontenido (temporal, en `zz_audit6_tmp/`, borrado al final) que reusa el paquete `compat` para crear fuentes SQLite y bases PG efímeras, y execs el binario `compat`. Los paths que exigen PG vivo crearon bases efímeras `audit6_<variant>_<ts>` dropeadas al final (verificado `audit6_* remaining: 0`). Para el ataque al código nuevo (rama defensiva `time.Time`+`DateType`, replicación incremental por journal) se usó además la **librería** `compat` directamente, porque el CLI `copy`/`cutover` no expone esos caminos aislados (ver §2). El password del DSN se leyó de `COMPAT_POSTGRES_DSN`; nunca se pegó literal.

---

## 1. Confirmación de cierre de los 6 hallazgos de AUDIT5

Re-ejecutada la repro original de cada hallazgo sobre HEAD. Tabla hallazgo → repro → veredicto con salida y exit reales.

### (a) [ALTA §4.1] Columna `date` en copy → exit 0 equivalent=true; cutover → status=ready

```
$ compat copy <cfg con audit6_dates(id integer pk, d date nullable), filas (1,'2020-01-01'),(2,NULL)>
EXIT=0
STDOUT={"source_digest":"708876bb…be1","destination_digest":"708876bb…be1","equivalent":true}

$ compat cutover <mismo cfg, destino limpio>
EXIT=0
STDOUT={"status":"ready","source_digest":"708876bb…be1","destination_digest":"708876bb…be1","changes_applied":0}
STDERR=compat cutover: audit: exact coverage for 3 required features
      compat cutover: capture: change capture installed on source
      compat cutover: snapshot: imported into destination
      compat cutover: catch-up: drained after 0 changes
```
**CERRADO.** Antes (AUDIT5 §4.1): exit 1 `ERR_VERIFY_DIVERGED`/`status=diverged` siempre. Ahora exit 0, digests iguales, `equivalent:true`/`status=ready`.

### (b) [MEDIA §4.2] `countMsg` uniforme en los 3 subcomandos

```
$ compat audit   → exit 2  STDOUT msg: "compat audit requires exactly one contract JSON argument"
$ compat copy    → exit 2  STDOUT msg: "compat copy requires exactly one migration JSON argument"
$ compat cutover → exit 2  STDOUT msg: "compat cutover requires exactly one cutover JSON argument"
```
**CERRADO.** Los tres sobres `ERR_USAGE` comparten ahora el patrón `requires exactly one <kind> JSON argument` (antes cutover divergía con `usage: compat cutover [--dry-run] <cutover.json>`). El hint de stderr subcomando-específico se conserva (no es machine-facing).

### (c) [BAJA §4.3] Doble `--dry-run` → ERR_USAGE

```
$ compat cutover --dry-run --dry-run x.json
EXIT=2
STDOUT={"status":"error","code":"ERR_USAGE","message":"compat cutover: duplicate flag \"--dry-run\""}
```
**CERRADO.** Antes: aceptado en silencio (map dedup). Ahora exit 2 con `duplicate flag "--dry-run"`.

### (d) [BAJA §4.4] `--` como separador

```
$ compat cutover --dry-run -- x.json      → EXIT=1 ERR_CONFIG "open x.json: …" (x.json tratado como path, no flag)
$ compat audit -- --raro.json             → EXIT=1 ERR_CONFIG "open --raro.json: …" (--raro.json = path)
$ compat -- audit --raro2.json            → EXIT=2 ERR_USAGE "compat audit: unexpected flag \"--raro2.json\""
```
**CERRADO.** `--` se descarta y lo siguiente es posicional. Tres observaciones coherentes:
- `cutover --dry-run -- x.json`: el `--` termina flags; `x.json` es el config (no existe → `ERR_CONFIG`, no `unexpected flag`).
- `audit -- --raro.json`: `--raro.json` (empieza con `-`) se acepta como path tras `--` → `ERR_CONFIG open` (no flag inesperado).
- `compat -- audit --raro2.json`: el `--` a nivel **dispatch** se consume y el siguiente token (`audit`) es el subcomando; el `--raro2.json` restante llega al `SplitArgs` de audit sin su propio `--`, por lo que se reporta como `unexpected flag`. Coherente con la semántica de **un solo** `--` (dispatch y subcomando cada uno consumen el suyo).

### (e) [BAJA §4.5] Flag antes del subcomando → mensaje orientador

```
$ compat --dry-run cutover x.json
EXIT=2
STDOUT={"…","code":"ERR_USAGE","message":"compat: flags must follow the subcommand (e.g. compat cutover --dry-run <config>)"}

$ compat bogus   → EXIT=2  msg: "compat: missing or unknown subcommand"   (control: sub desconocido sigue genérico)
```
**CERRADO.** Mensaje orientador cuando el primer token es un flag; el caso normal de subcomando desconocido **no** se rompió (sigue genérico).

### (f) [BAJA §4.6] `err` UNA sola vez en stderr (not-exact / diverged) — conteo de líneas

not-exact (copy y cutover, contract requiere `foreign_keys` — family genérica `unknown`):
```
$ compat copy <cfg not-exact>
EXIT=1  STDERR (2 líneas):
  [{"feature":"foreign_keys","status":"unknown","reason":"requires a parser and semantic compiler"},{"feature":"tables","status":"exact"},{"feature":"canonical_full_text","status":"exact"},{"feature":"primary_keys","status":"exact"}]
  feature "foreign_keys" is unknown: requires parser and semantic compiler
  → línea de error "feature … is unknown" aparece 1 vez (err-envelope-lines=1)
$ compat cutover <cfg not-exact>  → EXIT=1, err-envelope-lines=1 (mismo patrón)
```
diverged (copy, NUMERIC(38,18) rellena `0.10` a escala 18 → divergencia real y reproducible):
```
$ compat copy <cfg div>
EXIT=1
STDOUT={"status":"error","code":"ERR_VERIFY_DIVERGED","message":"snapshot mismatch: source e72bc5… destination b1bf65…"}
STDERR (2 líneas):
  {"source_digest":"e72bc5…","destination_digest":"b1bf65…","equivalent":false}
  snapshot mismatch: source e72bc5… destination b1bf65…
  → "snapshot mismatch" aparece 1 vez; VerificationReport "equivalent":false aparece 1 vez
```
**CERRADO.** Antes (AUDIT5 §4.6): la línea de error aparecía **dos** veces en stderr. Ahora el `err` se imprime una sola vez (vía `cliout.Die`); el payload estructurado (`[]Finding` / `VerificationReport`) se conserva en stderr y el envelope tipado en stdout. Conteo = 1 en los cuatro paths (copy/cutover × not-exact/diverged).

### Tabla resumen de confirmación

| AUDIT5 | Hallazgo | Repro sobre HEAD | Veredicto |
|---|---|---|---|
| §4.1 ALTA | `date` rompe copy/cutover verify | copy exit 0 `equivalent:true`; cutover `status=ready` | ✅ CERRADO |
| §4.2 MEDIA | `countMsg` inconsistente | los 3 ahora `requires exactly one … JSON argument` | ✅ CERRADO |
| §4.3 BAJA | doble `--dry-run` en silencio | exit 2 `duplicate flag "--dry-run"` | ✅ CERRADO |
| §4.4 BAJA | sin `--` separador | `--` descartado, lo siguiente posicional | ✅ CERRADO |
| §4.5 BAJA | flag-antes-sub → msg genérico | msg orientador; sub desconocido sigue genérico | ✅ CERRADO |
| §4.6 BAJA | `err` 2× en stderr | `err` 1× en los 4 paths; payload conservado | ✅ CERRADO |

**Cierre: 6/6.**

---

## 2. Ataque al código nuevo de los fixes

### 2.1 `date → TEXT`: round-trip con fechas borde / inválida / NULL

Cinco casos contra PG real (schema `audit6_edge(id integer pk, d date nullable)`), todos `exit=0 equivalent=true` y columna física PG = `text`:

| Caso | Origen (SQLite TEXT) | Destino landed | exit | equivalent |
|---|---|---|---|---|
| mín `0001-01-01` | `0001-01-01` | `0001-01-01` | 0 | true |
| máx `9999-12-31` | `9999-12-31` | `9999-12-31` | 0 | true |
| **inválida `2020-13-45`** | `2020-13-45` | `2020-13-45` | 0 | true |
| `NULL` | NULL | NULL | 0 | true |
| mixta (3 filas, con NULL) | `2020-01-01`, NULL, `0001-01-01` | idéntico | 0 | true |

**Fecha inválida `2020-13-45`:** ¿qué pasa y es coherente? Ambos extremos almacenan `date` como `TEXT` (SQLite por afinidad TEXT; PG ahora `TEXT` por el fix). Ninguno valida formato de fecha → el valor viaja byte-for-byte `"2020-13-45"` y los digests coinciden (`equivalent:true`). **Es coherente** con el contrato canónico (equivalencia portable byte-for-byte por sobre semántica nativa del dialecto), y consistente con las hermanas `timestamp`/`json`/`uuid` (también `TEXT`, sin validación nativa). No es un hallazgo: la familia `date` es un carrier de texto canónico, no un validador de calendario; un agente que necesite validación de fechas no está en el caso de uso de migración portable canónica (mismo criterio que `timestamp` no usa `TIMESTAMP` nativo). Área limpia.

### 2.2 Rama defensiva `time.Time` + `DateType` con destino legado `DATE` nativo

Se creó a mano una tabla PG con `DATE` nativo: `CREATE TABLE audit6_legacy (id bigint PRIMARY KEY, d DATE)` + filas `(1,'2020-01-01'),(2,NULL)`.

- **Sonda de tipo go:** la columna `DATE` nativa se lee como `time.Time` (`go type = time.Time value=2020-01-01 00:00:00 +0000 UTC`) — confirma la premisa del fix.
- **Re-verify aislada (librería):** `ExportSnapshot` del destino legado (schema declarando `DateType`) + `VerifySnapshots` vs snapshot SQLite → **`equivalent=true`** (digests iguales). La rama defensiva (`canonicalValue`: `if kind == DateType` dentro del check `time.Time` → `DateValue "2006-01-02"`) **converge**, no diverge.
- **CLI `copy` contra el destino legado preexistente:** `exit=1` con **`ERR_SNAPSHOT`** `apply base schema: ERROR: relation "audit6_legacy" already exists (SQLSTATE 42P07)` — **no** `ERR_VERIFY_DIVERGED`. `ImportSnapshot` hace `CREATE TABLE` (sin `DROP`), así que choca con la tabla legada antes de llegar a verify.

**¿Alcanzable con destino legado DATE nativo?** La rama defensiva **sí es alcanzable y converge**, pero **sólo por la librería** (re-verify aislada). **No es alcanzable por el CLI** `copy`/`cutover`: ambos recrean el schema (`ImportSnapshot`) y colisionan con la tabla legada (`ERR_SNAPSHOT`), o la reemplazan si el destino está limpio (entonces ya es `TEXT`, no legado). Véase hallazgo §3.1 (la doc §11 afirma divergencia; el comportamiento real es convergencia vía librería / colisión vía CLI).

### 2.3 `date` + replicación incremental (UPDATE/INSERT/DELETE con captura instalada)

A nivel librería (el CLI `cutover` no expone una pausa para mutar entre snapshot y drain): `ApplySchema` + `InstallChangeCapture` en origen SQLite, `ExportSnapshot`→`ImportSnapshot` a PG, luego `UPDATE d='0001-01-01' WHERE id=1`, `INSERT (3,'2020-02-29')`, `DELETE WHERE id=2`; drain del journal con `ReadCapturedChanges` + `ApplyChangesTolerant`; verify.

```
[incr] journal changes applied=3
[incr verify] equivalent=true  source=0e5fc631…  dest=0e5fc631…
[incr landed] id=1 d=0001-01-01, id=3 d=2020-02-29   (id=2 eliminado)
```
**Área limpia.** La familia `date` round-tripea por el camino del journal (triggers de captura + applier tolerante) tanto como por el snapshot: 3 cambios replicados, digests iguales, valores landed correctos (incluido `2020-02-29`).

### 2.4 `--` separator: variantes

| Comando | exit | Comportamiento | Veredicto |
|---|---|---|---|
| `copy -- --config.json` (archivo cuyo nombre empieza con `-`) | 0 | `--config.json` aceptado como path → `equivalent:true` | ✅ |
| `audit -- -- x.json` (doble `--`) | 2 | 1er `--` fin-de-flags; 2do `--` y `x.json` son posicionales → count 2 → `ERR_USAGE` (countMsg) | ✅ coherente |
| `audit x.json --` (`--` al final) | 1 | `x.json` posicional, `--` descartado → count 1 → procede → `ERR_CONFIG open x.json` | ✅ coherente |
| `cutover --dry-run --` (`--` al final sin args) | 2 | `--dry-run` flag, `--` fin-de-flags, sin posicional → count 0 → `ERR_USAGE` (countMsg) | ✅ coherente |

Semántica de **un solo `--`** consistente. Área limpia.

### 2.5 Flag duplicado: ¿solo `--dry-run` o cualquier booleano futuro? ¿el mensaje nombra el flag?

- `cutover --dry-run --dry-run x.json` → `duplicate flag "--dry-run"` (nombra el flag). ✅
- `copy --bogus --bogus x.json` → `unexpected flag "--bogus"` (no `duplicate`).

**Alcance:** la detección de duplicado es para **flags reconocidos** (`SplitArgs`: `if known[a]` → `if present[a]` → duplicate). Hoy el único flag reconocido es `--dry-run` (cutover); audit/copy no tienen flags reconocidos, así que un token repetido que empieza con `-` es `unexpected`, no `duplicate`. Cualquier **futuro** flag booleano sólo entra en la verificación de duplicados si se añade a `knownFlags` del subcomando. El mensaje nombra el flag ofensor (`%q`). Área limpia (comportamiento correcto y documentado en AGENTS §8: "duplicated recognized flag").

### 2.6 Mensaje orientador: ¿sólo flag-antes-del-sub, o también rompió el sub desconocido normal?

| Primer token | mensaje | tipo |
|---|---|---|
| `-x` | `flags must follow the subcommand…` | orientador |
| `--version` | `flags must follow the subcommand…` | orientador |
| `--help` | `missing or unknown subcommand` | genérico (help-ish) |
| `-h` | `missing or unknown subcommand` | genérico (help-ish) |
| `-help` | `missing or unknown subcommand` | genérico (help-ish) |
| `bogus` | `missing or unknown subcommand` | genérico (sub desconocido) |
| `""` (vacío) | `missing or unknown subcommand` | genérico |

**Sólo aparece cuando el primer token empieza con `-` y no es help-ish** (`DispatchUsageMessage` + `isHelpIsh`). El caso normal de subcomando desconocido **no se rompió**: sigue dando el genérico. Área limpia.

### 2.7 stderr único: ¿el payload estructurado sigue presente y parseable?

- not-exact (copy): stderr línea 1 = `[]Finding` → **parseable** (`json.Unmarshal` OK, 4 findings). Envelope `ERR_AUDIT_NOT_EXACT` en stdout. Línea de error 1× en stderr.
- diverged (copy): stderr contiene `VerificationReport` → **parseable** (`equivalent:false`, ambos digests recuperables). Envelope `ERR_VERIFY_DIVERGED` en stdout. `snapshot mismatch` 1× en stderr.
- cutover diverged: stdout = **1** línea JSON con `code` embebido (`{"status":"diverged","code":"ERR_VERIFY_DIVERGED",…}`), `has-separate-error-envelope=false`. Exactamente el contrato §8.1 caso (b).

**AGENTS.md §8.1 — claim por claim:**

| Claim §8.1 | Veredicto |
|---|---|
| Simple error envelope (default): una línea JSON a stdout, nada más machine-readable | ✅ (ERR_CONFIG/ERR_USAGE/ERR_CONNECT_* muestran exactamente 1 línea JSON en stdout) |
| `compat cutover` diverged: exactamente **una** línea JSON en stdout con `code` embebido, **sin** envelope `{"status":"error",…}` aparte | ✅ (stdout-lines=1, has-separate-error-envelope=false) |
| `compat copy` not-exact/diverged: payload `[]Finding`/`VerificationReport` a stderr **antes** del envelope; línea de error a stderr **exactly once** | ✅ (payload parseable, err 1×; orden payload→err→envelope) |
| `compat cutover` not-exact sigue el mismo patrón stderr-once para `[]Finding` | ✅ (cutover not-exact err-envelope-lines=1) |
| Cada línea de envelope es un objeto JSON parseable; el agente ramifica por `code`; taxonomía cerrada | ✅ (todos los envelopes observados llevan `code`) |
| Tabla `ERR_USAGE`: "wrong argument count, an unexpected flag, a duplicated recognized flag, or a missing/unknown subcommand" | ✅ (b count, c duplicate, d/e unexpected, e/bogus unknown — los 4 cubiertos) |
| `ERR_VERIFY_DIVERGED`: cutover una línea con code embebido; copy `VerificationReport` a stderr + envelope | ✅ |

§8.1 describe **exactamente** el patrón actual. Área limpia.

---

## 3. Hallazgos nuevos

### 3.1 [BAJA] AGENTS.md §11 afirma divergencia contra destino legado `DATE` nativo; el comportamiento real es convergencia (librería) o colisión (CLI)

**Síntoma.** AGENTS.md §11 (línea 311) afirma: *"The first `compat copy`/`compat cutover` verify against a legacy native-DATE destination reports `ERR_VERIFY_DIVERGED` / `status=diverged` (exit 1)"*. La propia §11 (línea 310) afirma a la vez que la rama defensiva hace que una re-verificación aislada *"converges rather than diverging"*. Las dos afirmaciones se contradicen, y la empírica contradice la de divergencia:

- **Librería** (re-verify aislada contra tabla PG `DATE` nativa creada a mano): `equivalent=true` (converge) — §2.2.
- **CLI `copy`** contra la tabla legada preexistente: `ERR_SNAPSHOT` (`relation already exists`), **no** `ERR_VERIFY_DIVERGED` — §2.2. El CLI recrea el schema (`ImportSnapshot` hace `CREATE TABLE` sin `DROP`) y choca antes de verify.

**Por qué BAJA.** Es prosa de upgrade-note (no el contrato machine-facing §8.1, que sí es exacto). El principio seguro —"la divergencia se detecta, nunca es silenciosa"— se conserva: no hay corrupción silenciosa. Sólo el **outcome** específico ("first verify diverges") está sobreestatado: en el caso alineado (origen date-only) la rama defensiva converge, y el CLI ni siquiera llega a verify (colisiona). Un agente que siguiera §11 esperaría `ERR_VERIFY_DIVERGED` y recibiría `equivalent=true` (librería) o `ERR_SNAPSHOT` (CLI). El FIX-A5-DATE-REPORT §1 repite la misma afirmación inexacta.

**Sugerencia (READ-ONLY, no aplicada).** Reescribir §11 línea 311 para que diga que, gracias a la rama defensiva, una re-verificación aislada contra un destino legado `DATE` nativo **converge** cuando los valores date-only coinciden (la divergencia sólo aparece si el origen llevaba componente horario que el `DATE` nativo truncó), y que el CLI `copy`/`cutover` contra una tabla legada preexistente **colisiona** con `ERR_SNAPSHOT` (no diverge) — el camino soportado sigue siendo recrear el schema.

### Áreas limpias (ataque)

- §2.1 fechas borde/inválida/NULL — area limpia (coherente con contrato canónico).
- §2.3 replicación incremental de `date` por journal — area limpia.
- §2.4 variantes de `--` — area limpia.
- §2.5 flag duplicado (sólo reconocidos; nombra el flag) — area limpia.
- §2.6 mensaje orientador (no rompe sub desconocido) — area limpia.
- §2.7 stderr único + payload parseable + §8.1 claim por claim — area limpia.

---

## 4. Limpieza y verificación final

- Bases efímeras `audit6_*`: 13 creadas, 13 dropeadas; verificación post-run `audit6_* remaining: 0`. ✅
- Binarios y orquestador temporales (`compat.exe`, `orch.exe`), fuentes SQLite, configs y dir `zz_audit6_tmp/` borrados. ✅
- Password jamás literal en el reporte (enmascarado `***`); leído de `COMPAT_POSTGRES_DSN`, nunca en líneas de comando. ✅
- `git status`: árbol limpio; sólo se crea `docs/reports/AUDIT6-CONFIRM.md`. No `git add`/`commit`. ✅

## Conteo final por severidad

- **Cerrados AUDIT5: 6/6** (1 ALTA + 1 MEDIA + 4 BAJA).
- **Nuevos: 1 BAJA** (§3.1 — §11 afirma divergencia legado `DATE`; behavior real converge/colisiona). 0 ALTA, 0 MEDIA.