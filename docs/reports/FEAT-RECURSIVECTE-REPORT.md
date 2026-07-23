# FEAT-RECURSIVECTE — Investigación de soporte para `WITH RECURSIVE`

**Resultado: BLOQUEADO.** Ningún subconjunto de `WITH RECURSIVE` puede garantizarse
byte-idéntico entre SQLite y PostgreSQL, ni siquiera con guardas sintácticas
obligatorias. La terminación es dependiente de los datos y su divergencia sobre
datos cíclicos es real, severa y no acotable en tiempo de compilación; no existe
una guarda de ciclo nativa portable. El rechazo actual en
`compat/sqlselect.go::stripCatalogWith` se mantiene, ahora respaldado por
evidencia ejecutada en lugar de una suposición.

Toda la evidencia de abajo proviene de SQL real ejecutado contra **SQLite real**
(driver `modernc.org/sqlite`) y **PostgreSQL 17 real** (DSN de la tarea; password
enmascarado `***`). El arnés de experimento vivió en `e2e/rectcte_experiment_test.go`
bajo el tag `e2e`, con timeouts duros por contexto y `statement_timeout` del lado
servidor para que ninguna recursión desbocada sobreviviera al experimento; se
eliminó al terminar (era investigación, no un test conservado). Cada experimento
usó una base PostgreSQL efímera con prefijo `rectcte_exp_<ns>`, dropeada en el
cleanup aun si el experimento fallaba.

---

## Riesgo 1 — Datos cíclicos con acumulador de profundidad

### Hipótesis

Casi toda query de árbol real (category tree, comment threads, org charts)
acumula una columna que **siempre cambia** (profundidad creciente o path
acumulado) para ordenar/presentar. La protección anti-ciclo de `UNION` deduplica
**filas completas**; si cada fila nueva es única por llevar una profundidad
distinta, `UNION` no deduplica nada y la recursión no termina. `UNION ALL` no
deduplica en absoluto.

### SQL ejecutado (idéntico en ambos motores)

Datos: un ciclo `1 → 2 → 1` (`nodes(id, parent_id)` con `(1,2),(2,1)`).

```sql
WITH RECURSIVE t(id, depth) AS (
  SELECT id, 0 FROM nodes WHERE id = 1
  UNION            -- (y una segunda corrida con UNION ALL)
  SELECT n.id, t.depth + 1 FROM nodes n JOIN t ON n.parent_id = t.id
)
SELECT id, depth FROM t
```

### Salida real

```
=========== EXPERIMENT 1: CYCLIC DATA (depth accumulator) ===========
[cyclic/UNION]     SQLite:   elapsed=6.007s rows=3178564 err=context deadline exceeded
[cyclic/UNION]     Postgres: elapsed=6.192s rows=222940  err=ERROR: canceling statement due to statement timeout (SQLSTATE 57014)
[cyclic/UNION ALL] SQLite:   elapsed=6s     rows=8096865 err=context deadline exceeded
[cyclic/UNION ALL] Postgres: elapsed=6.256s rows=218943  err=ERROR: canceling statement due to statement timeout (SQLSTATE 57014)
```

### Lectura

- **La hipótesis se confirma**: `UNION` **no** frena el ciclo cuando se acumula
  una profundidad monótona. SQLite generó 3.178.564 filas en 6 s y seguía
  subiendo; sólo lo detuvo la cancelación del cliente (`context deadline
  exceeded`). `UNION ALL` es aún peor (8.096.865 filas).
- **Los modos de fallo DIVERGEN**, que es lo decisivo:
  - **SQLite no tiene guardia de iteración del lado servidor** para CTE
    recursivas. No hay un `SQLITE_LIMIT_*` que aborte la recursión con error:
    produce filas indefinidamente hasta que el cliente cancela. En un despliegue
    sin timeout de cliente, se cuelga/consume memoria sin límite.
  - **PostgreSQL tampoco tiene límite nativo de iteraciones** para `WITH
    RECURSIVE`. Sólo se detuvo porque el experimento fijó
    `statement_timeout = '6000'`. Sin ese timeout, corre hasta agotar RAM/disco.
  - Los **conteos de filas antes del corte externo difieren en un orden de
    magnitud** (SQLite 3,1 M vs PG 223 K con `UNION`): no hay un resultado común,
    determinista y acotado para datos cíclicos. No existe "el mismo resultado" que
    comparar — existe "dos desbordamientos distintos, ambos detenidos por
    mecanismos externos diferentes".

No hay forma de acotar esto en tiempo de compilación: la aciclicidad **no es
estáticamente verificable**. Una `FOREIGN KEY (parent_id) REFERENCES nodes(id)`
**no** impide ciclos (`A→B→A` satisface la FK), así que incluso el category tree
"bien diseñado" del PM puede contener un ciclo.

---

## Riesgo 1b — ¿Existe una guarda de ciclo NATIVA portable?

Si hubiera una guarda de ciclo que ambos motores acepten, se podría exigir
sintácticamente. Se probó las dos únicas candidatas.

### SQL ejecutado

Cláusula `CYCLE` del estándar SQL (PostgreSQL 14+):

```sql
WITH RECURSIVE t(id, depth) AS ( ... UNION ALL ... ) CYCLE id SET is_cycle USING path
SELECT id, depth FROM t
```

Guarda manual con arrays (idiomático en PostgreSQL):

```sql
WITH RECURSIVE t(id, depth, path) AS (
  SELECT id, 0, ARRAY[id] FROM nodes WHERE id = 1
  UNION ALL
  SELECT n.id, t.depth + 1, t.path || n.id FROM nodes n JOIN t ON n.parent_id = t.id
  WHERE NOT (n.id = ANY(t.path))
)
SELECT id, depth FROM t
```

### Salida real

```
[CYCLE clause] Postgres err=<nil>
[CYCLE clause] SQLite   err=SQL logic error: near "CYCLE": syntax error (1)
[array guard]  Postgres err=<nil>
[array guard]  SQLite   err=SQL logic error: no such function: ANY (1)
```

### Lectura

- La **cláusula `CYCLE`** termina en PostgreSQL pero es un **error de sintaxis en
  SQLite** — SQLite no la implementa. No es portable.
- La **guarda por arrays** termina en PostgreSQL pero SQLite **no tiene tipo
  array ni `ANY`**. No es portable.
- La gramática de expresiones canónica (`compat/sqlparse.go`) **no tiene** ningún
  primitivo de pertenencia-sobre-path-acumulado: no hay arrays, ni `instr`, ni
  `position` en la allowlist de funciones. No se puede expresar una guarda de
  path portable dentro de la gramática.
- Aun si se craftease una guarda de texto ad-hoc, el parser podría verificar que
  una guarda **está presente**, pero **no que sea correcta** (que rompa *todos*
  los ciclos para *todos* los datos). Una guarda sintácticamente presente pero
  semánticamente errónea volvería a desbordar de forma divergente. "Sintaxis de
  guarda obligatoria" daría **falsa seguridad**, que es exactamente lo que la
  disciplina byte-idéntica del proyecto prohíbe.

---

## Riesgo 2 — Orden sin `ORDER BY` explícito

### SQL ejecutado

Árbol **acíclico** real de 3 niveles: `1 → {2,3}`, `2 → {4,5}`, `3 → {6}`,
insertado en orden deliberadamente desordenado (`(3,1),(1,NULL),(6,3),(2,1),(5,2),(4,2)`)
para exponer dependencia del orden de scan.

```sql
WITH RECURSIVE t(id, parent_id, depth) AS (
  SELECT id, parent_id, 0 FROM nodes WHERE parent_id IS NULL
  UNION ALL
  SELECT n.id, n.parent_id, t.depth + 1 FROM nodes n JOIN t ON n.parent_id = t.id
)
SELECT id, depth FROM t            -- (y una segunda corrida con ORDER BY depth, id)
```

### Salida real

```
=========== EXPERIMENT 2: ORDERING (acyclic 3-level tree) ===========
[order/NO ORDER BY]        SQLite:   [1:0 2:1 3:1 4:2 5:2 6:2]
[order/NO ORDER BY]        Postgres: [1:0 3:1 2:1 6:2 5:2 4:2]
[order/NO ORDER BY]        identical? false
[order/ORDER BY depth,id]  SQLite:   [1:0 2:1 3:1 4:2 5:2 6:2]
[order/ORDER BY depth,id]  Postgres: [1:0 2:1 3:1 4:2 5:2 6:2]
[order/ORDER BY depth,id]  identical? true
```

### Lectura

- **Sin `ORDER BY` el orden DIVERGE** sobre el mismo árbol acíclico: SQLite emite
  `1,2,3,4,5,6`; PostgreSQL emite `1,3,2,6,5,4`. Ambos recorren por niveles
  (breadth-first) pero el orden **intra-nivel** difiere porque depende del orden
  de scan de la working table de cada motor.
- **Con `ORDER BY depth, id` explícito ambos coinciden** fila por fila.
- Conclusión de este riesgo aislado: el orden **es** arreglable exigiendo
  sintácticamente un `ORDER BY` de nivel superior. Pero el orden **no es el
  bloqueante** — el bloqueante es la terminación (Riesgo 1).

---

## Diseño considerado y descartado

Se evaluó un subconjunto "seguro" = `WITH RECURSIVE` con (a) exactamente una
auto-referencia en el término recursivo, (b) `UNION` (no `UNION ALL`), (c)
`ORDER BY` de nivel superior obligatorio, y (d) una guarda de ciclo obligatoria.

- (c) resuelve el Riesgo 2 (probado).
- (b) es inútil contra el Riesgo 1 cuando se acumula profundidad/path (probado:
  `UNION` generó 3,1 M filas sin deduplicar).
- (d) es **inimplementable de forma garantizada**: no hay guarda nativa portable
  (probado: `CYCLE` y `= ANY(path)` fallan en SQLite), la gramática canónica no
  puede expresar una guarda de path, y la corrección de una guarda ad-hoc no es
  verificable sintácticamente.

Por tanto el subconjunto "seguro" no existe: cualquier subconjunto que se acepte
puede recibir datos cíclicos (no verificables en compilación) y producir dos
desbordamientos divergentes en lugar de un resultado común. Aceptarlo violaría la
garantía central del proyecto (resultado byte-idéntico en ambos motores).

## Decisión

**No entra nada.** Se conserva el rechazo existente de `WITH RECURSIVE` en
`compat/sqlselect.go::stripCatalogWith` y el comentario canónico en
`compat/schema.go`. No hay cambios en el modelo (`CommonTableExpr` no gana campo
`Recursive`), ni en el compilador (`compat/ddl.go::compileWith` intacto), ni
tests e2e nuevos (el conteo top-level de e2e permanece en 48). Las docs
(`AGENTS.md`, `docs/COMPATIBILITY.md`) se actualizan para citar esta evidencia en
lugar de afirmar la divergencia sin prueba.

## Trade-off honesto

El category tree acíclico del PM, con `ORDER BY` explícito, **sí** produce
resultados idénticos en ambos motores (Experimento 2). Lo que **no** se puede
garantizar es que los datos sean acíclicos, y sobre datos cíclicos no hay
resultado equivalente ni guarda portable. El proyecto prefiere rechazar un patrón
útil-pero-no-garantizable antes que emitir SQL cuya equivalencia dependa de una
propiedad de los datos que no puede probar. Esta es la misma disciplina con la que
se descartaron `round`/`substr`/`cast(... AS integer)` (ver
`FEAT-CUBOA-1-REPORT.md`): equivalencia garantizada o rechazo explícito, nunca
degradación silenciosa.
