# FIX-A5 — Familia `date` (ALTA §4.1) + `countMsg` unificado (MEDIA §4.2)

Fecha: 2026-07-22
Ámbito: fix de los 2 hallazgos de AUDIT5-CLI — ALTA §4.1 (familia `date` siempre diverge en copy/cutover) y MEDIA §4.2 (`countMsg` inconsistente entre los 3 subcomandos).
Entorno: Windows 11, Go 1.26.4, repo `sqlite-postgres-compat` en `main` (padre `hostinger`). Árbol limpio al inicio salvo este fix.
PostgreSQL: **real**, PostgreSQL 17.10 (Alpine) vía `COMPAT_POSTGRES_DSN`. DSN usado: `postgres://postgres:***@31.220.22.176:5434/postgres?sslmode=disable` (password **enmascarado como `***`** en todo este reporte; jamás literal). Bases efímeras propias con prefijo `fixa5_` creadas y dropeadas al terminar; la suite e2e usa su base `compat_e2e_<ts>` (creada/dropeada por `TestMain`).

---

## 1. Decisión sobre `time.Time` + `DateType` (defensivo)

**Decisión: SÍ se maneja.** Cuando `canonicalValue` recibe un `time.Time` y la familia es `DateType`, formatea `"2006-01-02"` (date-only) **antes** de la rama `time.Time` genérica que produce `TimestampValue`.

**Por qué.** El fix principal mapea `DateType → TEXT` en PG, así que los destinos nuevos nunca devuelven un `time.Time` para una columna date (pgx devuelve un string para `TEXT`). Pero un destino creado por una **versión anterior** de la herramienta aún tiene columnas `DATE` nativas; si se re-verifica con el código nuevo, pgx devuelve `time.Time`. Sin el manejo defensivo, esa rama genérica lo plegaría a `TimestampValue` `"2020-01-01T00:00:00Z"` y divergiría del origen SQLite `"2020-01-01"`. Con el manejo, la re-verificación aislada contra un destino legado **converge** en vez de diverger.

**Limitación documentada (igual).** El camino soportado para un destino legado con `DATE` nativo es **recrear el schema** (drop + re-`ApplySchema`, o re-correr la migración completa). El manejo defensivo sólo evita que una re-verificación puntual diverja falsamente; no convierte al destino legado en un destino soportado. La primera verificación de `compat copy`/`compat cutover` contra un destino con `DATE` nativo legado igual reporta `ERR_VERIFY_DIVERGED`/`status=diverged` (la divergencia **la detecta verify, no es silenciosa**), y la acción correctiva es recrear el schema. Documentado en `AGENTS.md` §11 y `docs/USAGE.md`.

**Trade-off.** Se agrega una rama `if kind == DateType` dentro del `time.Time` check. Costo: una rama más en `canonicalValue` (cero impacto en hot path para destinos nuevos, que no la alcanzan). Beneficio: robustez frente a destinos legados y coherencia con el espíritu del resto del fix (que la familia `date` nunca diverja falsamente).

---

## 2. Fix ALTA §4.1 — `DateType → TEXT` en PostgreSQL

### Root cause (verificado por el PM con PG real, AUDIT5 §4.1)

`compat/ddl.go` mapeaba `DateType` al tipo nativo PG `DATE`. pgx devuelve una columna `DATE` como `time.Time` (sonda directa: `goType=time.Time value=2020-01-01 00:00:00 +0000 UTC`). En `canonicalValue`, la rama `time.Time` (evaluada **antes** del `switch kind`) lo canonizaba a `TimestampValue` `"2020-01-01T00:00:00Z"`. El origen SQLite almacena `date` como `TEXT` y al leerse como string caía al `case DateType → DateValue "2020-01-01"`. Digests distintos → `ERR_VERIFY_DIVERGED` **siempre**, aunque los datos son lógicamente equivalentes. El diseño ya protegía `timestamp`/`json`/`uuid` mapeándolos a `TEXT` con comentarios protectores; `date` era la única familia temporal fuera del patrón.

### Cambio

`compat/ddl.go` (Postgres):
```go
case DateType:
    // pgx returns a native DATE column as a time.Time, which the generic
    // time.Time branch in canonicalValue would fold to a TimestampValue
    // ("2020-01-01T00:00:00Z") and diverge from the SQLite TEXT source
    // ("2020-01-01"). TEXT preserves the exact canonical date value used by
    // both engines, mirroring the timestamp/json/uuid protective mapping.
    return "TEXT", nil
```

`compat/store.go` (`canonicalValue`, rama `time.Time`):
```go
if timestamp, ok := source.(time.Time); ok {
    if kind == DateType {
        return Value{Kind: DateValue, Value: timestamp.UTC().Format("2006-01-02")}, nil
    }
    return Value{Kind: TimestampValue, Value: timestamp.UTC().Format(time.RFC3339Nano)}, nil
}
```

### Trade-offs

- **Consistencia con las hermanas.** `date` ahora sigue el mismo patrón protector que `timestamp`/`json`/`uuid` (PG `TEXT` + comentario). Se elimina la oversight original.
- **Costo de physical type.** La columna PG deja de ser `DATE` nativo y pasa a `TEXT`. Pierde validación de formato `DATE` y aritmética de fechas nativa en PG. **Aceptado**: el contrato canónico prioriza la equivalencia byte-for-byte del valor portable por sobre la semántica nativa del dialecto (mismo criterio que `timestamp`, que también es `TEXT` y no `TIMESTAMP` nativo). Un agente que necesite aritmética de fechas nativa no está en el caso de uso de migración portable canónica.
- **Destinos legados.** Ver §1 (limitación documentada + manejo defensivo).

### Tests

- **Unit** `compat/ddl_test.go`: `TestPostgresDateUsesLosslessTextStorage` — `compileType(Postgres, DateType) == "TEXT"`.
- **Unit** `compat/store_test.go`: `TestCanonicalDateHandlesTimeTimeFromLegacyNativeDate` — `canonicalValue(DateType, "2020-01-01")` y `canonicalValue(DateType, time.Time(2020-01-01))` convergen a `DateValue "2020-01-01"`; offset no-UTC → date-only UTC; la rama genérica sigue siendo `TimestampValue` para `TimestampType`.

---

## 3. Fix MEDIA §4.2 — `countMsg` unificado

### Hallazgo (AUDIT5 §4.2)

Los 3 subcomandos emitían mensajes distintos para "cantidad incorrecta" (envelope `ERR_USAGE` exit 2):

| Sub | `countMsg` antes |
|---|---|
| `audit` | `compat audit requires exactly one contract JSON argument` |
| `copy` | `compat copy requires exactly one migration JSON argument` |
| `cutover` | `usage: compat cutover [--dry-run] <cutover.json>` |

`audit` y `copy` usaban la prosa "`compat X requires ... JSON argument`"; `cutover` divergía con la forma `usage: ...`. Pre-existente (preservado a propósito por FEAT-UNIFY trade-off #3).

### Cambio

Unificado al patrón mayoritario/documentado (`requires exactly one ... JSON argument`). `compat/cutover.go`:
```go
"compat cutover requires exactly one cutover JSON argument"
```
El hint de **stderr** (`uso: compat cutover [--dry-run] <cutover.json>\n...`) se conserva intacto — es subcomando-específico por diseño y no es parte del envelope machine-facing. Sólo converge el **mensaje del envelope** (`countMsg`).

### Trade-offs

- **Consistencia.** Los 3 subcomandos ahora emiten el mismo formato de envelope para conteo incorrecto.
- **Cambio de texto observable.** El envelope de cutover para conteo incorrecto cambia de `usage: ...` a `compat cutover requires exactly one cutover JSON argument`. Es un cambio de texto de un mensaje de error (`ERR_USAGE`/exit 2), no de código ni de exit. No hay tests e2e existentes que asertaran el texto viejo de cutover (verificado: `TestCLIRejectsUnknownFlagAsUsage` y `TestCLIDispatchUsage` sólo asertan `code=ERR_USAGE` + exit 2, no el texto del countMsg; `cliout_test.go` usa strings propios `prog ...`). Se agrega el assert nuevo abajo.

### Tests

- **e2e** `e2e/system_test.go`: `TestCLIUsageCountMessageConsistent` — para los 3 subcomandos con 0 positionals: exit 2, `code=ERR_USAGE`, y mensaje exacto `compat <sub> requires exactly one <kind> JSON argument`.

---

## 4. e2e nuevo de round-trip `date` (contra PG REAL)

`e2e/system_test.go`: `TestSystemDateFamilyRoundTripsEquivalent` — schema con columna `date` (`fixa5_dates(id integer pk, d date nullable)`), filas `(1,'2020-01-01')` y `(2,NULL)`, `compat copy` contra PG real vía el **binario buildeado** (no `go run`, que colapsa exit codes en Windows). Asserta exit 0 y `equivalent=true`, y verifica que el valor aterrizó como `2020-01-01` y el NULL se preservó. Usa la base e2e compartida (`postgresTestDSN` = `compat_e2e_<ts>`), con `DROP TABLE IF EXISTS fixa5_dates` previo para que el import sea aditivo sobre un destino limpio.

Salida real del e2e (password no aparece; la suite lo lee de `COMPAT_POSTGRES_DSN`):
```
=== RUN   TestSystemDateFamilyRoundTripsEquivalent
--- PASS: TestSystemDateFamilyRoundTripsEquivalent (10.48s)
```

### Demo con binario buildeado (salida real del DESPUÉS)

Script Go autocontenido (`zz_fixa5_demo/`, creado y borrado) que: crea base efímera `fixa5_date_demo`, buildea `compat` desde el repo, crea fuente SQLite (`fixa5_dates` con `d TEXT`, filas `(1,'2020-01-01')`,`(2,NULL)`), escribe `migration.json` (familia `date`), corre `compat copy`, dropea la base. Salida real (password enmascarado):

```
[setup] created PG database "fixa5_date_demo" (admin DSN masked: postgres://postgres:c***@31.220.22.176:5434/postgres?sslmode=disable)
[setup] built compat binary: C:\Users\ADMINI~1\AppData\Local\Temp\fixa5-demo-448172935\compat.exe
[setup] SQLite source ready: table fixa5_dates, rows (1,'2020-01-01'), (2,NULL)
=== RUN: compat copy <date config> ===
--EXIT--
0
--STDOUT--
{"source_digest":"ce2534042bc74d9a33b3e9fca05f8da0a1da87135e4e10f037b0b0a242d9ee8e","destination_digest":"ce2534042bc74d9a33b3e9fca05f8da0a1da87135e4e10f037b0b0a242d9ee8e","equivalent":true}
[verify] PG column fixa5_dates.d physical type = text
[verify] id=1 d="2020-01-01" (valid=true); id=2 d valid=false
[cleanup] dropped PG database "fixa5_date_demo"
```

- **ANTES (AUDIT5 §4.1):** `EXIT=1`, stdout envelope `ERR_VERIFY_DIVERGED` (`source 7fcf9d... destination 7369f5...`), `equivalent:false`.
- **DESPUÉS (arriba):** `EXIT=0`, `source_digest == destination_digest`, `equivalent:true`. La columna física PG es `text` (confirma `DateType → TEXT`), el valor aterriza como `2020-01-01` y el NULL se preserva (`valid=false`).

### Tests e2e MEDIA (salida real)

```
=== RUN   TestCLIUsageCountMessageConsistent
=== RUN   TestCLIUsageCountMessageConsistent/audit
=== RUN   TestCLIUsageCountMessageConsistent/copy
=== RUN   TestCLIUsageCountMessageConsistent/cutover
--- PASS: TestCLIUsageCountMessageConsistent (0.06s)
    --- PASS: TestCLIUsageCountMessageConsistent/audit (0.02s)
    --- PASS: TestCLIUsageCountMessageConsistent/copy (0.02s)
    --- PASS: TestCLIUsageCountMessageConsistent/cutover (0.02s)
```

---

## 5. Docs

- `AGENTS.md` §1 (tabla de familias): `date` ahora `TEXT` / `TEXT` (era `TEXT` / `DATE`).
- `AGENTS.md` §11 (nuevo, "Upgrade compatibility — `date` family mapping"): frontera de mapeo de schema; recrear schema del destino legado; la divergencia la detecta verify, no es silenciosa.
- `docs/USAGE.md`: nueva subsección "Compatibilidad al actualizar la herramienta (familia `date`)" junto a la nota de upgrade de captura (FIX-R4), mismo contenido en español.
- `README.md` y `docs/TESTING.md`: conteo e2e actualizado de 34 (33+1) a **36 (35+1)** — se agregaron 2 tests top-level (`TestSystemDateFamilyRoundTripsEquivalent`, `TestCLIUsageCountMessageConsistent`).

`docs/USAGE.md` no menciona el mapeo físico de `date` en otra parte, así que no hubo otra línea que tocar para el mapeo.

---

## 6. Definición de hecho — verificación

| Criterio | Resultado |
|---|---|
| `.\scripts\check.ps1` verde | ✅ `gofmt: OK` / `go vet: OK` / `go test: OK` (exit 0) |
| `go vet -tags=e2e ./e2e` verde | ✅ exit 0 |
| e2e nuevo de `date` corrido en VERDE contra el PG dado (salida real pegada, password enmascarado) | ✅ §4 |
| demo con binario buildeado: `compat copy` con config date → exit 0 + `equivalent=true` | ✅ §4 (antes exit 1 diverged) |
| bases temporales propias en PG con prefijo `fixa5_` dropeadas al terminar | ✅ `fixa5_date_demo` dropeada; verificación post-run: "OK: no fixa5_/compat_e2e_ databases remain" |
| password jamás literal en REPORT/logs pegados | ✅ enmascarado `c***`/`***` |
| no `git add/commit` | ✅ sólo edición de archivos |
| archivos tocados sólo del set permitido | ✅ `compat/ddl.go`, `compat/ddl_test.go`, `compat/store.go`, `compat/store_test.go`, `cmd/compat/cutover.go`, `e2e/system_test.go`, `AGENTS.md`, `docs/USAGE.md`, `docs/TESTING.md`, `README.md` + este reporte nuevo |

### Suite e2e completa contra PG real

36 pruebas de nivel superior: **35 PASS + 1 FAIL intencional** (`TestSystemClaimsExactCoverageForRequiredFeatureFamilies`, baseline conocido `32+1 intencional` que ahora es `35+1` por los 2 tests agregados — los families genéricos `foreign_keys`/`check_constraints`/`indexes`/`triggers`/`views`/`stored_routines`/`full_text` siguen `unknown` por contrato §9). No es regresión ni hallazgo; todos los tests CLI y de familias (incluidos `timestamp`/`json`/`uuid`/`float`/`boolean`/`integer`/`text` y ahora `date`) pasan en verde.

### No-regresión de familias hermanas

`TestSystemPreservesJSONUUIDAndTimestampSemantics`, `TestPostgresJSONAndUUIDUseLosslessTextStorage`, `TestPostgresTimestampUsesLosslessTextStorage`, `TestSQLiteSnapshotRoundTrip` y el resto de la suite pasan. La rama `time.Time` genérica para `TimestampType` se preserva (verificada por `TestCanonicalDateHandlesTimeTimeFromLegacyNativeDate`: la rama no-`DateType` sigue dando `TimestampValue "2020-01-01T00:00:00Z"`).

---

## 7. Archivos modificados

- `compat/ddl.go` — `DateType → TEXT` (Postgres) + comentario protector.
- `compat/store.go` — `canonicalValue`: rama `time.Time` + `kind == DateType` → `DateValue "2006-01-02"` (defensivo).
- `compat/ddl_test.go` — `TestPostgresDateUsesLosslessTextStorage`.
- `compat/store_test.go` — `TestCanonicalDateHandlesTimeTimeFromLegacyNativeDate` + import `time`.
- `cmd/compat/cutover.go` — `countMsg` unificado.
- `e2e/system_test.go` — `TestSystemDateFamilyRoundTripsEquivalent` + `TestCLIUsageCountMessageConsistent`.
- `AGENTS.md` — §1 tabla + §11 upgrade note.
- `docs/USAGE.md` — subsección upgrade `date`.
- `docs/TESTING.md` — conteo 36 (35+1).
- `README.md` — conteo 36 (35+1).
- `docs/reports/FIX-A5-DATE-REPORT.md` — este reporte (nuevo; no se tocaron reports existentes).