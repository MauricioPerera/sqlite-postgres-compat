# QUAL-C4 — Refactor conservador de anidamiento/longitud

Refactor de comportamiento-preservante sobre las 3 funciones del backlog #4
(ESTADO-CODIGO-REPORT §2.4). Cero cambios de comportamiento: mismos errores con
los mismos mensajes, mismo SQL generado, misma semántica.

## Funciones intervenidas

| Función | Archivo | Líneas antes → después | Anidamiento máx antes → después | Meta |
|---|---|---|---|---|
| `parseCatalogSelect` | `compat/sqlselect.go` | 149 → 38 | 5 → 2 | <80, máx 4 ✓ |
| `Schema.Validate` | `compat/schema.go` | 136 → 22 | 5 → 3 | <80, máx 4 ✓ |
| `compileExpressionIn` | `compat/ddl.go` | 115 → 50 | 4 → 3 | <80 ✓ |

Las 3 superan la meta. No hubo desviaciones.

> **Métrica de anidamiento (reproducible):** número de tabs en el sangrado de
> cada línea no vacía de la función, contado con un script Python que itera el
> rango de líneas `[inicio, fin]` de la función y reporta el máximo de tabs
> iniciales. Esto coincide con la convención del reporte original (tabs del
> cuerpo = profundidad de anidamiento; el cuerpo de la función cuenta como 0, y
> el "anidamiento 6" del reporte para `parseCatalogSelect` corresponde a 5 tabs
> en la línea más profunda). El script está abajo.

### Script de medición (reproducible)

```python
import re
def measure(path, start, end):
    maxtabs=0; lines=0
    with open(path,encoding='utf-8') as f:
        all=f.read().splitlines()
    for i in range(start-1, end):
        ln=all[i]
        tabs=0
        for c in ln:
            if c=='\t': tabs+=1
            else: break
        if tabs>maxtabs: maxtabs=tabs
        lines+=1
    return lines, maxtabs
def find_func(path, name):
    with open(path,encoding='utf-8') as f:
        all=f.read().splitlines()
    for i,ln in enumerate(all):
        if re.search(r'^func\b.*\b'+name+r'\b', ln):
            depth=0; started=False
            for j in range(i, len(all)):
                for c in all[j]:
                    if c=='{': depth+=1; started=True
                    elif c=='}': depth-=1
                if started and depth==0:
                    return i+1, j+1
    return None,None
```

Rangos usados (DESPUÉS): `parseCatalogSelect` L26-63, `Validate` L213-234,
`compileExpressionIn` L246-295. ANTES (baseline, funciones originales):
`parseCatalogSelect` L11-159, `Validate` L213-348, `compileExpressionIn`
L246-360.

## Qué se extrajo

### `parseCatalogSelect` (sqlselect.go)
Extracción por etapa del parser. La función principal quedó como orquestación
lineal; la lógica vivía en literales-función anidados dentro del slice `clauses`
(origen del anidamiento 5, p.ej. el `if number < 0` de LIMIT/OFFSET y los
`DESC`/`ASC` de ORDER BY).

Helpers nuevos:
- `stripCatalogSelectHeader` — trim, `CREATE VIEW ... AS`, prefijo
  `SELECT`/`DISTINCT`. Devuelve `(body, distinct, err)`.
- `catalogSelectClauses` — arma el slice de cláusulas con handlers delgados.
- `locateCatalogClauses` — localiza keywords y ordena por posición (bubble
  sort original preservado).
- `applyCatalogClauses` — aplica cada handler en orden de fuente.
- `parseCatalogProjections` — parsea la lista de proyecciones.
- `applyOrderByClause`, `applyLimitClause`, `applyOffsetClause` — los casos
  que anidaban, ahora planos a nivel paquete.
- Tipos `catalogClause` y `locatedClause` movidos a nivel paquete (antes
  `locatedClause` era local a la función).

**Trade-off:** más funciones y dos tipos nuevos a nivel paquete aumentan levemente
la superficie del archivo, pero cada helper tiene un solo propósito y es testeable
aisladamente. El orden de chequeos de error se preserva exacto (header → no FROM
→ parseo FROM → aplicación de cláusulas → proyecciones → "no projections"), de
modo que el primer error que disparaba el código original sigue disparándose
primero.

### `Schema.Validate` (schema.go)
Extracción por entidad. La función principal quedó como secuencia de validación
que hilvana los mapas `tables`/`tableColumns` que el validador de índices
necesita.

Helpers nuevos (todos a nivel paquete, sin receptor — no usan `s`):
- `validateTable` — valida una tabla y la registra en ambos mapas.
- `validateTableColumn` — valida una columna (aquí vivía el anidamiento 5: el
  chequeo de dimensión vector dentro del loop de columnas).
- `validateIndexes`, `validateViews`, `validateTriggers`, `validateRoutines`.

**Preservado explícitamente:** el chequeo `knownTypeFamily` (extensión AUDIT2
reciente) se copió textual en `validateTableColumn` y `validateRoutines`. El
orden de chequeos por tabla (nombre → reservado → duplicado → columnas →
constraints → registro de `tableColumns`) se mantiene idéntico, igual que el
orden entre entidades (tablas → índices → vistas → triggers → rutinas). El
mensaje de error `foreign key on table %q has an invalid referential action`
se preserva.

**Trade-off:** los helpers reciben los mapas por parámetro en vez de cerrar sobre
ellos; es un acoplamiento ligeramente más explícito pero más fácil de razonar y
testear. Las variables internas de mapa se renombraron (`indexes`→`indexNames`,
etc.) para no sombrear el parámetro; sin efecto observable.

### `compileExpressionIn` (ddl.go)
Extracción de casos del switch que reducía longitud real sin oscurecer. Se
extrajeron los cuatro casos con lógica no trivial; los triviales (`null`,
`string`, `integer`/`decimal`, `boolean`, `star`) y los cortos (`coalesce`,
`replace`) quedaron inline.

Helpers nuevos:
- `compileColumnExpression(value, inTrigger)` — el caso `column`. **No recibe
  `engine`** (el original no lo usaba) para no introducir un parámetro muerto.
- `compileBinaryExpression(engine, expression, inTrigger)` — operadores
  binarios (incluido el mapeo `LIKE`→`ILIKE` en Postgres).
- `compileUnaryExpression(engine, expression, inTrigger)` — `not`/`is_null`/
  `is_not_null`.
- `compileScalarFunction(engine, expression, inTrigger)` — funciones escalares
  de un argumento.

**Preservado exacto:** el wrapper `compileExpression` y la firma de
`compileExpressionIn` no cambiaron. La semántica NEW/OLD gated por
`inTrigger` se preservó byte a byte: sólo `i == 0` y `inTrigger` disparan el
uppercase sin quote; todo lo demás se quita. El helper `compileColumnExpression`
recibe el flag y lo propaga de la única forma posible (no hay recursión que
herede contexto). Se agregó un test directo
(`TestCompileColumnExpressionGatesTriggerNewOld`) que pina el caso `a.new`
(sólo el segmento inicial es mágico) y el caso `New` fuera de trigger (case
preservado, quoted, no plegado a `NEW`).

**Trade-off:** cuatro helpers nuevos para cuatro casos. `coalesce`/`replace`
quedaron inline porque moverlos a helpers ahorra ~4 líneas cada uno sin bajar
el anidamiento máx (ya 3, bajo la meta 4) y oscurecería dónde se ensambla
`COALESCE(...)`/`REPLACE(...)`. El caso `boolean` quedó inline por la misma
razón: 6 líneas, anidamiento 3, y extraerlo añadiría una firma sin ganancia
neta.

## Red de pruebas

- Tests existentes **no modificados**: `git diff --numstat` sobre
  `compat/*_test.go` muestra `30 0 compat/ddl_test.go` (solo adiciones — el test
  nuevo) y **cero** cambios en `schema_test.go` ni `sqlselect_test.go`.
- Test nuevo (adición pura): `TestCompileColumnExpressionGatesTriggerNewOld`
  en `ddl_test.go` — pin directo del helper más sutil (el gating `inTrigger` de
  `compileColumnExpression`, código sensible por AUDIT2).

## Definición de hecho (salida REAL pegada)

```
$ go build ./...        → BUILD_OK  (sin output de error)
$ go vet ./...          → VET_OK    (sin output de error)
$ go test ./compat/ -count=1
ok  	example.com/sqlite-postgres-compat/compat	2.403s
```

El verbose (`-v`) corre todas las suites (incluidas las de trigger NEW/OLD,
vector, LIKE/ILIKE, schemas de trigger, snapshot round-trip) — todas PASS,
incluido el test nuevo `TestCompileColumnExpressionGatesTriggerNewOld`.

## Verificación de cero cambios de comportamiento

1. `go build ./...` verde.
2. `go vet ./...` verde.
3. `go test ./compat/ -count=1` verde (suite completa, sin saltar nada).
4. `git diff --numstat -- 'compat/*_test.go'` → solo adiciones (30/0), ningún
   test existente reescrito.
5. Archivos tocados: únicamente `compat/sqlselect.go`, `compat/schema.go`,
   `compat/ddl.go` y `compat/ddl_test.go` (adición). No se tocaron `go.mod`,
   `go.sum`, `experiments/**`, `cmd/**`, `e2e/**`, `compat/inspect.go`.

## Archivos no tocados (respeto del cerco)
`go.mod`, `go.sum`, `experiments/`, `cmd/`, `e2e/`, `compat/inspect.go`,
`compat/schema_test.go`, `compat/sqlselect_test.go`.