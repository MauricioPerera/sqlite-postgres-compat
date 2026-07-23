# FEAT-RANDOMUUID — Función canónica `gen_random_uuid()` (generador no determinista de UUID v4)

**Fecha:** 2026-07-23
**Ámbito:** agregar una función canónica NUEVA de cero argumentos, `gen_random_uuid()`, a la gramática de expresiones (`compat/sqlparse.go` → `compat/ddl.go`), utilizable en `Column.Default` y dentro del valor de una acción de trigger. Es el **primer nodo no determinista** de la gramática.
**Motor de PostgreSQL usado para verificación:** PostgreSQL 17 en `postgres://postgres:***@31.220.22.176:5434/postgres` (password enmascarada). El harness E2E (`TestMain`) crea y **dropea** una base efímera `compat_e2e_<nanos>`; no se crean bases nombradas adicionales (todas las tablas del test viven dentro de esa base efímera). 0 bases remanentes al terminar.
**Archivos tocados:** `compat/sqlparse.go`, `compat/sqlparse_test.go`, `compat/ddl.go`, `compat/ddl_test.go`, `e2e/system_test.go`, `README.md`, `docs/TESTING.md`, `docs/COMPATIBILITY.md`, `AGENTS.md`, este reporte. No se tocó `sqlselect.go`, `schema.go`, `inspect.go`, `store.go`, `capture.go`, `replicate.go`, `cmd/**`, ni reportes existentes.

---

## 1. El problema real y por qué NO se tradujo literal `hex(randomblob(16))`

Un experimento del PM encontró que un trigger típico de SaaS que genera un ID único inline con `hex(randomblob(16))` (idiom estándar de SQLite) **no compila hoy**: `hex` no está en la allowlist de funciones (AGENTS.md §3), y por diseño la capa rechaza cualquier función fuera de la allowlist en vez de degradar en silencio.

La solución **NO** es traducir literalmente `hex(randomblob(16))`:

- `randomblob`/`hex` son funciones de SQLite. **PostgreSQL de núcleo (sin extensiones) no tiene un generador de bytes aleatorios equivalente**: no hay un `randomblob(n)` estándar. Agregar `hex`/`randomblob` a la allowlist obligaría a inventar una traducción de PG frágil (p.ej. armar bytes con `md5(random()::text)` u otras piruetas) que además **no produce un UUID válido** (no fija los nibbles de versión/variante) y no es equivalente.
- El propósito real del idiom es "generar un identificador único". La forma canónica y portable de ese propósito es un **UUID v4**.

Por eso la solución es una **función canónica NUEVA de propósito equivalente**: `gen_random_uuid()`, cero argumentos, que cada motor implementa con su mejor primitiva nativa. No es una traducción sintáctica de `hex(randomblob(16))`; es la abstracción correcta del intento del autor.

`hex(randomblob(16))` sigue rechazándose exactamente igual que antes (`hex` no está en la allowlist).

---

## 2. Diseño

Patrón idéntico al existente: un `Kind` nuevo producido en `parseCatalogExpression` (rama de `catalogFunctionCall`) + su rama en `compileExpressionIn`.

### 2.1 Parser (`compat/sqlparse.go`)

`catalogFunctionCall("gen_random_uuid()")` devuelve `name="gen_random_uuid"`, `argument=""`. Se agrega el caso al `switch` de funciones:

```go
case "gen_random_uuid":
    if strings.TrimSpace(argument) != "" {
        return Expression{}, fmt.Errorf("gen_random_uuid takes no arguments")
    }
    return Expression{Kind: "gen_random_uuid"}, nil
```

- `gen_random_uuid()` → `Expression{Kind: "gen_random_uuid"}` (sin `Args`).
- `gen_random_uuid(1)` / `gen_random_uuid(a)` / `gen_random_uuid('x')` → error de aridad (paréntesis no vacíos), nunca un nodo válido. (Un nombre de función con argumentos que no matchea la allowlist ya caía en `unsupported catalog function`; acá matchea el nombre pero se rechaza por aridad — cualquiera de las dos vías deja el input fuera de la gramática, que es lo exigido.)

### 2.2 Compilador (`compat/ddl.go`)

```go
case "gen_random_uuid":
    return compileGenRandomUUID(engine)
```

- **PostgreSQL:** `gen_random_uuid()` — built-in del núcleo desde PG13. El proyecto exige PG17, así que no hay extensión (`pgcrypto`) ni version-gating. Verificado contra PG17 real.
- **SQLite:** no existe una función de núcleo que genere UUID, así que se emite una expresión inline que arma un v4 válido a partir de `randomblob`/`hex`:

```
(lower(hex(randomblob(4)) || '-' || hex(randomblob(2)) || '-4' || substr(hex(randomblob(2)), 2) || '-' || substr('89ab', 1 + abs(random() % 4), 1) || substr(hex(randomblob(2)), 2) || '-' || hex(randomblob(6))))
```

Construcción del layout 8-4-4-4-12 (RFC 4122 v4):

| Grupo | Fragmento | Salida |
|---|---|---|
| 8 | `hex(randomblob(4))` | 8 hex aleatorios |
| 4 | `hex(randomblob(2))` | 4 hex aleatorios |
| 4 | `'4' || substr(hex(randomblob(2)), 2)` | nibble de **versión** `4` + 3 hex |
| 4 | `substr('89ab', 1 + abs(random() % 4), 1) || substr(hex(randomblob(2)), 2)` | nibble de **variante** en `{8,9,a,b}` + 3 hex |
| 12 | `hex(randomblob(6))` | 12 hex aleatorios |

Detalles de correctitud:
- `lower(...)` porque `hex()` devuelve mayúsculas y un UUID canónico es minúscula.
- `random() % 4` queda en `[-3, 3]`, así que `abs(...)` **nunca** toca el int64 más negativo (cuyo `abs` desbordaría con `integer overflow` en SQLite); el índice de variante queda en el rango 1-based válido `[1, 4]`.
- **Paréntesis externos:** SQLite exige que un `DEFAULT` de columna que no sea literal esté envuelto en paréntesis (`DEFAULT (expr)`). Sin ellos, `ApplySchema` en SQLite falla con `near "(": syntax error`. Los paréntesis externos también son inofensivos en cualquier otro contexto (valor de trigger, `CHECK`). (Este fue el único ajuste sobre el diseño inicial, detectado al correr el E2E — ver §4.)

`Column.Default` es un `*Expression` compilado por `compileExpression` sin restricción de `Kind`, así que `DEFAULT gen_random_uuid()` en una columna `family=uuid` funciona sin tocar `schema.go`. La columna `uuid` mapea a `TEXT` en ambos motores; en PostgreSQL `gen_random_uuid()` devuelve tipo `uuid` y PG lo coacciona a `TEXT` por assignment cast (verificado contra PG real, §3.3).

---

## 3. Nota explícita de NO-DETERMINISMO (patrón distinto al resto de la gramática)

**Este es el único nodo de la gramática que NO cumple equivalencia byte-idéntica, y es correcto que así sea.** Todo el resto de la gramática exige que ambos motores emitan/almacenen exactamente lo mismo. `gen_random_uuid()` genera un valor aleatorio nuevo en cada evaluación: es **imposible** que SQLite y PostgreSQL produzcan el mismo valor, y no tendría sentido exigirlo.

La prueba de "equivalencia" para este nodo es distinta y está definida así:

- **(a) Ambos motores compilan y ejecutan la expresión sin error.** Verificado por `ApplySchema` verde en SQLite real y PG real (si la expresión de un motor fuera SQL inválido, fallaría acá).
- **(b) El valor generado en cada motor es un UUID v4 válido** — formato 8-4-4-4-12, nibble de versión `4`, nibble de variante en `{8,9,a,b}`. Verificado con un regex estricto sobre cada valor generado, en unit y E2E.
- **(c) Valores sucesivos son distintos entre sí** (sanity check de aleatoriedad, no un test de flakiness: se generan varios y se verifica que no se repiten).

No es una comparación "mismo valor en ambos motores" (eso no aplica). Documentado también en AGENTS.md §3 y docs/COMPATIBILITY.md.

---

## 4. Verificación (salida real)

### 4.1 `.\scripts\check.ps1` (gofmt + vet + unit tests) — VERDE

```
gofmt: OK
go vet: OK
go test: OK
?   	example.com/sqlite-postgres-compat/cmd/compat	[no test files]
ok  	example.com/sqlite-postgres-compat/cmd/internal/cliout	1.608s
ok  	example.com/sqlite-postgres-compat/compat	1.654s
```

`go vet -tags=e2e ./e2e` → sin salida, `VET_EXIT=0` (VERDE).

### 4.2 Unit tests congelados (parse + compile en ambos motores)

`compat/sqlparse_test.go` y `compat/ddl_test.go`:

```
=== RUN   TestCompileGenRandomUUIDForBothEngines
--- PASS: TestCompileGenRandomUUIDForBothEngines (0.01s)
=== RUN   TestParseCatalogExpressionGenRandomUUID
--- PASS: TestParseCatalogExpressionGenRandomUUID (0.00s)
PASS
ok  	example.com/sqlite-postgres-compat/compat	2.358s
```

- **Parse** (`TestParseCatalogExpressionGenRandomUUID`): `gen_random_uuid()` → `Expression{Kind:"gen_random_uuid"}`; `gen_random_uuid(1)`, `gen_random_uuid(a)`, `gen_random_uuid('x')` rechazados por aridad.
- **Compile** (`TestCompileGenRandomUUIDForBothEngines`): SQL exacto congelado por motor:
  - PostgreSQL: `gen_random_uuid()` (literal, built-in nativo).
  - SQLite: la expresión completa `(lower(hex(randomblob(4)) || '-' || ... || hex(randomblob(6))))`. Además el test **ejecuta** esa expresión 512 veces contra el driver modernc y verifica con regex estricto que cada resultado es un v4 válido y único (aleatoriedad + validez a nivel unit).

### 4.3 Equivalencia contra PostgreSQL 17 REAL + SQLite real (E2E)

`TestSystemGenRandomUUIDGeneratesValidV4OnBothEngines` (tag `e2e`, en `e2e/system_test.go`). Ejerce **ambos caminos de uso** exigidos:

1. **Column DEFAULT:** tabla `randomuuid_defaulted` con `id` `family=uuid` y `DEFAULT gen_random_uuid()`. Se insertan 8 filas **sin** especificar `id` en SQLite real y PG real; cada `id` generado por el default debe ser un v4 válido y único.
2. **Trigger action:** trigger `AFTER INSERT` sobre `randomuuid_events` cuya acción `INSERT INTO randomuuid_tokens VALUES (gen_random_uuid(), NEW.id)` usa la función como valor. Se insertan 8 eventos; cada `token` escrito por el trigger debe ser un v4 válido y único.

Un `ApplySchema` verde en PG real prueba que PG acepta el DDL compilado (su `gen_random_uuid()` nativo, y la coacción `uuid`→`TEXT`); un `ApplySchema` verde en SQLite prueba que la expresión `randomblob`/`hex` es SQL válido allí.

Ejecutado DE VERDAD contra el DSN dado:

```
=== RUN   TestSystemGenRandomUUIDGeneratesValidV4OnBothEngines
--- PASS: TestSystemGenRandomUUIDGeneratesValidV4OnBothEngines (2.07s)
PASS
ok  	example.com/sqlite-postgres-compat/e2e	5.220s
```

Antes de escribir el E2E se corrió un probe descartable (borrado) que confirmó la única viabilidad no obvia: PostgreSQL acepta `CREATE TABLE ... (id TEXT DEFAULT gen_random_uuid())` y coacciona `uuid`→`TEXT` automáticamente, generando v4 válidos. Ese probe detectó también, al primer intento del E2E, que SQLite exige `DEFAULT (expr)` con paréntesis (ver §2.2), lo que motivó envolver la expresión SQLite en paréntesis externos.

### 4.4 Suite E2E completa (sin regresiones)

`go test -tags=e2e ./e2e -count=1` contra PG real:

```
--- FAIL: TestSystemClaimsExactCoverageForRequiredFeatureFamilies (0.00s)
    --- FAIL: .../foreign_keys      system does not provide exact coverage: status=unknown reason=requires parser and semantic compiler
    --- FAIL: .../check_constraints  status=unknown reason=requires parser and semantic compiler
    --- FAIL: .../indexes           status=unknown reason=requires parser and semantic compiler
    --- FAIL: .../triggers          status=unknown reason=requires parser and semantic compiler
    --- FAIL: .../views             status=unknown reason=requires parser and semantic compiler
    --- FAIL: .../stored_routines   status=unknown reason=requires parser and semantic compiler
    --- FAIL: .../full_text         status=unknown reason=requires parser and semantic compiler
FAIL
FAIL	example.com/sqlite-postgres-compat/e2e	75.277s

# Conteo de veredictos de nivel superior en la corrida completa:
#   grep -cE "^--- PASS: Test" -> 47
#   grep -cE "^--- FAIL: Test" -> 1  (solo el gate del 100 %, por diseño)
```

Conteo de pruebas de nivel superior actualizado a **48** (era 47) en README.md y docs/TESTING.md, verificado con `grep -rhE "^func Test[A-Z]" e2e/*.go | grep -v TestMain | wc -l` = 48. 47 superadas + 1 fallida de forma intencional (`TestSystemClaimsExactCoverageForRequiredFeatureFamilies`, el gate del 100 % que sigue rojo por diseño). Sin ninguna otra falla. 0 bases temporales remanentes en PG.

---

## 5. Docs actualizadas en la misma tarea

- **AGENTS.md §3:** `gen_random_uuid` agregado a la tabla de allowlist (aridad cero) con una nota extensa de no-determinismo (única excepción a byte-identical; por qué no se traduce literal `hex(randomblob(16))`; usable en `DEFAULT` y en acciones de trigger).
- **docs/COMPATIBILITY.md:** `gen_random_uuid` en la allowlist y un párrafo dedicado al patrón no determinista (formato v4, mapeo por motor, criterio de equivalencia formato/unicidad).
- **README.md / docs/TESTING.md:** conteo E2E 47 → 48.

Cero claims de que el binario cumpla algo que no cumpla: se documenta explícitamente que este nodo NO es byte-idéntico y por qué eso es correcto.

---

## 6. Limpieza y reglas

- Probes temporales (`compat/zz_uuidprobe_test.go`, `e2e/zz_probe_test.go`): **borrados**. `git status` sólo muestra los archivos permitidos + este reporte.
- Sin procesos en foreground que no terminen solos. Nada escrito fuera del repo. No se hizo `git add/commit/stash`. Password del DSN nunca en literal (enmascarada `***`). 0 bases temporales remanentes en PostgreSQL.
