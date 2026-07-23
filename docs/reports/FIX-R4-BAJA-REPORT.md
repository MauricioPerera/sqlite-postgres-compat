# FIX-R4 — Cierre de las 2 BAJA accionables de AUDIT4

Dev efímero, sin memoria previa. HEAD de partida `a8c925f` (rama `main`), árbol
limpio. Dos fixes independientes sobre los hallazgos BAJA de la ronda 4.

- **Fix #1 (AUDIT4-A BAJA-N1):** `GROUP BY`/`ORDER BY` con operando vacío se
  aceptaban en silencio y la cláusula se descartaba al emitir.
- **Fix #2 (AUDIT4-B BAJA-N1):** el cambio de formato del journal (FIX-R1,
  marcador en la rama `typeof='real'` de `DECIMAL`) creó una frontera de versión
  de herramienta no documentada: un journal in-flight capturado por triggers
  pre-fix, aplicado con código nuevo, diverge (verify lo detecta, no es
  silencioso). Falta la nota operativa de upgrade/compatibilidad.

ARCHIVOS tocados (sólo estos): `compat/sqlselect.go`, `compat/sqlselect_test.go`,
`AGENTS.md`, `docs/USAGE.md`. Código y comentarios en inglés; docs en el
idioma/registro de cada archivo (AGENTS.md inglés machine-facing, USAGE.md
español). No se ejecutó `git add`/`commit` (regla).

---

## Fix #1 — Operando vacío tras keyword de cláusula → error de parseo

### Qué pasaba

`applyCatalogClauses` (`compat/sqlselect.go`) calcula el span de cada cláusula
con `keywordMatchSpan` y le pasa el texto recortado al handler. Cuando la
keyword cae al final del string (operando vacío), el valor resulta `""`:

- `GROUP BY` / `ORDER BY`: `splitTopLevelComma("")` devuelve `[]`, el loop no
  ejecuta, `GroupBy`/`OrderBy` queda vacío **sin error**. Al emitir
  (`compileSelect`), `len(...) > 0` es falso → la cláusula se **descarta
  silenciosamente**. El SQL inválido se acepta y se traduce como si la cláusula
  no existiera.
- `HAVING` / `WHERE`: ya erroraban (`parseCatalogExpression("")` → `empty
  expression`).
- `LIMIT` / `OFFSET`: ya erroraban (`strconv.Atoi("")`).

Inconsistencia: sólo `GROUP BY`/`ORDER BY` aceptaban el operando vacío en
silencio. SQLite y Postgres rechazan `GROUP BY`/`ORDER BY` sin operandos como
error de sintaxis.

### Fix

Un solo punto de control en `applyCatalogClauses`, antes de invocar el handler:
si el valor recortado es `""`, devolver un error de parseo claro y consistente
con el estilo del archivo:

```go
value := strings.TrimSpace(remainder[start:end])
// A clause keyword with no operand (e.g. "GROUP BY" at end of string) is a
// syntax error in SQLite and Postgres; reject it explicitly rather than
// accepting an empty clause that the emitter would silently discard.
if value == "" {
    return fmt.Errorf("SELECT %s clause has no operand", clauses[current.index].keyword)
}
if err := clauses[current.index].apply(value); err != nil { ... }
```

Mensaje: `SELECT GROUP BY clause has no operand` (y análogos para cada keyword).
El check es **uniforme** para las 6 cláusulas (`WHERE`, `GROUP BY`, `HAVING`,
`ORDER BY`, `LIMIT`, `OFFSET`), así cubre todas de una sola vez y deja
redundante (pero inofensiva) la rechaza que `HAVING`/`WHERE`/`LIMIT`/`OFFSET`
ya hacían por su cuenta. El handler ya no recibe `""`.

### Tests nuevos

`TestParseCatalogSelectRejectsEmptyClauseOperand` (en
`compat/sqlselect_test.go`), un subtest por cláusula con operando vacío, más
dos bordes:

| Subtest | Input | Esperado |
|---|---|---|
| GROUP BY empty | `SELECT a FROM t GROUP BY` | err `no operand` |
| GROUP BY empty extra whitespace | `SELECT a FROM t GROUP  BY ` | err `no operand` |
| ORDER BY empty | `SELECT a FROM t ORDER BY` | err `no operand` |
| ORDER BY empty extra whitespace | `SELECT a FROM t ORDER\tBY\t` | err `no operand` |
| HAVING empty | `SELECT a FROM t HAVING` | err `no operand` |
| WHERE empty | `SELECT a FROM t WHERE` | err `no operand` |
| LIMIT empty | `SELECT a FROM t LIMIT` | err `no operand` |
| OFFSET empty | `SELECT a FROM t OFFSET` | err `no operand` |
| LIMIT empty with OFFSET | `SELECT a FROM t LIMIT 10 OFFSET` | err `no operand` |
| GROUP BY empty with ORDER BY operand | `SELECT a FROM t GROUP BY ORDER BY b` | err `no operand` |

El último borde verifica que `GROUP BY ORDER BY b` se rechaza por el operando
vacío de `GROUP BY` (el span de `GROUP BY` se corta en la posición de `ORDER
BY`, dejando `""`), en vez de tomar `ORDER BY b` como operando de `GROUP BY`.

Los casos válidos existentes (`TestParseCatalogSelectCommonView`,
`...WithJoinAndAggregation`, `...RejectsNegativeLimit/Offset`,
`...AllowsZeroLimitAndOffset`, `...ToleratesKeywordInternalWhitespace`,
`...KeywordWhitespaceDoesNotReachStringLiterals`, `...JoinInternalWhitespace`)
siguen verdes: el check sólo dispara con valor vacío.

### Trade-offs

- **Check uniforme vs. localizado.** Se eligió un check único en
  `applyCatalogClauses` en lugar de tocar sólo `GROUP BY`/`ORDER BY`. Costo:
  los mensajes de error para `HAVING`/`WHERE`/`LIMIT`/`OFFSET` vacíos cambian
  de `empty expression` / `strconv.Atoi` a `SELECT <kw> clause has no operand`.
  Beneficio: una sola fuente de verdad, consistencia con el estilo de errores
  del archivo, y cobertura real de las 6 cláusulas (no sólo las dos que
  fallaban). No hay tests previos que afirmen el mensaje viejo para esos casos
  vacíos, así que no rompe nada.
- **No se toca `compileSelect` ni `ddl.go`.** El fix es de parseo (rechazo
  temprano), no de emisión. La rama `len(...) > 0` en `compileSelect` queda
  intacta como defensa, pero ahora es inalcanzable para el operando vacío
  porque el parser ya erroró antes.

---

## Fix #2 — Nota de upgrade/compatibilidad (frontera de versión del marcador)

### Qué pasaba

FIX-R1 (commit `6e9c2f6` / `4225126`) cambió el formato del journal: el trigger
de captura emite `realDecimalMarker || printf('%!.17g', col)` **sólo** en la
rama `typeof='real'` de `DECIMAL`. Los triggers de una versión anterior emiten
esa rama **sin** marcador. Consecuencia (verificada en AUDIT4-B §d): un journal
capturado por triggers pre-fix, aplicado con `canonicalValue` nuevo a un destino
canónico `TEXT`, diverge para decimales REAL-stored de alta magnitud (17
dígitos) — `ApplyChanges` no errora, pero `VerifySnapshots` reporta
`Equivalent == false`. **No es silencioso** (verify lo atrapa) y no afecta a
valores de forma corta (`0.1` coincide). El requisito operativo de
**re-instalar** la captura tras actualizar la herramienta, y de **no** aplicar un
journal in-flight pre-actualización con código nuevo, no estaba documentado.

### Fix (sólo docs)

No se toca código. Se agrega la nota operativa en dos archivos, en el registro
de cada uno:

- **`AGENTS.md`** (inglés, machine-facing): nueva **sección 10 "Upgrade
  compatibility — capture trigger versioning"** (antes de "Interface note"),
  con tres puntos: reinstall de `InstallChangeCapture` tras actualizar;
  drenar/descartar journals in-flight pre-upgrade; la divergencia la detecta
  `verify` (no hay corrupción silenciosa). Además, dado que la sección 7
  "Always rejected" transcribe los rechazos del parser, se agrega un bullet por
  el nuevo rechazo de Fix #1 (`SELECT <keyword> clause has no operand`).
- **`docs/USAGE.md`** (español): nueva subsección **"Compatibilidad al
  actualizar la herramienta (reinstall de la captura)"** dentro de "Replicación
  incremental automática", tras el bloque de catch-up tolerante, con los mismos
  tres puntos en español.

La palabra clave para el grep de definición de hecho es **`reinstall`**
(aparece en ambos archivos; USAGE.md la incluye como glosa entre paréntesis
junto a "reinstalar" para mantener un único token grepable en ambos):

```
grep -n "reinstall" AGENTS.md docs/USAGE.md
AGENTS.md:296:- **Reinstall capture after upgrading.** ...
docs/USAGE.md:161:### Compatibilidad al actualizar la herramienta (reinstall de la captura)
```

### Trade-offs

- **Docs, no código.** AUDIT4-B recomendaba explícitamente una nota operativa
  ("Bastaría una nota operativa"). No se altera `canonicalValue` ni se añade
  migración automática de journals viejos: el escenario mixto de versión está
  fuera del contrato de migración documentado (el catch-up drena el journal
  antes de cambiar el código). Codificar retrocompatibilidad habría sido un
  alcance mayor, arriesgado y no pedido.
- **Lenguaje por archivo.** AGENTS.md quedó en inglés (su registro
  machine-facing); USAGE.md en español. Para que un único grep pegue en ambos,
  USAGE.md incluye el token inglés `reinstall` como glosa entre paréntesis, sin
  alterar el registro español del cuerpo.
- **Bullet extra en AGENTS.md §7.** Se documentó también el rechazo de Fix #1
  para que el spec siga siendo fiel al código ("the code is the source of
  truth"). Es un bullet on-topic, no una variante de alcance.

---

## Definición de hecho

- [x] Tests nuevos por cada cláusula con operando vacío → error (10 subtests,
  todos verdes).
- [x] Casos válidos existentes siguen verdes.
- [x] `go build ./...` verde (salida real más abajo).
- [x] `go vet ./...` verde.
- [x] `go test ./compat/ -count=1` verde (salida real más abajo).
- [x] `grep -n "reinstall" AGENTS.md docs/USAGE.md` con hit en ambos.
- [x] `.\scripts\check.ps1` verde (salida real más abajo).

### Evidencia ejecutada (salida real)

`go build ./... && go vet ./...`:
```
BUILD_OK
VET_OK
```

`go test ./compat/ -count=1`:
```
ok  	example.com/sqlite-postgres-compat/compat	2.373s
```

`go test ./compat/ -run TestParseCatalogSelectRejectsEmptyClauseOperand -v -count=1`:
```
=== RUN   TestParseCatalogSelectRejectsEmptyClauseOperand
=== RUN   TestParseCatalogSelectRejectsEmptyClauseOperand/GROUP_BY_empty
=== RUN   TestParseCatalogSelectRejectsEmptyClauseOperand/GROUP_BY_empty_extra_whitespace
=== RUN   TestParseCatalogSelectRejectsEmptyClauseOperand/ORDER_BY_empty
=== RUN   TestParseCatalogSelectRejectsEmptyClauseOperand/ORDER_BY_empty_extra_whitespace
=== RUN   TestParseCatalogSelectRejectsEmptyClauseOperand/HAVING_empty
=== RUN   TestParseCatalogSelectRejectsEmptyClauseOperand/WHERE_empty
=== RUN   TestParseCatalogSelectRejectsEmptyClauseOperand/LIMIT_empty
=== RUN   TestParseCatalogSelectRejectsEmptyClauseOperand/OFFSET_empty
=== RUN   TestParseCatalogSelectRejectsEmptyClauseOperand/LIMIT_empty_with_OFFSET
=== RUN   TestParseCatalogSelectRejectsEmptyClauseOperand/GROUP_BY_empty_with_ORDER_BY_operand
--- PASS: TestParseCatalogSelectRejectsEmptyClauseOperand (0.00s)
    --- PASS: .../GROUP_BY_empty (0.00s)
    --- PASS: .../GROUP_BY_empty_extra_whitespace (0.00s)
    --- PASS: .../ORDER_BY_empty (0.00s)
    --- PASS: .../ORDER_BY_empty_extra_whitespace (0.00s)
    --- PASS: .../HAVING_empty (0.00s)
    --- PASS: .../WHERE_empty (0.00s)
    --- PASS: .../LIMIT_empty (0.00s)
    --- PASS: .../OFFSET_empty (0.00s)
    --- PASS: .../LIMIT_empty_with_OFFSET (0.00s)
    --- PASS: .../GROUP_BY_empty_with_ORDER_BY_operand (0.00s)
PASS
ok  	example.com/sqlite-postgres-compat/compat	2.358s
```

`grep -n "reinstall" AGENTS.md docs/USAGE.md`:
```
AGENTS.md:296:- **Reinstall capture after upgrading.** ...
docs/USAGE.md:161:### Compatibilidad al actualizar la herramienta (reinstall de la captura)
```

`.\scripts\check.ps1` (gate del repo):
```
gofmt: OK
go vet: OK
?   	example.com/sqlite-postgres-compat/cmd/compat-audit	[no test files]
?   	example.com/sqlite-postgres-compat/cmd/compat-copy	[no test files]
?   	example.com/sqlite-postgres-compat/cmd/compat-cutover	[no test files]
ok  	example.com/sqlite-postgres-compat/cmd/internal/cliout	2.525s
ok  	example.com/sqlite-postgres-compat/compat	2.362s
go test: OK
```