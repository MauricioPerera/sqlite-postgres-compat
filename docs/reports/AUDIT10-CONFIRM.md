# AUDIT10-CONFIRM — Ronda de confirmación: `gen_random_uuid()` y bloqueo de `WITH RECURSIVE`

**Fecha:** 2026-07-23
**Auditor:** adversarial independiente, sin memoria previa.
**Motores:** SQLite real (`modernc.org/sqlite`, `:memory:` / archivo temporal) y **PostgreSQL 17 real**
(`postgres://postgres:***@31.220.22.176:5434/postgres`, password enmascarada). El harness E2E crea y
**dropea** una base efímera `compat_e2e_<nanos>`; todas mis tablas (`audit10_*`) viven dentro de esa
base y desaparecen con ella. **0 bases remanentes.**
**Alcance de cambios:** READ-ONLY sobre el repo. Probes en ficheros temporales `compat/zz_audit10_test.go`
y `e2e/zz_audit10_test.go`, **borrados al terminar**. Único fichero permanente: este reporte.
**Binarios:** los tests se ejecutaron con `go test` (exit code observado en cada corrida). Sin procesos
en foreground colgados.

---

## Parte 1 — Confirmación de cierre de `gen_random_uuid()`

Re-ejecución de las pruebas congeladas de FEAT-RANDOMUUID contra **ambos** motores reales.

| Prueba (re-ejecutada) | SQLite real | PostgreSQL 17 real | Estado |
|---|---|---|---|
| Parse `gen_random_uuid()` → `Kind:"gen_random_uuid"`; args rechazados por aridad (unit congelado `TestParseCatalogExpressionGenRandomUUID`) | n/a (parser) | n/a (parser) | **CERRADO** (PASS) |
| Compile congelado por motor (`TestCompileGenRandomUUIDForBothEngines`, incl. 512 ejecuciones v4 en SQLite) | `(lower(hex(randomblob(4)) \|\| … ))` | `gen_random_uuid()` | **CERRADO** (PASS) |
| **DEFAULT** en columna `family=uuid`, 8 inserts sin `id` (E2E `TestSystemGenRandomUUIDGeneratesValidV4OnBothEngines`) | 8 v4 válidos, únicos | 8 v4 válidos, únicos | **CERRADO** (PASS, 2.10s) |
| **Trigger action** `INSERT … VALUES (gen_random_uuid(), …)`, 8 eventos | 8 v4 válidos, únicos | 8 v4 válidos, únicos | **CERRADO** (PASS) |

Salida real:

```
=== FROZEN UNIT ===
ok   example.com/sqlite-postgres-compat/compat   2.382s
=== FROZEN E2E (real PG+SQLite) ===
--- PASS: TestSystemGenRandomUUIDGeneratesValidV4OnBothEngines (2.10s)
ok   example.com/sqlite-postgres-compat/e2e   5.123s
```

**Veredicto Parte 1: gen_random_uuid CERRADO.** Ambos caminos documentados (DEFAULT + valor de trigger)
producen v4 válidos y únicos en SQLite real y PG17 real; los congelados unit y E2E siguen verdes.

---

## Parte 2 — Ataque adversarial al código nuevo

Evidencia ejecutada en `compat/zz_audit10_test.go` (unit) y `e2e/zz_audit10_test.go` (E2E, PG real).

### 2.1 Robustez del parser — LIMPIO (sin hallazgo)

`TestAudit10ParseAttacks` (ejecutado):

```
parse("gen_random_uuid()"):    ACCEPTED kind="gen_random_uuid"
parse("gen_random_uuid( )"):   ACCEPTED   (espacios internos — TrimSpace)
parse("GEN_RANDOM_UUID()"):    ACCEPTED   (mayúsculas — ToLower)
parse("Gen_Random_Uuid()"):    ACCEPTED   (mixto)
parse("gen_random_uuid ()"):   ACCEPTED   (espacio antes del paréntesis)
parse("  gen_random_uuid()  "):ACCEPTED   (whitespace externo)
parse("gen_random_uuid(1)"):   REJECTED (gen_random_uuid takes no arguments)
parse("gen_random_uuid(a)"):   REJECTED
parse("gen_random_uuid('x')"): REJECTED
parse("gen_random_uuid(, )"):  REJECTED
```

Mayúsculas/espacios se aceptan por diseño (los nombres de función de la gramática son
case-insensitive y se `TrimSpace`, consistente con el resto). Cualquier argumento se rechaza por
aridad. **Comportamiento correcto y consistente; ningún hallazgo.**

### 2.2 Anidamiento en expresión mayor — LIMPIO (sin bug)

`TestAudit10NestedExpressionCompiles` (ejecutado): `coalesce(gen_random_uuid(), 'x')`,
`lower(gen_random_uuid())`, `upper(gen_random_uuid())` **compilan en ambos motores**. Son
semánticamente inútiles (`coalesce` nunca devuelve el fallback porque un UUID nunca es NULL;
`upper(...)` produce un UUID en mayúsculas — no canónico pero **idéntico criterio en ambos motores**),
pero son SQL válido y consistente. **Ningún bug.**

### 2.3 Validación estricta masiva — LIMPIO

`TestAudit10Strict300UUIDs` (ejecutado): 300 UUIDs generados por `sqliteUUIDv4Expression`, parser
estricto v4 (`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`):

```
300/300 strict v4 valid, unique. variant-nibble distribution: map[8:89 9:80 a:61 b:70]
```

**100% válidos y únicos**, nibble de variante repartido en `{8,9,a,b}`. Sumado a las 512 del test
congelado: 812 muestras, 0 inválidas.

### 2.4 `gen_random_uuid()` en CHECK / índice de expresión / columna generada STORED (motores reales)

`TestAudit10GenRandomUUIDMisuseContexts` (ejecutado contra PG17 real). `applyBoth` aplica el mismo
esquema a SQLite y a PG y compara aceptación (divergencia = un motor acepta y el otro rechaza):

```
[CHECK gen_random_uuid()]              sqlite: accept=true   postgres: accept=true
[EXPRESSION INDEX gen_random_uuid()]   sqlite: accept=false (non-deterministic functions prohibited in index expressions)
                                       postgres: accept=false (functions in index expression must be marked IMMUTABLE, 42P17)
[STORED GENERATED gen_random_uuid()]   sqlite: accept=false (non-deterministic functions prohibited in generated columns)
                                       postgres: accept=false (generation expression is not immutable, 42P17)
```

- **Índice de expresión:** compat compila el DDL, pero **ambos** motores lo rechazan en `ApplySchema`
  de forma consistente. Sin divergencia. → **BAJA** (B1): el freno lo pone el motor, no compat.
- **Columna generada STORED (interacción con Cubo A):** compat compila, **ambos** motores rechazan
  consistentemente. Sin divergencia. → **BAJA** (B2). Responde la pregunta de Cubo A: una columna
  STORED que recomputa `gen_random_uuid()` es rechazada por los dos motores; no hay mal comportamiento
  silencioso.
- **CHECK:** **ambos motores ACEPTAN el DDL de un CHECK no determinista** y compat **no lo rechaza**.
  Ver Hallazgo M1.

### 2.5 Copy con columna NOT NULL DEFAULT `gen_random_uuid()` (sin default de PK) — LIMPIO

`TestAudit10CopyNotNullDefault` (ejecutado): tabla `audit10_copy(id uuid NOT NULL DEFAULT
gen_random_uuid(), n int)`, 5 inserts en SQLite **sin** `id` (el default dispara), `ExportSnapshot` →
`ImportSnapshot` a **PG17 real**:

```
copy NOT NULL default: 5/5 valid v4 UUIDs survived SQLite->PostgreSQL copy
```

Los valores se generan en el origen y viajan como datos (el import no re-dispara el default). **5/5
v4 válidos preservados. Limpio.**

---

### Hallazgo M1 — `gen_random_uuid()` alcanzable y **sin guarda** en un CHECK no determinista · **MEDIA**

**Evidencia (ejecutada, §2.4):** compat compila `CHECK (is_not_null(gen_random_uuid()))` y **tanto
SQLite como PG17 lo aceptan** (`accept=true` en ambos). PostgreSQL —a diferencia de índices y columnas
generadas— **no exige inmutabilidad en constraints CHECK**, y SQLite tampoco frena funciones no
deterministas en un CHECK.

**Por qué importa:** `gen_random_uuid()` es —por el propio reporte— **el primer nodo no determinista de
la gramática**. Antes de `eb28b7e` era imposible colocar no-determinismo en un CHECK; ahora es
expresable y **compat no lo rechaza**. El caso probado (`is_not_null(...)`, siempre verdadero) es
inofensivo, pero la gramática permite construir un predicado no determinista **con resultado variable**
sobre este nodo (p.ej. un `lt`: `gen_random_uuid() < '8000…'`, ~50/50), que **admitiría/rechazaría filas
de forma independiente por motor y por escritura** — divergencia silenciosa de datos, exactamente la
clase que la disciplina byte-idéntica prohíbe. La documentación (AGENTS.md §3, COMPATIBILITY.md) afirma
que el nodo "se puede usar en DEFAULT y en acciones de trigger", **subestimando** que el compilador
genérico (`compileExpression`) lo admite en CUALQUIER contexto de expresión (CHECK, WHERE, proyección de
vista, etc.).

**Recomendación:** rechazar `gen_random_uuid` (y todo nodo no determinista) fuera de los contextos
seguros documentados (DEFAULT de columna, valor de acción de trigger), o al menos fuera de CHECK/índice/
generada, con error explícito en `Schema.Validate` — en línea con la regla "garantía o rechazo
explícito, nunca degradación silenciosa". Nota: índice y generada ya quedan cubiertos por el rechazo
consistente de ambos motores; **el CHECK es el único contexto donde ambos aceptan** y por tanto el único
que hoy queda sin red.

---

## Parte 3 — Re-verificación del bloqueo de `WITH RECURSIVE`

### 3.1 El rechazo sigue vigente e intacto

`TestAudit10RecursiveDoor` (ejecutado):

```
WITH RECURSIVE rejected: WITH RECURSIVE is outside the canonical grammar: recursive CTE
  termination and ordering are not guaranteed identical between SQLite and PostgreSQL
lowercase 'with recursive' rejected: (mismo error)
```

Mayúsculas y minúsculas de la keyword se rechazan al parsear en
`compat/sqlselect.go::stripCatalogWith`.

### 3.2 El commit `d42a22c` no tocó el código

`git show --stat d42a22c` → **solo** 3 ficheros: `AGENTS.md`, `docs/COMPATIBILITY.md`,
`docs/reports/FEAT-RECURSIVECTE-REPORT.md`. **NO** modifica `compat/schema.go` ni `compat/ddl.go` (ni
`compat/sqlselect.go`). Verificado además que el árbol de trabajo actual no tiene cambios sin commitear
sobre los ficheros auditados (`git diff --stat` vacío en `compat/{ddl,sqlparse,sqlselect,schema}.go`,
`AGENTS.md`, `docs/COMPATIBILITY.md`).

### 3.3 Las citas apuntan correctamente

- `AGENTS.md` línea 167 cita `docs/reports/FEAT-RECURSIVECTE-REPORT.md` (el fichero existe).
- `docs/COMPATIBILITY.md` línea 31 cita `reports/FEAT-RECURSIVECTE-REPORT.md` (relativa correcta).
- Simétricamente, las citas de `gen_random_uuid` a `FEAT-RANDOMUUID-REPORT.md` (AGENTS.md §137,
  COMPATIBILITY.md §90) también resuelven. **Citas correctas.**

### 3.4 Puerta abierta: self-reference sin la keyword `RECURSIVE`

Pregunta del encargo: ¿un `WITH` no recursivo admite por error un self-reference que lo vuelva
"recursivo de facto" sin la palabra `RECURSIVE`? **Probado en parse y contra ambos motores reales.**

`TestAudit10RecursiveDoor` + `TestAudit10RecursiveDoorRealEngines` (ejecutados):

```
self-ref WITH (no RECURSIVE) PARSED into AST (with 1 CTE)
compile(sqlite)   = WITH "t" AS (SELECT "id" FROM "t") SELECT "id" FROM "t"
compile(postgres) = WITH "t" AS (SELECT "id" FROM "t") SELECT "id" FROM "t"

[SELF-REF WITH view DDL]  sqlite: accept=true   postgres: accept=true
QUERY sqlite:   rows=0  err=SQL logic error: circular reference: t (1)
QUERY postgres: rows=2  err=<nil>
>>> RUNTIME DIVERGENCE: sqlite queryable=false, postgres queryable=true
```

**Lectura:**
- **NO se vuelve recursivo de facto.** No hay recursión desbocada en ningún motor: SQLite rechaza el
  self-reference (`circular reference`), PostgreSQL lo resuelve **no recursivamente** ligando el `t`
  interior a la tabla base. El bloqueo de `WITH RECURSIVE` (el temor de terminación del reporte) **NO
  se elude**: sin la keyword, ningún motor recursa.
- **PERO destapa una divergencia distinta:** compat sólo guarda la keyword literal `RECURSIVE`, así que
  un CTE que se auto-referencia **sin** la keyword **parsea, compila y se aplica** — el mismo `CREATE
  VIEW` es aceptado por los dos motores, pero al consultarlo **SQLite falla (`circular reference: t`)
  mientras PostgreSQL devuelve filas** (liga a la tabla base). Mismo DDL byte-idéntico, comportamiento
  divergente en runtime. Ver Hallazgo M2.

---

### Hallazgo M2 — Self-reference en CTE no recursivo → divergencia de runtime (pre-existente) · **MEDIA**

**Evidencia (ejecutada, §3.4):** `WITH t AS (SELECT id FROM t) SELECT id FROM t` (con una tabla base
`t`) parsea y compila idéntico en ambos motores; ambos aceptan el `CREATE VIEW`; al consultar, SQLite
da `circular reference: t` (vista inutilizable) y PostgreSQL devuelve 2 filas. **Divergencia real** en
la clase que la disciplina byte-idéntica prohíbe (un motor produce datos, el otro error).

**Atribución honesta:** este gap **NO fue introducido por los dos commits auditados**. Es de la feature
de `WITH` no recursivo (commit `38224a8`). `d42a22c` sólo agregó documentación y `eb28b7e` es de
`gen_random_uuid`. El guard de `RECURSIVE` en sí **está intacto y es correcto**. Lo reporto porque el
encargo pidió explícitamente probar esta puerta: la guarda detecta la *keyword*, no la *propiedad*
(auto-referencia), y esa diferencia deja pasar un patrón divergente.

**Recomendación:** en `stripCatalogWith`/validación de vistas, detectar cuando un CTE referencia su
propio nombre en su cuerpo y rechazarlo (`self-referential CTE requires RECURSIVE, which is outside the
canonical grammar`), cerrando el patrón por *propiedad* y no sólo por keyword.

---

## Conteo final por severidad

| Severidad | # | Hallazgos |
|---|---|---|
| **ALTA** | 0 | — |
| **MEDIA** | 2 | **M1** `gen_random_uuid()` alcanzable y sin guarda en CHECK no determinista (ambos motores aceptan; nuevo por `eb28b7e`). **M2** self-reference en CTE sin `RECURSIVE` → divergencia runtime SQLite (error) vs PG (filas) — **pre-existente** (feature `WITH`, no de los commits auditados). |
| **BAJA** | 2 | **B1** índice de expresión con `gen_random_uuid()`: compat compila, ambos motores rechazan consistentemente (freno del motor, no de compat). **B2** columna generada STORED ídem. Ambos **sin divergencia**. |

### Áreas verificadas y LIMPIAS (explícito)
- Parser de `gen_random_uuid`: mayúsculas/espacios/whitespace aceptados por diseño; args rechazados. **Limpio.**
- Anidamiento `coalesce/lower/upper(gen_random_uuid())`: compila y es consistente en ambos motores. **Limpio.**
- 812 UUIDs (300 propios + 512 congelados) 100% v4 estrictos y únicos. **Limpio.**
- Copy de columna `NOT NULL DEFAULT gen_random_uuid()` SQLite→PG17: 5/5 v4 preservados. **Limpio.**
- Camino DEFAULT + trigger contra PG17 real: v4 válidos y únicos. **CERRADO.**
- Rechazo de `WITH RECURSIVE` (mayús/minús): vigente. `d42a22c` no tocó `schema.go`/`ddl.go`. Citas correctas. **Confirmado.**

## Veredicto global
- **gen_random_uuid: CERRADO** (ambos caminos documentados, congelados verdes contra motores reales).
- **WITH RECURSIVE: bloqueo confirmado e intacto**; el guard de la keyword no se elude hacia recursión.
- **2 MEDIA / 0 ALTA / 2 BAJA.** La única exposición *nueva* de los commits auditados es M1 (CHECK no
  determinista). M2 es un gap pre-existente destapado por la sonda de la puerta. Ninguno rompe el
  cierre de `gen_random_uuid` ni el bloqueo de `WITH RECURSIVE`.

## Limpieza
- Probes temporales `compat/zz_audit10_test.go` y `e2e/zz_audit10_test.go`: **borrados**.
- Sin binarios remanentes; sin procesos en foreground colgados; sin `git add/commit/stash`.
- Password del DSN siempre `***`. Bases PG efímeras (`compat_e2e_*`) dropeadas por el harness; 0 remanentes.
- `git status` final: sólo `docs/reports/AUDIT10-CONFIRM.md` como adición nueva.
