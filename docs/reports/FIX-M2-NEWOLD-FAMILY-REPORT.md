# FIX-M2 (new/old + family) — Cierre de AUDIT2-A MEDIA #2 y AUDIT2-C MEDIA #4

> **Nota de path**: la entrega pedía `docs/reports/FIX-M2-REPORT.md`, pero ese
> archivo ya existe **committed y limpio** (commit `d1db418`) con el report de
> OTRO dev sobre fixes distintos (LIMIT/OFFSET en `sqlselect.go` + esquema
> non-public en `sqltrigger.go`). El esquema `FIX-M{N}` se reutiliza entre
> rondas y colisiona. Sobreescribirlo destruiría trabajo ajeno committed, así
> que este report se entrega en `FIX-M2-NEWOLD-FAMILY-REPORT.md`. Los CODE fixes
> sí están aplicados en los archivos correspondientes.

Dev efímero. Repo: `sqlite-postgres-compat`, rama `main`. Otros 2 devs trabajan
en paralelo sobre `compat/sqltrigger.go`, `compat/capture.go`, `cmd/`,
`AGENTS.md`, `docs/USAGE.md`, `e2e/`; esos archivos **no** se tocaron.

Archivos modificados (solo los permitidos por la consigna):
- `compat/ddl.go`
- `compat/ddl_test.go`
- `compat/schema.go`
- `compat/schema_test.go` (nuevo)

## Fix 1 — AUDIT2-A MEDIA #2: `new`/`old` como variable mágica de trigger fuera de contexto

**Síntoma** (`compat/ddl.go`, rama `"column"` de `compileExpression`): cualquier
expresión cuyo primer segmento fuera `new`/`old` (case-insensitive) se compilaba
como `NEW`/`OLD` en mayúsculas **sin comillas**, incluso en CHECK, índices
`WHERE`, `DEFAULT` de columna y vistas. Una tabla con columna citada `"New"`
usada en un CHECK producía `CHECK (NEW > 0)`, que Postgres pliega a `new` →
columna inexistente → DDL falla en destino.

**Fix**: la interpretación NEW/OLD ahora aplica **solo en contexto de trigger**.
Fuera de ese contexto, `new`/`old` son columnas comunes y pasan por
`quoteIdentifier` como cualquier otra.

**Cómo (decisión de diseño, minimizando el diff)**: se eligió un **wrapper** en
lugar de agregar un parámetro `inTrigger` a `compileExpression` en todos los
sitios. Así los 8 sitios no-trigger (índices, `DEFAULT`, CHECK, `SELECT`/vistas)
quedan intactos:

- `compileExpression(engine, expr)` ahora es un thin wrapper que delega en
  `compileExpressionIn(engine, expr, false)` (no-trigger por defecto).
- `compileExpressionIn(engine, expr, inTrigger bool)` contiene la lógica
  original; la rama `"column"` solo convierte a mayúsculas el segmento `new`/`old`
  cuando `inTrigger && i == 0`; si no, lo cita con `quoteIdentifier`.
- La recursión propaga `inTrigger` (binarios, unarios, funciones escalares) y
  `compileExpressionArgs` recibe `inTrigger` y lo propaga a cada argumento,
  para que un `coalesce(new.x, 0)` anidado en un body de trigger siga
  resolviendo `NEW."x"`.
- Los 5 sitios de trigger (`compileTrigger` WHEN ×2, `compileTriggerAction`
  valor de asignación + WHERE de update + WHERE de delete) llaman a
  `compileExpressionIn(engine, expr, true)`.

**Trade-off**: el wrapper deja el flag implícito en la firma pública
`compileExpression` (siempre `false` = "fuera de trigger"). Es deliberado y lo
seguro: el caso común (CHECK/índice/vista/DEFAULT) no puede activar la magia
NEW/OLD por accidente. Un nuevo sitio que necesite contexto de trigger debe usar
`compileExpressionIn(..., true)` explícitamente. Alternativa considerada y
descartada: firmar `compileExpression(engine, expr, inTrigger)` y cambiar los 13
sitios — diff mayor y sin beneficio, porque los sitios no-trigger quieren
siempre `false`.

**Sin regresión de triggers**: `new.x`/`old.x` en bodies de trigger compila
idéntico al antes (`NEW."x"`/`OLD."x"`, prefijo sin comillas). Verificado por
tests existentes (`TestCompileCanonicalTriggerForBothEngines`,
`TestCompileCanonicalTriggerUpdateAndDeleteActions`) y nuevos.

## Fix 2 — AUDIT2-C MEDIA #4: familias de tipo desconocidas no rechazadas por `Schema.Validate`

**Síntoma** (`compat/schema.go`, `Schema.Validate`): solo se chequeaba
`column.Type.Family == ""` y la dimensión de `vector`. Un typo (`family: "nope"`)
pasaba `Validate` y afloraba recién en `compileType` (`ddl.go:182`,
`"type family %q is not supported by %s"`) después de conectar a ambas DBs,
mal clasificado como `ERR_SNAPSHOT`.

**Fix**: `Schema.Validate` rechaza ahora familias fuera del set conocido, con
mensaje temprano que incluye tabla/columna (o rutina/parámetro) y la familia
inválida. Aplicado también a parámetros de rutinas (donde `Validate` ya los
recorre, `schema.go:304`).

**Una sola fuente de verdad**: se introdujo `knownTypeFamilies` (mapa de las 11
familias) y `knownTypeFamily(family)` en `schema.go`, junto a las constantes
`TypeFamily`. `Schema.Validate` usa esa función; no se duplicó la lista. Los
switches de `compileType` (ddl.go) no se refactorizaron: enumeran familias para
mapearlas a SQL concreto por motor — preocupación distinta a "¿es una familia
conocida?"; tocarlos agregaría diff y riesgo sin valor. La fuente única del
**set de familias conocidas** es `knownTypeFamilies`.

## Tests nuevos

En `compat/ddl_test.go`:
- `TestCompileTriggerNewOldStillMagic` — `new.x`/`old.x` (y `NEW.x`
  case-insensitive, y anidado en `coalesce`) en bodies de trigger sigue
  compilando a `NEW."x"`/`OLD."x"`; ningún prefijo queda citado. SQLite y
  Postgres. (Caso (b) de la definición de hecho.)
- `TestCompileNonTriggerNewOldColumnIsQuoted` — CHECK con columna `new` (sin
  citar) y `"New"` (citada, case-sensitive), índice parcial `WHERE` con columna
  `old`, y vista con `new` en proyección + `WHERE`. Todos compilan a
  identificador **citado** (`"new"`, `"New"`, `"old"`); ningún `NEW`/`OLD`/`new`
  sin comillas. SQLite y Postgres. (Caso (a).)

En `compat/schema_test.go` (nuevo):
- `TestSchemaValidateRejectsUnknownTypeFamily` — `family: "nope"` rechazado; el
  error menciona tabla, columna y la familia.
- `TestSchemaValidateAcceptsEveryKnownTypeFamily` — las 11 familias aceptadas;
  `vector` aceptado con dimensión y rechazado por la dimensión (no por "familia
  no soportada") cuando falta el argumento.
- `TestSchemaValidateRejectsUnknownTypeFamilyOnRoutineParameter` — parámetro de
  rutina con `family: "nope"` rechazado; el error menciona rutina, parámetro y
  familia. (Caso (c).)

## Verificación real (salida pegada de los tres comandos)

```
$ go build ./...
BUILD_EXIT=0
```
(sin salida)

```
$ go vet ./...
VET_EXIT=0
```
(sin salida)

```
$ go test ./compat/ -count=1
ok  	example.com/sqlite-postgres-compat/compat	2.286s
TEST_EXIT=0
```

Suite `compat` verde mientras otros devs editan archivos ajenos en paralelo. No
se observaron fallos en tests de mis archivos. Los archivos modificados por los
devs paralelos (`sqltrigger.go`, `capture.go`, `cmd/`, `e2e/`, `AGENTS.md`,
`docs/USAGE.md`) no fueron tocados por este fix; cualquier fallo en sus suites
es ajeno y no se abordó.