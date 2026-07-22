# ESTADO DEL CÓDIGO — Reporte de calidad y salud

> Análisis READ-ONLY del repo `sqlite-postgres-compat` (HEAD `b831179`, árbol limpio).
> Fecha: 2026-07-22. Go `go1.26.4 windows/amd64`.
> No repite el análisis de estado del proyecto (ver `ESTADO-PROYECTO-REPORT.md`); este reporte evalúa la **calidad del código**.
> Salida de `go test`/`go vet`/`gofmt`/`go list -m -u` pegada textualmente.

---

## 1. ARQUITECTURA — mapa de paquetes

### Paquetes y responsabilidades

| Paquete | Rol | LOC prod / test |
|---|---|---|
| `compat/` (15 archivos prod, 14 de test) | **Núcleo.** Inspección de catálogo (SQLite+Postgres), DDL canónico, captura por triggers, replicación, auditoría de contratos, verificación, rutinas/triggers/selects SQL. | 4736 / 2983 |
| `cmd/internal/cliout` | Codificación/decodificación estricta de JSON de E/S y códigos de error estandarizados para los binarios. Capa de presentación compartida. | 206 / 0 |
| `cmd/compat-audit` | Binario: audita un contrato JSON contra un catálogo. | 41 / 0 |
| `cmd/compat-copy` | Binario: aplica un plan de migración JSON (copia de snapshot). | 98 / 0 |
| `cmd/compat-cutover` | Binario: orquesta cutover (dry-run + instalación de captura + import + drain + verificación). El `main()` más complejo (120 líneas). | 338 / 0 |
| `e2e/` (build tag `e2e`) | Tests end-to-end contra PostgreSQL real. **Excluidos de `go test ./...`** por el tag (PG desmontado). | 0 / 2170 |
| `experiments/vector/` | **Módulo Go independiente** (`example.com/vector-exp`, su propio `go.mod`) con `replace example.com/sqlite-postgres-compat => ../..`. Sonda de tipos vector/pgvector. | 23 / 1080 |

### Grafo de dependencias (interno)

```
cmd/compat-audit   ─┐
cmd/compat-copy    ─┼──> cmd/internal/cliout ──> compat
cmd/compat-cutover ─┘                           (núcleo hoja)
e2e/*             ──────> compat
experiments/vector─┴───> compat   (vía replace, módulo aparte)
```

- `compat` **no importa** a `cmd/`, `e2e` ni `experiments` (verificado con `grep` — `exit=1`, sin hits). **No hay capas violadas ni ciclos.** Es una hoja pura.
- Dirección de dependencia correcta: los binarios dependen del núcleo + la capa `cliout`; el núcleo no conoce a sus consumidores.
- `cliout` depende de `compat` (usa sus tipos de error), lo cual es razonable para una capa de presentación de los mismos binarios.

### Acoplamientos raros detectados

- **`experiments/vector` es un módulo Go separado** con `replace` al padre. No forma parte de la build normal de `go test ./...` (el root reporta `does not contain package example.com/sqlite-postgres-compat/experiments/vector`). Además arrastra **versión de `pgx` distinta** (`v5.10.0`) a la del root (`v5.7.6`) y una dependencia extra (`libsql-client-go`). Es código de investigación sin integración en CI → silencioso con respecto al resto. (smell, ver §3)
- Los tres binarios `cmd/*` repiten el patrón de parseo de flags + `os.Exit(cliout.EmitError(...))` de forma casi idéntica (ver §3, duplicación).

---

## 2. MÉTRICAS REALES

### 2.1 Tamaño por paquete

| Paquete | LOC prod | LOC test | ratio test/prod |
|---|---|---|---|
| `compat` | 4736 | 2983 | 0.63 |
| `cmd/internal/cliout` | 206 | 0 | 0 |
| `cmd/compat-cutover` | 338 | 0 | 0 |
| `cmd/compat-copy` | 98 | 0 | 0 |
| `cmd/compat-audit` | 41 | 0 | 0 |
| `e2e` (tag `e2e`) | 0 | 2170 | n/a |
| `experiments/vector` (módulo aparte) | 23 | 1080 | n/a |

La masa de producción vive en `compat` (~4.7k líneas). Los binarios son pegamento fino sin tests unitarios (su lógica cubierta solo vía e2e). El ratio test/prod del núcleo es 0.63 — razonable pero concentrado en parser/DDL; las funciones de catálogo Postgres quedan sin test (ver §2.4).

### 2.2 `go test ./... -count=1 -cover` (salida real pegada)

```
	example.com/sqlite-postgres-compat/cmd/compat-audit		coverage: 0.0% of statements
	example.com/sqlite-postgres-compat/cmd/compat-copy		coverage: 0.0% of statements
	example.com/sqlite-postgres-compat/cmd/compat-cutover		coverage: 0.0% of statements
	example.com/sqlite-postgres-compat/cmd/internal/cliout		coverage: 0.0% of statements
ok  	example.com/sqlite-postgres-compat/compat	2.355s	coverage: 64.8% of statements
```

- `e2e/` y `experiments/vector/` **no aparecen**: `e2e` por el build tag `e2e`; `experiments/vector` por ser módulo independiente.
- Cobertura efectiva: **64.8 %** del núcleo `compat`. Los `cmd/*` marcan 0.0 % (no hay tests unitarios; su comportamiento se ejercita solo por la suite e2e desmontada).

### 2.3 `go vet ./...` (salida real pegada)

```
EXIT=0
```
Limpio. Sin warnings del analizador estático en el módulo raíz. (`go vet ./...` en `experiments/vector` también `EXIT=0`.)

### 2.4 Funciones más largas / complejas

Método reproducible usado: contador en `awk` que arranca en cada línea que empieza con `func ` y cuenta llaves `{`/`}` para detectar el cierre real de la función (no la línea de EOF). Script: `/tmp/funcLen.awk` (incluido abajo). Profundidad de anidamiento medida con conteo de llaves por línea dentro de cada función.

**Top 12 (solo producción, `compat/` + `cmd/`), por líneas:**

| # | Líneas | Prof. máx | Archivo:línea | Función |
|---|---|---|---|---|
| 1 | **227** | 4 | `compat/inspect.go:369` | `(*Store).inspectPostgres` |
| 2 | 149 | **6** | `compat/sqlselect.go:11` | `parseCatalogSelect` |
| 3 | 130 | 5 | `compat/sqlparse.go:13` | `parseCatalogExpression` |
| 4 | 130 | 5 | `compat/schema.go:185` | `Schema.Validate` |
| 5 | 120 | — | `cmd/compat-cutover/main.go:82` | `main` |
| 6 | 109 | 4 | `compat/ddl.go:237` | `compileExpression` |
| 7 | 106 | 4 | `compat/inspect.go:124` | `inspectSQLiteTable` |
| 8 | 101 | 4 | `compat/store.go:261` | `canonicalValue` |
| 9 | 96 | 5 | `compat/runtime.go:14` | `(*Store).CallRoutine` |
| 10 | 91 | — | `compat/ddl.go:361` | `compileSelect` |
| 11 | 87 | 4 | `compat/sqlroutine.go:8` | `parsePostgresCatalogRoutine` |
| 12 | 84 | — | `compat/inspect.go:231` | `inspectSQLiteIndexes` |

**Candidatas a refactor (≥80 líneas o anidamiento ≥4):**

1. `inspectPostgres` (`compat/inspect.go:369`) — **227 líneas, anidamiento 4**. Encadena 3 consultas SQL inline (columnas, constraints, indexes) con scan/parse manual y ramas `Unresolved`. Patrón claro para extraer `inspectPostgresColumns` / `inspectPostgresConstraints` / `inspectPostgresIndexes`.
2. `parseCatalogSelect` (`compat/sqlselect.go:11`) — 149 líneas, **anidamiento 6** (el más profundo del repo). Parser manual con estado; la profundidad indica ramas anidadas de lookahead.
3. `Schema.Validate` (`compat/schema.go:185`) — 130 líneas, anidamiento 5; valida tablas/columnas/constraints con bucles anidados.
4. `main` de `compat-cutover` (`cmd/compat-cutover/main.go:82`) — 120 líneas de ~17 chequeos `if err != nil { os.Exit(...) }` secuenciales; lineal pero largo.
5. `compileExpression` (`compat/ddl.go:237`) — 109 líneas, anidamiento 4.
6. `inspectSQLiteTable` (`compat/inspect.go:124`) — 106 líneas, anidamiento 4.

Las #1, #2 y #3 son las prioridades más claras por combinación de longitud + profundidad.

<details>
<summary>Script reproducible (<code>/tmp/funcLen.awk</code>)</summary>

```awk
/^[[:space:]]*func / {
  if (infunc) print (lineStart-1) "\t" file ":" startLine "\t" sig
  infunc=1; startLine=NR; sig=$0
  lb=gsub(/{/,"{",$0); rb=gsub(/}/,"}",$0); depth=lb-rb; lineStart=NR; next
}
infunc {
  lineStart++
  lb=gsub(/{/,"{",$0); rb=gsub(/}/,"}",$0); depth+=lb-rb
  if (depth<=0 && lineStart>startLine) { print (lineStart-startLine+1) "\t" file ":" startLine "\t" sig; infunc=0 }
}
END { if (infunc) print (lineStart-startLine+1) "\t" file ":" startLine "\t" sig }
```
Invocación: `for f in $(find ./compat ./cmd -name "*.go" ! -name "*_test.go"); do awk -v file="$f" -f /tmp/funcLen.awk "$f"; done | sort -rn | head -15`
</details>

---

## 3. DEUDA Y RIESGOS

### 3.1 TODO/FIXME/HACK (grep real)

```
$ grep -rn -E "TODO|FIXME|HACK|XXX" --include="*.go" .   → 0 hits
```
No hay marcadores de deuda explícitos en el código. (Un único `NOTE:` aparece en `experiments/vector/postvectortype_test.go:174`, solo en un `t.Logf`.)

### 3.2 Funciones exportadas sin test directo

De los identificadores exportados del núcleo, **4 no aparecen en ningún `_test.go`**:

| Símbolo | Archivo | Razón / riesgo |
|---|---|---|
| `OpenStore` | `compat/store.go:28` | Constructor; 0 % cobertura. Probablemente cubierto vía e2e, no unitario. |
| `OpenPostgres` | `compat/store.go:61` | Constructor PG; 0 % (requiere PG vivo). |
| `RequireEquivalent` | `compat/verify.go:55` | 0 % cobertura. Lógica de verificación sin test unitario. |
| `String` | (Stringer) | Común omitir; bajo riesgo. |

### 3.3 Paths de error / funciones 0 % cobertura (9 totales)

```
compat/inspect.go:369  inspectPostgres              0.0%
compat/inspect.go:597  sqliteReferentialAction      0.0%
compat/inspect.go:614  postgresReferentialAction    0.0%
compat/inspect.go:636  inspectPostgresIndexes        0.0%
compat/inspect.go:781  extractIndexPredicate        0.0%
compat/schema.go:316   validReferentialAction        0.0%
compat/store.go:28     OpenStore                    0.0%
compat/store.go:61     OpenPostgres                 0.0%
compat/verify.go:55    RequireEquivalent           0.0%
```

- **Gap accionable (no requieren PostgreSQL):** `validReferentialAction` (`compat/schema.go:316`), `sqliteReferentialAction`/`postgresReferentialAction` (`compat/inspect.go:597/614`) y `extractIndexPredicate` (`compat/inspect.go:781`) son **funciones puras** verificadas por lectura — totalmente testeables sin infraestructura y hoy en 0 %.
- Las de catálogo Postgres (`inspectPostgres`, `inspectPostgresIndexes`, `OpenPostgres`) solo pueden ejercitarse con PG vivo (suite e2e desmontada) → deuda de cobertura heredada de la decisión de desmontar PostgreSQL.

### 3.4 Duplicación evidente

- **Boilerplate de error en `cmd/*`:** el bloque
  ```go
  unexpected, ok := cliout.StrictFlags(...)
  if !ok { os.Exit(cliout.EmitError(cliout.ErrUsage, fmt.Sprintf("...: unexpected flag %q", unexpected))) }
  ```
  se repite casi idéntico en `cmd/compat-audit/main.go:13`, `cmd/compat-copy/main.go:24`, `cmd/compat-cutover/main.go:84`. Idem el patrón `if err != nil { os.Exit(cliout.EmitError(cliout.ErrX, err.Error())) }` (16+ ocurrencias). Candidato a un helper `must(err, code)` o `cliout.Run(...)`.
- **Cierre manual de `rows` en ramas de error:** 16 ocurrencias de `rows.Close()`/`constraints.Close()` manuales (e.g. `compat/inspect.go:462,467,471` llaman `constraints.Close()` antes de cada `return Inspection{}, err`). Patrón repetido propenso a olvidos; `defer rows.Close()` + un único punto de retorno reduciría superficie.
- `compileExpression` (`compat/ddl.go:237`) y `parseCatalogExpression` (`compat/sqlparse.go:13`) tienen familias de `switch` grandes sobre el mismo conjunto de nodos de expresión — posible simetría extraíble, pero no duplicación literal.

### 3.5 Dependencias (`go list -m -u all` — red disponible, salida real)

**Dependencias directas desactualizadas:**

| Módulo | Actual | Disponible |
|---|---|---|
| `github.com/jackc/pgx/v5` | v5.7.6 | **v5.10.0** |
| `modernc.org/sqlite` | v1.39.1 | **v1.54.0** |

Indirectas relevantes atrasadas: `golang.org/x/crypto` v0.37.0 → v0.54.0, `golang.org/x/text` v0.24.0 → v0.40.0, `golang.org/x/sync` v0.16.0 → v0.22.0, `modernc.org/libc` v1.66.10 → v1.74.3, `stretchr/testify` v1.10.0 → v1.11.1.

- **Drift entre módulos:** `experiments/vector/go.mod` usa `pgx v5.10.0` (más nuevo) mientras el root usa `v5.7.6`. No rompe nada hoy (son módulos separados) pero señala que el experimento quedó fuera del flujo de actualización del root.
- Ninguna dependencia sospechosa/abandonada; `pgx` y `modernc.org/sqlite` son las mantenidas y correctas para este dominio.

### 3.6 Smells concretos (archivo:línea)

- `gofmt -l` reporta **21 archivos no formateados** — pero todos por **CRLF**: `file compat/ddl.go` confirma `with CRLF line terminators` y `gofmt -d` reescribe todo el archivo. El repo usa terminadores CRLF y `gofmt` espera LF. No es defecto de código, sí **inconsistencia de line-endings** que ensucia diffs y hace fallar cualquier `gofmt`/CI de formato. (Archivos afectados: todos los `compat/*.go`, `cmd/*`, `e2e/*`, `experiments/vector/*` listados por `gofmt -l`.)
- `experiments/vector` como módulo separado con `replace` (`experiments/vector/go.mod`) — fuera de `go test ./...`, sin cobertura en CI, con versión de `pgx` divergente.
- `(*Store).inspectPostgres` (`compat/inspect.go:369`): 3 queries SQL inline literales de 60+ líneas cada una embebidas en la función Go — difíciles de leer y testear aisladas.
- `cmd/compat-cutover/main.go:199` contiene `os.Exit(1)` suelto fuera del flujo `cliout.EmitError*` (los demás errores pasan por `cliout`), inconsistencia menor en el manejo de errores.

---

## 4. VEREDICTO Y BACKLOG

### Veredicto: 🟡 AMARILLO

**Justificación.** La arquitectura es sana y simple (núcleo hoja `compat`, sin ciclos ni capas violadas, binarios como pegamento fino), `go vet` limpio y tests pasando. Pero: (1) cobertura efectiva 64.8 % con 9 funciones a 0 % (varias puras y testeables sin PG), (2) concentración de complejidad en 3-6 funciones largas/anidadas (`inspectPostgres` 227 líneas, `parseCatalogSelect` anidamiento 6), (3) `cmd/*` sin ningún test unitario, (4) line-endings CRLF que rompen cualquier gate de formato, y (5) `experiments/vector` como módulo huérfano con `replace` y drift de versiones. Nada crítico ni de seguridad; deuda mantenida, no degrade la herramienta hoy, pero varios ítems son baratos y mejoran la postura de calidad.

### Backlog priorizado

1. **Normalizar line-endings a LF + gate `gofmt`.** Convertir los 21 archivos CRLF a LF y añadir `gofmt -l`/`git diff --check` en CI. *Archivos:* todos en `compat/`, `cmd/`, `e2e/`, `experiments/vector/`. *Esfuerzo: chico.*
2. **Tests unitarios para funciones puras hoy a 0 %.** Cubrir `validReferentialAction` (`compat/schema.go:316`), `sqliteReferentialAction`/`postgresReferentialAction` (`compat/inspect.go:597/614`), `extractIndexPredicate` (`compat/inspect.go:781`) y `RequireEquivalent` (`compat/verify.go:55`). *Esfuerzo: chico.* (Sube cobertura del núcleo sin infra.)
3. **Refactor de `inspectPostgres`** (`compat/inspect.go:369`): extraer `inspectPostgresColumns`/`Constraints`/`Indexes` para aislar las 3 queries SQL y sus scans. *Archivos:* `compat/inspect.go`. *Esfuerzo: mediano.*
4. **Reducir anidamiento de `parseCatalogSelect`** (`compat/sqlselect.go:11`, prof. 6) y revisar `Schema.Validate` (`compat/schema.go:185`) y `compileExpression` (`compat/ddl.go:237`) — extraer helpers por nodo. *Esfuerzo: mediano.*
5. **Deduplicar el boilerplate de flags/errores en `cmd/*`.** Helper `cliout` que centralice parseo estricto + `os.Exit(EmitError(...))` y el `os.Exit(1)` suelto en `cmd/compat-cutover/main.go:199`. *Archivos:* `cmd/internal/cliout/cliout.go`, los 3 `cmd/*/main.go`. *Esfuerzo: chico.*
6. **Tests unitarios mínimos para `cmd/*`** (al menos `cliout` y parseo de flags de cada binario, sin PG). *Archivos:* `cmd/internal/cliout`, `cmd/*`. *Esfuerzo: mediano.*
7. **Actualizar dependencias directas y decidir el destino de `experiments/vector`.** Subir `pgx` v5.7.6→v5.10.0 y `modernc.org/sqlite` v1.39.1→v1.54.0 en el root; alinear o eliminar el módulo `experiments/vector` (drift de `pgx`, `replace`). *Archivos:* `go.mod`, `go.sum`, `experiments/vector/go.mod`. *Esfuerzo: mediano.*

---

### `git status --short` al final de la tarea

```
?? docs/reports/ESTADO-CODIGO-REPORT.md
```

(Único cambio respecto del árbol: este reporte nuevo. Los paths `../*` que muestra `git status` son archivos no-versionados **fuera** del repo, no modificaciones del repo.)