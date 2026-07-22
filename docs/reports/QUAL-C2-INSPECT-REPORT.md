# QUAL-C2 — Refactor conservador de `(*Store).inspectPostgres`

**Fecha:** 2026-07-22
**Archivo tocado:** `compat/inspect.go` (único)
**Autor:** dev efímero (GLM vía CCDD)

## 1. Objetivo

`(*Store).inspectPostgres` (`compat/inspect.go:369`) era la función más larga del repo
(227 líneas, anidamiento 4) con 3 consultas SQL inline de 60+ líneas (columnas,
constraints, indexes) más las fases de views/triggers/routines. El objetivo era un
refactor **conservador de comportamiento**: extraer las fases a funciones separadas
de modo que `inspectPostgres` quede como orquestador corto (<80 líneas) y cada fase
sea legible y aislada, con **cero cambios de comportamiento**.

## 2. Cambios realizados

Se reemplazó el cuerpo monolítico de `inspectPostgres` por un orquestador de 33 líneas
que llama a 6 fases, 5 nuevas + 1 existente:

| Función | Estado | Responsabilidad |
|---|---|---|
| `inspectPostgres` (orquestador) | refactorizado → 33 líneas | secuencia las fases, arma `Schema.Tables` |
| `inspectPostgresColumns` | **nueva** | query `information_schema.columns` + armado del map `tables`/`order` + unresolved de columnas |
| `inspectPostgresConstraints` | **nueva** | query `pg_constraint` + attach de constraints a `tables` + unresolved |
| `inspectPostgresIndexes` | **preexistente** (`inspect.go:636`), integrada tal cual | ya era función; se invoca sin cambios |
| `inspectPostgresViews` | **nueva** | query `information_schema.views` |
| `inspectPostgresTriggers` | **nueva** | query `pg_trigger` |
| `inspectPostgresRoutines` | **nueva** | query `pg_proc` (con el comentario sobre prokind trasladado al doc-comment) |

### Firma de las nuevas funciones

```go
func (store *Store) inspectPostgresColumns(ctx context.Context, inspection *Inspection) (map[string]*Table, []string, error)
func (store *Store) inspectPostgresConstraints(ctx context.Context, tables map[string]*Table, inspection *Inspection) error
func (store *Store) inspectPostgresViews(ctx context.Context, inspection *Inspection) error
func (store *Store) inspectPostgresTriggers(ctx context.Context, inspection *Inspection) error
func (store *Store) inspectPostgresRoutines(ctx context.Context, inspection *Inspection) error
```

`inspection *Inspection` se pasa por puntero para que cada fase agregue a
`inspection.Unresolved` y a `inspection.Schema.*` en el mismo orden relativo que el
original (columnas → constraints → indexes → views → triggers → routines). El map
`tables` y el slice `order` los construye `inspectPostgresColumns` y los comparte con
`inspectPostgresConstraints` (puntero al map → las mutaciones de `table.Constraints`
se ven). El orquestador luego copia `*tables[name]` a `inspection.Schema.Tables`,
replicando el patrón original (populate tras columnas, re-populate por índice tras
constraints).

## 3. Conservación de comportamiento

- **Mismas queries SQL byte a byte.** Las 3 queries objetivo (columnas, constraints)
  más las de views/triggers/routines se copiaron textualmente dentro de sus nuevas
  funciones. Verificación: se extrajeron todas las raw-string literales (backtick)
  del archivo original y del refactorizado y se diffearon — **16 strings, diff
  vacío**. La query de indexes no se movió (ya vivía en su propia función).
- **Mismo orden de resultados.** El orquestador invoca las fases en el orden exacto
  del original y cada fase anexa a `inspection.Unresolved` y `inspection.Schema.*` en
  el mismo orden de filas. El doble-populate de `Schema.Tables` (tras columnas y tras
  constraints) se conservó idéntico.
- **Mismos errores y mensajes.** Toda cadena de error, `Reason` de `CatalogObject` y
  ruta de retorno se preservó. Las helpers puras (`sqliteReferentialAction`,
  `postgresReferentialAction`, `extractIndexPredicate` en `inspect.go`;
  `validReferentialAction` en `schema.go`) **no se tocaron** — mismas firmas.
- **Semántica de `Unresolved` idéntica** (mismos `Kind`, `Name`, `Definition`,
  `Reason`, mismo `Exact = len(Unresolved) == 0` al cierre).
- **Patrón de close de rows preservado por fase** (detalle de fidelidad):
  columns y views **no** cierran el rows en error de scan; constraints, triggers y
  routines **sí** cierran en error de scan y en los `json.Unmarshal` de constraints.
  Esto se respetó literalmente para no alterar el comportamiento de liberación de
  recursos en cada path de error.

## 4. Verificación (salida real de comandos)

```
$ go build ./...
BUILD_OK
$ go vet ./...
VET_OK
$ go test ./compat/ -count=1
ok  	example.com/sqlite-postgres-compat/compat	2.295s
$ gofmt -l compat/inspect.go     # (vacío = canónico)
$ # conteo reproducible de inspectPostgres (awk: desde la línea del func hasta su llave de cierre balanceada)
inspectPostgres lines: 33 (lines 369-401)
$ diff <(grep -o '`[^`]*`' /tmp/inspect.go.orig) <(grep -o '`[^`]*`' compat/inspect.go)
SQL_IDENTICAL_OK
```

- `gofmt -l` no lista el archivo → formato canónico, sin diff.
- Diferencia del archivo: `1 file changed, 122 insertions(+), 71 deletions(-)` (solo
  `compat/inspect.go`).
- Condiciones de “definición de hecho”: build ✅, vet ✅, `go test ./compat/` ✅,
  `inspectPostgres` = 33 líneas (<80) ✅, 3 queries idénticas byte a byte ✅.

> Nota: la suite corre mientras otros devs editan `compat/inspect_test.go`,
> `compat/schema_test.go` y `compat/verify_test.go` (modificados antes de mi inicio,
> ajenos a este refactor). Mi corrida final del paquete `compat` fue verde. No toqué
> ningún `*_test.go` ni `cmd/**`.

## 5. Trade-offs

- **Extraje 5 fases, no solo las 3 nombradas** (columnas, constraints, indexes). Las
  3 consultas inline del reporte (columnas, constraints; indexes ya era función) eran
  el foco, pero dejar views/triggers/routines inline dejaba al orquestador en ~98
  líneas, por encima del límite de 80. Para cumplir la definición de hecho (<80) y el
  espíritu de “cada fase legible y aislada” extraje también views/triggers/routines.
  Es extracción de funciones pura, sin abstracción especulativa ni frameworks.
- **Paso de `*Inspection` por puntero** entre fases en vez de devolver slices de
  unresolved y mergear en el orquestador. Alternativa más “funcional” (cada fase
  devuelve `[]CatalogObject` y el orquestador los concatena) duplicaría la lógica de
  append y rompería el orden si se equivoca el orden de concatenación. El puntero
  preserva el orden de append exacto del original con menos código. Trade-off:
  acoplamiento ligero (las fases mutan la `inspection` recibida), pero idéntico al
  comportamiento previo donde todo mutaba la misma variable local.
- **El map `tables` se comparte por puntero** entre `inspectPostgresColumns` (lo
  construye) y `inspectPostgresConstraints` (lo muta). Es el mecanismo más fiel al
  original (que usaba una sola variable local `tables`); evita copiar/reconstruir.
- **Comentarios trasladados, no eliminados.** El bloque de comentario sobre
  `atttypmod`/pgvector y el de `prokind`/agregados se movieron a los doc-comments de
  `inspectPostgresColumns` e `inspectPostgresRoutines` respectivamente. Comentarios
  no son comportamiento; se preservó la información.
- **No se eliminó el doble-populate redundante de `Schema.Tables`** (tras columnas y
  tras constraints). Produce el mismo resultado que un populate único, pero
  conservarlo evita cualquier riesgo de reordenamiento y mantiene el diff de
  comportamiento nulo. Es un featuritis menor deliberadamente no tocado.
- **Line endings:** el archivo original usa LF; el refactorizado también (verificado
  `grep -c $'\r'` = 0 en ambos). El warning de git “LF will be replaced by CRLF” es
  ruido de `core.autocrlf` en Windows, no un cambio real de endings.

## 6. Límites / no hechos

- No se modificó ninguna firma prohibida ni ningún archivo fuera de
  `compat/inspect.go`.
- No se ejecutó una verificación contra un Postgres real (no se pidió); la garantía
  de “mismas queries” es por inspección byte a byte del literal SQL (diff vacío),
  no por ejecución de la base.