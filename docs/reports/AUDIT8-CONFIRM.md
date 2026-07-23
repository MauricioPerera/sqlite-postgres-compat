# AUDIT8-CONFIRM — Ronda de confirmación de cierre AUDIT7 MEDIA

Fecha: 2026-07-23
Auditor: independiente, sin memoria previa.
Repo: `sqlite-postgres-compat`, branch `main`, HEAD `7d96737` (commit de fix: `2073bbf`).
Árbol limpio al inicio y al final (verificado con `git status --short`).
PG real usado: `postgres://postgres:***@31.220.22.176:5434/postgres?sslmode=disable` (password enmascarado; el valor real solo vivió en `$env:COMPAT_POSTGRES_DSN` de la sesión, nunca se logueó ni se escribió a disco).

## Resumen

Los 3 hallazgos MEDIA de AUDIT7 (A-H1, C-H1, C-H2) están **CERRADOS**. Se
reprodujeron con las repros originales sobre HEAD, con salida real pegada
abajo. El barrido de ataque al código nuevo del fix (whitespace del parser
con newline/runs múltiples/string-literals/interacción con `GROUP BY`;
precedencia de DSN en `test-system.ps1` en sus 3 ramas; bordes del parseo
`-json`) no encontró hallazgos nuevos. `FIX-A7-REPORT.md` es preciso: no se
encontró ninguna afirmación que el código no cumpla.

## 1. Tabla de confirmación de cierre (3/3)

| Hallazgo | Repro original | Resultado en HEAD | Veredicto |
|---|---|---|---|
| **A-H1** (AUDIT7-A) — `IS NULL`/`IS NOT NULL` rechazan whitespace múltiple/tabs en expresiones de catálogo (CHECK/índice/vista/trigger WHEN) | `parseCatalogExpression` sobre `"col IS NULL"`, `"col IS  NULL"`, `"col IS  NOT  NULL"`, `"col IS\tNOT\tNULL"` y trigger `WHEN new.x IS  NOT  NULL` | Los 4 casos de expresión y el WHEN de trigger parsean y compilan sin error, idéntico a un solo espacio, en SQLite y Postgres (ver §2) | **CERRADO** |
| **C-H1** (AUDIT7-C) — `docs/COMPATIBILITY.md` tabla de valores dice Date→Postgres `DATE`, pero el código mapea a `TEXT` | Lectura de `docs/COMPATIBILITY.md` línea de Date vs `compat/ddl.go:159` (`case DateType`) | Doc dice `Date | TEXT | TEXT | Preserva el valor canónico exacto (DATE nativo haría que pgx devuelva time.Time y se pliegue a timestamp, divergiendo del origen)`; coincide exactamente con `ddl.go:159` (`DateType` → `"TEXT"` en Postgres) | **CERRADO** |
| **C-H2** (AUDIT7-C) — `scripts/test-system.ps1` siempre terminaba `exit 1` (gate no-funcional) | Corrida real con DSN por ENV VAR (sin flag) → se esperaba `exit 0`; corrida con `-E2ERun` que excluya el test intencional → se esperaba `exit 1` con mensaje claro | Ver §3: `exit 0` en el caso positivo (detecta el único FAIL intencional), `exit 1` con mensaje explícito en el caso negativo | **CERRADO** |

## 2. Repro A-H1 (programa temporal Go, borrado al terminar)

Archivo temporal `compat/zz_audit8_confirm_test.go` (paquete `compat`, acceso
a símbolos no exportados `parseCatalogExpression`/`compileExpression`/
`parseCatalogSelect`/`compileSelect`), borrado tras la corrida:

```go
cases := []string{
    "col IS NULL",
    "col IS  NULL",
    "col IS  NOT  NULL",
    "col IS\tNOT\tNULL",
}
for _, in := range cases {
    expr, err := parseCatalogExpression(in)
    if err != nil { fmt.Printf("ERR %q -> %v\n", in, err); continue }
    s, _ := compileExpression(SQLite, expr)
    p, _ := compileExpression(Postgres, expr)
    fmt.Printf("%q -> SQLite=%s PG=%s\n", in, s, p)
}
```

Salida real (`go test ./compat/ -run TestZZAudit8ConfirmWhitespace -v`):

```
"col IS NULL" -> SQLite=("col" IS NULL) PG=("col" IS NULL)
"col IS  NULL" -> SQLite=("col" IS NULL) PG=("col" IS NULL)
"col IS  NOT  NULL" -> SQLite=("col" IS NOT NULL) PG=("col" IS NOT NULL)
"col IS\tNOT\tNULL" -> SQLite=("col" IS NOT NULL) PG=("col" IS NOT NULL)
```

Ningún `ERR` — los 4 casos que en AUDIT7-A fallaban con `unsupported catalog
expression` ahora parsean y compilan correctamente. Confirmado también en
`WHEN` de trigger vía la misma ruta (`catalogOperatorSpan`, compartida por
`parseCatalogExpression` y `parseSQLiteCatalogTrigger`/
`parsePostgresCatalogTrigger`, que reutilizan `parseCatalogExpression`
internamente).

Se corrió `go test ./compat/... ` completo tras el barrido (§4.1 de
`FIX-A7-REPORT.md` ya lo documentaba en verde); no se repite aquí por
brevedad, solo se confirma que sigue en verde (ver §5 de este reporte).

## 3. Repro C-H2: `scripts/test-system.ps1` (corridas reales, ~1-2 min cada una)

### 3.1 Caso positivo — DSN por ENV VAR, sin flag

```
$env:COMPAT_POSTGRES_DSN = "postgres://postgres:***@31.220.22.176:5434/postgres?sslmode=disable"
& .\scripts\test-system.ps1
```

Salida real:
```
?    example.com/sqlite-postgres-compat/cmd/compat  [no test files]
ok   example.com/sqlite-postgres-compat/cmd/internal/cliout  (cached)
ok   example.com/sqlite-postgres-compat/compat  (cached)
test-system: OK - the only e2e failure is the documented baseline 'TestSystemClaimsExactCoverageForRequiredFeatureFamilies' (docs/reports/AUDIT7-C.md, finding H2).
EXITCODE=0
```

**`exit 0`, sin flag, solo con la env var.**

### 3.2 Caso negativo — `-E2ERun` que excluye el test intencional

```
& .\scripts\test-system.ps1 -E2ERun 'TestDatabaseDSNPreservesConnectionParameters'
```

Salida real:
```
?    example.com/sqlite-postgres-compat/cmd/compat  [no test files]
ok   example.com/sqlite-postgres-compat/cmd/internal/cliout  (cached)
ok   example.com/sqlite-postgres-compat/compat  (cached)
test-system: expected intentional failure 'TestSystemClaimsExactCoverageForRequiredFeatureFamilies' did not fail (it passed or did not run).
Either exact coverage improved for a previously-unknown feature family (update docs/COMPATIBILITY.md and this script's baseline), or the -run filter excluded it, or something else changed. Investigate before treating this as green.
EXITCODE=1
```

**`exit 1`, mensaje claro y accionable.**

## 4. Ataque al código nuevo del fix — hallazgos nuevos

### 4.1 Whitespace del parser (`catalogOperatorSpan`, `sqlparse.go`)

Barrido adicional (mismo harness temporal, borrado):

```go
viewCases y exprCases adicionales:
"col IS\nNULL"                          // newline entre IS y NULL
"col IS   NOT     NULL"                 // runs múltiples, ancho variable
"col = 'x IS  NULL' AND y IS NULL"      // literal de string con "IS  NULL" dentro
"col IS  NULL AND other = 1"            // interacción con AND
"CREATE VIEW v AS\nSELECT a FROM t"     // AS SELECT con newline
"CREATE VIEW v AS  SELECT a FROM t GROUP  BY a"  // AS SELECT + GROUP BY en el mismo SELECT
```

Salida real:
```
"col IS\nNULL" -> SQLite=("col" IS NULL) PG=("col" IS NULL)
"col IS   NOT     NULL" -> SQLite=("col" IS NOT NULL) PG=("col" IS NOT NULL)
"col = 'x IS  NULL' AND y IS NULL" -> SQLite=(("col" = 'x IS  NULL') AND ("y" IS NULL)) PG=(("col" = 'x IS  NULL') AND ("y" IS NULL))
"col IS  NULL AND other = 1" -> SQLite=(("col" IS NULL) AND ("other" = 1)) PG=(("col" IS NULL) AND ("other" = 1))
DEF "CREATE VIEW v AS\nSELECT a FROM t" -> SQLite=SELECT "a" FROM "t"
DEF "CREATE VIEW v AS  SELECT a FROM t GROUP  BY a" -> SQLite=SELECT "a" FROM "t" GROUP BY "a"
```

- **Newline tolerado, y es correcto tolerarlo**: se confirmó contra SQLite
  real (`modernc.org/sqlite`, `:memory:`) que `SELECT 1 WHERE 1 IS\nNULL`
  ejecuta sin error (0 filas, resultado semánticamente correcto de `1 IS
  NULL` = falso). El mecanismo usa `unicode.IsSpace`, que incluye `\n`, `\t`,
  `\r` y espacio — coherente con el tokenizador de SQLite, que trata
  cualquier whitespace como separador equivalente.
- **Runs múltiples de ancho variable**: tolerados sin problema (`catalogOperatorSpan` no asume un ancho fijo, escanea whitespace hasta la palabra anterior).
- **Literales de string que contienen "IS  NULL"**: NO afectados — el
  string `'x IS  NULL'` se preserva verbatim como literal; el escaneo
  derecha-a-izquierda de `splitCatalogOperator` respeta `inSingle`/`inDouble`
  y no interpreta contenido entre comillas como operador.
- **Interacción con `GROUP BY` en el mismo SELECT**: `AS  SELECT` (whitespace
  tolerante) y `GROUP  BY` (whitespace tolerante, mecanismo preexistente) en
  la misma definición de vista compilan correctamente juntos, sin
  interferencia entre `topLevelKeyword("AS SELECT")` y `topLevelKeyword("GROUP
  BY")`.

**Área limpia — sin hallazgos nuevos en el whitespace del parser.**

### 4.2 `test-system.ps1` — precedencia del DSN (3 ramas, corridas reales)

| Rama | Comando | Resultado real |
|---|---|---|
| Flag explícito gana sobre env var | `$env:COMPAT_POSTGRES_DSN` puesto a un DSN **bogus** (`postgres://bogususer:bogus@127.0.0.1:1/nonexistent`); `.\scripts\test-system.ps1 -PostgresDsn <DSN real>` | `exit 0`, usó el DSN del flag (el bogus habría fallado de inmediato) |
| Env var sola se respeta | `$env:COMPAT_POSTGRES_DSN` = DSN real, sin flag | `exit 0` (ya mostrado en §3.1) |
| Sin nada → localhost, falla limpio | Subproceso `pwsh` nuevo con `Remove-Item Env:\COMPAT_POSTGRES_DSN` antes de correr el script (sin flag) | `exit 1`, mensaje `"test-system: no test results parsed from the e2e run (build or setup failure before any test ran)."` + output crudo con el error real de conexión (`dial tcp 127.0.0.1:5432 ... connectex: ... denegó expresamente dicha conexión`) |

Salida real de la 3ª rama (subproceso limpio):
```
test-system: no test results parsed from the e2e run (build or setup failure before any test ran).
Raw go test output:
failed to connect to `user=Administrador database=postgres`: 127.0.0.1:5432 (127.0.0.1): dial error: dial tcp 127.0.0.1:5432: connectex: No se puede establecer una conexión ya que el equipo de destino denegó expresamente dicha conexión.
FAIL	example.com/sqlite-postgres-compat/e2e	2.335s
EXITCODE=1
```

Mensaje claro (no críptico): explica que no se parseó ningún resultado y
adjunta la razón real (fallo de conexión a Postgres) en el output crudo.

**Área limpia — las 3 ramas de precedencia funcionan como se espera.**

### 4.3 Bordes del parseo `-json`: build/timeout

Por lectura de `scripts/test-system.ps1` (§ líneas 39-50, 52-60): el loop
solo cuenta un evento si trae `.Test` (nombre de test). Un fallo de
**build/setup antes de correr ningún test** (paquete no compila, DSN
inválido rechazado antes de `TestMain`, regex de `-run` inválida) emite
eventos JSON de nivel paquete con `Action: "fail"` pero **sin** `.Test`, así
que `$results.Count` queda en `0` y cae en la rama explícita de "no se pudo
parsear ningún resultado" → `exit 1` con el output crudo volcado
(`$evt.Output`), no se pierde información.

Se simuló, sin tocar el repo, usando el propio parámetro `-E2ERun` con una
regex inválida (`-run` mal formado ≈ fallo de setup antes de correr tests,
análogo estructuralmente a un fallo de build en cuanto a que no emite
eventos `Test`):

```
& .\scripts\test-system.ps1 -E2ERun '[invalid('
```

Salida real:
```
test-system: no test results parsed from the e2e run (build or setup failure before any test ran).
Raw go test output:
testing: invalid regexp for element 0 of -test.run ("[invalid("): error parsing regexp: missing closing ]: `[invalid(`
FAIL	example.com/sqlite-postgres-compat/e2e	2.824s
EXITCODE=1
```

Confirma por evidencia ejecutada (no solo lectura) que esta rama reporta
`exit 1` con detalle, no lo pierde.

**Timeout**: por lectura de código, `go test -json` reporta un evento
`Action: "fail"` **con `.Test`** para el test que estaba corriendo cuando
`-timeout` expira (Go asocia el panic de timeout al test en curso), más un
evento de nivel paquete. Ese test entra a `$results` como `fail` y, al no
ser `$IntentionalFailTest`, cae en `$unexpectedFailures` → `exit 1` con el
nombre del test listado. No se simuló ejecutando un timeout real (habría
requerido tocar el repo para insertar un `sleep`, prohibido por el alcance
READ-ONLY); el análisis por lectura es consistente con el comportamiento
documentado de `go test -json` y con el caso de build/setup ya verificado
empíricamente arriba (misma rama de código para "sin `.Test`", rama distinta
pero mecánicamente idéntica para "con `.Test` = fail").

**Área limpia por lectura + evidencia parcial ejecutada — sin hallazgos.**

### 4.4 ¿`FIX-A7-REPORT.md` afirma algo que el código no cumpla?

Se contrastó cada afirmación del reporte contra el diff real del commit
`2073bbf` (`git show --stat` + `git diff 2073bbf~1 2073bbf`) y contra el
código en HEAD:

- Descripción de `catalogOperatorSpan` (fixed-width para operadores sin
  espacio, escaneo derecha-a-izquierda tolerante para multi-palabra) →
  coincide exactamente con el código (`sqlparse.go:273-306`).
- Descripción de `stripCatalogSelectHeader` usando `topLevelKeyword`/
  `keywordMatchSpan` → coincide (`sqlselect.go:78,82`).
- Barrido de sección 3 (tabla de sitios con mecanismo tolerante, `ddl.go` no
  necesita tolerancia por ser compilador de salida) → verificado por lectura
  de `ddl.go`, correcto: son concatenaciones de string fijas emitidas por el
  propio compilador, nunca vuelven a parsear su propia salida.
- Descripción del fix de `test-system.ps1` (4 puntos: `-json`, dict
  `Test→pass/fail` solo top-level, lógica de veredicto de 4 ramas,
  `-E2ERun`) → coincide con el diff real y con las corridas de este reporte.
- Verificación 4.1/4.2/4.3 del reporte (`check.ps1` verde, caso positivo
  `exit 0`, caso negativo `exit 1`) → reproducido de nuevo en este reporte
  con el mismo resultado (§3).
- Trade-offs de sección 5: descriptivos, no afirman comportamiento
  verificable adicional.

**No se encontró ninguna afirmación del reporte que el código no cumpla.**

## 5. Verificación de regresión

```
$ go build ./...     → OK (sin salida)
$ go vet ./...        → OK (sin salida)
$ go test ./...        → OK (compat, cmd/internal/cliout en verde)
```

## Áreas limpias (explícitas)

- Whitespace del parser de catálogo (`catalogOperatorSpan`,
  `keywordMatchSpan`): newline, runs múltiples, literales de string, e
  interacción `AS SELECT` + `GROUP BY` en la misma sentencia — todo correcto.
- `scripts/test-system.ps1`: las 3 ramas de precedencia del DSN (flag > env
  var > default localhost) funcionan como se documentó, con corridas reales.
- Rama de "no se pudo parsear ningún resultado" (`build/setup failure`):
  confirmada con evidencia ejecutada (regex de `-run` inválida), reporta
  `exit 1` con detalle, no lo pierde.
- `docs/reports/FIX-A7-REPORT.md`: sin afirmaciones que el código no cumpla.

## Conteo final

- Hallazgos MEDIA de AUDIT7 confirmados **CERRADOS: 3/3**.
- Hallazgos nuevos de esta ronda (AUDIT8): **ALTA: 0, MEDIA: 0, BAJA: 0**.

## Limpieza

- Temporales borrados: `compat/zz_audit8_confirm_test.go`,
  `compat/zz_audit8_sqlite_test.go` (ambos verificados eliminados).
- No se creó ninguna base de datos temporal manual en Postgres (las corridas
  de `test-system.ps1` usan su propio `TestMain` de `e2e/`, que crea y
  dropea `compat_e2e_<nanos>` por sí solo; no se usó prefijo `audit8_`
  porque no hizo falta crear bases manuales para esta ronda).
- Ningún proceso quedó corriendo en foreground sin terminar.
- `git status --short` final: solo este reporte dentro del repo (los
  demás `??` listados por git pertenecen a directorios ajenos fuera del
  repo, no tocados).
