# AUDIT9-CUBOA — Ronda de confirmación e interacción entre las 5 features del Cubo A

**Fecha:** 2026-07-23
**Rol:** auditor adversarial independiente, sin memoria previa. Árbol `main` con las 5 features del Cubo A ya implementadas (FEAT-CUBOA-1, 2A, 2B, 3, 4, 5), cada una auditada aislada por su dev. Foco de esta ronda: **lo que emerge al COMBINAR las features** + **confirmar que los límites honestos declarados siguen cerrados** + **no-regresión de la cadena de fidelidad**.
**Motor:** `go1.26.4 windows/amd64`, `modernc.org/sqlite`, PostgreSQL **17 real** en `postgres://postgres:***@31.220.22.176:5434/postgres?sslmode=disable` (password SIEMPRE `***`). Binarios/tests buildeados con `go build`/`go test` (nunca `go run` para exit codes de decisión).
**Metodología:** todo con evidencia EJECUTADA. Se creó un archivo de test temporal `e2e/zz_audit9_test.go` (tag `e2e`, 8 tests de nivel superior), se corrió contra PG 17 real, y se **borró al terminar**. `git vet` limpio. Bases temporales `compat_e2e_%` dropeadas; 0 remanentes verificado por consulta a `pg_database`.

**VEREDICTO: ÁREA LIMPIA.** 6/6 combinaciones de features producen equivalencia de datos (`equivalent=true`) o resultados byte/fila-idénticos SQLite vs PG; ningún cruce rompe round-trip ni fidelidad; 8/8 límites honestos confirmados CERRADOS (caen a error de compilación o a `Unresolved`, nunca a SQL divergente); cadena de fidelidad intacta. **0 hallazgos ALTA, 0 MEDIA, 0 BAJA.**

---

## 1. Matriz de interacciones entre features nuevas (evidencia ejecutada vs PG 17 real)

Cada test aplica el esquema canónico en SQLite real, exporta snapshot, lo importa en PostgreSQL 17 real (un `ImportSnapshot` verde prueba que PG **acepta el DDL compilado** de la combinación), y luego compara datos/resultados fila a fila entre motores o verifica `equivalent=true`.

| # | Interacción probada | Qué combina | Verificación | Resultado |
|---|---|---|---|---|
| I1 | Vista con `UNION` y vista con `EXCEPT` cuyas ramas usan `CASE` (proyección) + `BETWEEN` + `IN` (WHERE) | 2A × 1 | Import PG verde; filas `(id,band)` SQLite==PG en ambas vistas, con fila `NULL` (score) que dispara 3-valued logic | **PASS** |
| I2 | Vista con **tabla derivada** `FROM (SELECT … CASE … WHERE bucket IN(…) UNION SELECT …) AS s` y vista con **CTE** `WITH w AS (mismo cuerpo con set-op anidado) SELECT … FROM w` | 2B × 2A × 1 | Import PG verde; filas `(id,label)` SQLite==PG en ambas vistas | **PASS** |
| I3 | Columna generada STORED cuya expresión usa `CASE` (grade), `coalesce(nullif(...))` (norm) y `CASE … BETWEEN …` (inrange) | 3 × 1 | Snapshot copy SQLite→PG `equivalent=true`; spot-check: PG **recomputó** `grade='F', norm=-1, inrange=0` para `score=0` (INSERT nunca escribió las generadas) | **PASS** |
| I4 | Índice de expresión sobre `lower(email)` **y** sobre un `CASE` | 4 × 1 | Compile **byte-idéntico** SQLite==PG de ambos `CREATE INDEX`; Import PG verde; snapshot `equivalent=true` | **PASS** |
| I5 | Dominio cuyo `CHECK` usa `IN(1,2,3)` **AND** `BETWEEN 0 AND 10` **AND** `(CASE WHEN VALUE>0 THEN 1 ELSE 0 END)=1` | 5 × 1 | Copy `equivalent=true`; el dominio **nativo PG** rechaza `code=5` (pasa BETWEEN, falla IN) y el `CHECK` inline **SQLite** también → ambos motores aplican la misma restricción compuesta | **PASS** |
| I6 | Tabla combinando **columna generada STORED + dominio + índice de expresión a la vez**; snapshot **y replicación incremental** (INSERT+UPDATE capturados en SQLite → `ApplyChanges` estricto en PG) | 3 × 4 × 5 | Tras replicar UPDATE `qty=8,label='WORLD'`: PG recomputó `total=16` **sin conflicto espurio**; `assertStoreSnapshotsEquivalent` verde | **PASS** |

**Salida ejecutada (`go test ./e2e -tags e2e -run TestAudit9… -v`):**

```
--- PASS: TestAudit9UnionExceptBranchesUseCaseBetweenIn (2.42s)
--- PASS: TestAudit9DerivedAndCTEUseCaseInNestedSetOp (2.00s)
--- PASS: TestAudit9GeneratedColumnUsesCaseCoalesceNullifBetween (1.67s)
--- PASS: TestAudit9ExpressionIndexOverLowerAndCase (1.16s)
--- PASS: TestAudit9DomainCheckUsesInBetweenCase (1.40s)
--- PASS: TestAudit9CombinedGeneratedDomainExpressionIndexReplication (2.42s)
```

**Notas de la caza (nada roto):**
- El anidamiento **CASE dentro de rama de set-op dentro de tabla derivada / CTE** (I2) es el cruce más profundo probado (3 features apiladas). El `caseSpanMask` de FEAT-1, el splitter de compounds de FEAT-2A y el parser de subquery/CTE de FEAT-2B componen sin fugas: los `AND`/`WHEN`/`END` internos no se confunden con el `UNION` de nivel superior ni con el límite de la subconsulta. Resultado fila-idéntico.
- La **exclusión de columnas generadas del INSERT/UPDATE** (FEAT-3) sobrevive intacta cuando la misma tabla lleva además un dominio (columna extra) y un índice de expresión (I6): la numeración `$N` de placeholders no se descoloca y el `before`-image cross-engine con el valor generado por la fuente coincide con la recomputación de PG → sin `ConflictError`.
- El **dominio con CHECK compuesto** (I5) confirma que `domain_value`→`VALUE` (PG) y `domain_value`→columna (SQLite inline) se sustituye correctamente aun cuando el CHECK anida `CASE`/`IN`/`BETWEEN`.

---

## 2. Confirmación de límites honestos declarados (8/8 CERRADOS)

Regla que se verifica: cada límite debe caer a **error de compilación** o a **`Unresolved` (`Exact=false`)**, **nunca** a SQL divergente emitido en silencio.

### 2.1 Nivel compilación/validación (`CompileDDL` debe fallar en AMBOS motores)

| Límite | Construcción probada | Resultado |
|---|---|---|
| Cadena de set-ops que **mezcla INTERSECT** con UNION/EXCEPT | `Compounds=[intersect, union]` sobre la misma vista | `CompileDDL` **error** en SQLite y PG → **CERRADO** (no emite cadena plana divergente) |
| **VIRTUAL** generated column | `Generated{Stored:false}` | `CompileDDL` **error** ambos → **CERRADO** |
| **Generada + DEFAULT** a la vez | `Generated` + `Default` en la misma columna | `CompileDDL` **error** ambos → **CERRADO** |
| **Generada en PRIMARY KEY** | PK `(id, g)` con `g` generada | `CompileDDL` **error** ambos → **CERRADO** |

```
--- PASS: TestAudit9CompileLevelLimitsStayClosed
    --- PASS: /mixed_intersect_union
    --- PASS: /virtual_generated
    --- PASS: /generated_with_default
    --- PASS: /generated_in_pk
```

### 2.2 Nivel inspección externa PG (deparse real → debe caer a `Unresolved`)

DDL nativo crudo aplicado en un `public` recreado (sin metadata canónica) en PG 17 real; `InspectSchema` inspecciona el catálogo nativo.

| Límite | DDL nativo PG | `Unresolved` observado |
|---|---|---|
| `WITH RECURSIVE` | `CREATE VIEW a9l_rec AS WITH RECURSIVE r(id) AS (…) SELECT id FROM r` | `view:a9l_rec` |
| Subconsulta correlacionada / `EXISTS` | `CREATE VIEW a9l_corr AS SELECT id FROM b WHERE EXISTS (SELECT 1 FROM o WHERE o.ref=b.id)` | `view:a9l_corr` |
| Índice de expresión que PG **constant-foldea** | `CREATE INDEX a9l_coalesce ON a9l_base ((coalesce(email,'x')))` → PG reescribe a `'x'::text` | `index:a9l_coalesce` |
| Dominio en inspección externa | `CREATE DOMAIN a9l_pos …; CREATE TABLE a9l_domtbl(qty a9l_pos)` | `domain_column:a9l_domtbl.qty` |

Salida real (todos presentes, `Exact=false`):

```
--- PASS: TestAudit9ExternalInspectionLimitsStayClosed (2.04s)
    unresolved objects: [domain_column:a9l_domtbl.qty index:a9l_coalesce view:a9l_rec view:a9l_corr]
```

Los 4 objetos caen a `Unresolved` — ninguno se reconstruye a un AST equivocado ni se degrada a texto silencioso. **CERRADOS.**

**Conteo de límites: 8/8 confirmados CERRADOS** (4 en compilación + 4 en inspección externa). Coincide con lo declarado en los reportes FEAT-CUBOA-2A/2B/3/4/5.

---

## 3. No-regresión de la cadena de fidelidad — VEREDICTO: INTACTA

- **Marcador `\x01real`:** el código `realDecimalMarker = "\x01real"` (`compat/capture.go:29`), su emisión por el trigger de captura (`capture.go:189`, `CASE typeof(col) WHEN 'real' THEN <marker>||printf('%.17g',…)`) y su corte en lectura (`cutRealDecimalMarker`, `store.go:455`, usado en `store.go:330`) **no fueron tocados** por ninguna feature del Cubo A (confirmado por inspección directa).
- **Reconciliación decimal/date y supresión de ecos:** suite de fidelidad completa verde, sin cambios de comportamiento:

```
go test ./compat/ -run 'Store|Capture|Replicat|Apply|Marker|Real|Decimal|Echo|Suppress|Conflict|Canonical' -count=1
ok  example.com/sqlite-postgres-compat/compat  2.419s
```

  Incluye (verificado en verbose en corrida previa): `TestSQLiteDecimalUsesLosslessTextStorage`, `TestApplyChangesDecimalRealStorageNoSpuriousConflict`, `TestApplyChangesPreservesArbitraryPrecisionDecimal(EndToEnd)`, `TestCanonicalDecimalReconcilesFloatStorage`, `TestCanonicalFloatRejectsInfNaN`, `TestApplyChangesInsertDuplicateRaisesConflictError`, `TestApplyChangesTolerantStillDetectsGenuineConflict`, `TestSQLiteSetCaptureSuppressedUpdatesStateTable` (supresión de ecos).
- **Replicación con features nuevas encima de la cadena de datos:** I6 (arriba) ejercita `InstallChangeCapture` + `ReadCapturedChanges` + `ApplyChanges` estricto sobre una tabla con generada+dominio+índice y **no** produce conflicto espurio ni divergencia. La única interacción de las features con la ruta de datos (el `continue` sobre columnas generadas en `insertRow`/`updateRow`) es puramente aditiva y no altera el marcador ni la reconciliación.

Cadena de fidelidad **intacta**.

---

## 4. Suite completa y limpieza

- **Suite E2E completa contra PG 17 real** (`go test ./e2e -tags e2e -count=1`, con los 8 tests AUDIT9 añadidos temporalmente): **54 tests de nivel superior superados + 1 fallido de forma intencional** (`TestSystemClaimsExactCoverageForRequiredFeatureFamilies`, el gate del 100 % rojo por diseño; documentado en AUDIT7-C.md H2). **Ninguna otra falla.** Los 8 tests AUDIT9 PASS.
- `go vet -tags=e2e ./e2e` → limpio. `go build ./...` → verde.
- **Bases temporales:** al inspeccionar quedaba 1 base huérfana `compat_e2e_1784800551247827300` (predata esta sesión — todas mis corridas terminaron y auto-limpiaron vía `TestMain`). **Dropeada**; reconfirmado `REMAINING=0` (`pg_database`). No se crearon bases con prefijo `audit9_`.
- **Archivos:** `e2e/zz_audit9_test.go` y el probe `chkdb_a9_tmp.go` **borrados**. `git status` final: sólo este reporte como novedad; ningún otro cambio introducido por la auditoría (las modificaciones pre-existentes del Cubo A quedaron intactas). Sin `git add/commit/stash`. Ningún proceso en foreground que no termine solo. Nada escrito fuera del repo (salvo temporales borrados). Password del DSN nunca en literal.

---

## 5. Hallazgos por severidad

| Severidad | Cantidad | Detalle |
|---|---|---|
| **ALTA** (corrupción / divergencia silenciosa de datos, o SQL divergente emitido) | **0** | Ninguna combinación produjo divergencia; ningún límite emitió SQL divergente. |
| **MEDIA** (rechazo incorrecto, inconsistencia, hueco) | **0** | Todos los rechazos observados son correctos y honestos (caen a error o `Unresolved`). |
| **BAJA** (cosmético) | **0** | — |

**Conteo final: 0 ALTA / 0 MEDIA / 0 BAJA. Área limpia.**

Combinaciones probadas: (2A UNION/EXCEPT × CASE/BETWEEN/IN), (2B derived+CTE × CASE/IN × set-op anidado), (3 generada × CASE/coalesce/nullif/BETWEEN), (4 índice-expr × lower + CASE), (5 dominio × IN/BETWEEN/CASE), (3+4+5 combinadas × snapshot + replicación incremental). Límites re-ejecutados: mezcla INTERSECT, VIRTUAL, generada+default, generada-en-PK, WITH RECURSIVE, correlacionada/EXISTS, índice coalesce constant-foldeado, dominio en inspección externa — 8/8 cerrados.
