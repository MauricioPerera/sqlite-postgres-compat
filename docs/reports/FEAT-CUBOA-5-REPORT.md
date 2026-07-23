# FEAT-CUBOA-5 — Dominios SQL (objeto canónico, asimétrico y honesto)

**Fecha:** 2026-07-23
**Ámbito:** soportar DOMINIOS (tipo con nombre = tipo base + `CHECK` opcional + `NOT NULL`/`DEFAULT` opcionales) como objeto canónico, con `CREATE DOMAIN` nativo en PostgreSQL y compilación INLINE (tipo base + `CHECK`) en SQLite, semánticamente equivalente, documentando explícitamente la asimetría que ninguna otra feature tiene.
**Motor de PostgreSQL usado para verificación:** PostgreSQL 17 en `postgres://postgres:***@31.220.22.176:5434/postgres?sslmode=disable` (password enmascarada). Base efímera `compat_e2e_%` creada y **dropeada** por el harness (`TestMain`); los objetos de prueba (dominios `cuboa5_*`, tablas `cuboa5_*`) viven **dentro** de esa base efímera. **0 bases** `compat_e2e_%`/`cuboa%` remanentes al terminar (verificado por consulta a `pg_database`).
**Archivos tocados:** `compat/schema.go`, `compat/schema_test.go`, `compat/ddl.go`, `compat/ddl_test.go`, `compat/inspect.go`, `e2e/system_test.go`, `README.md`, `docs/TESTING.md`, `docs/COMPATIBILITY.md`, `AGENTS.md`, este reporte. No se tocó `compat/sqlparse.go`, `sqlselect.go`, `store.go`, `capture.go`, `replicate.go`, `cmd/**`, ni reportes existentes. (`inspect_test.go` también recibió un test de round-trip por metadata; está dentro de la lista permitida.)

---

## 1. Resumen ejecutivo

| Capacidad | Estado | Evidencia |
|---|---|---|
| Compilar dominio: PG `CREATE DOMAIN` nativo + columna que lo usa; SQLite inline (tipo base + CHECK + NOT NULL/DEFAULT) | **ENTRA** — SQL exacto en ambos motores | §3.2, unit congelado |
| Equivalencia de datos entre dominio PG (nativo) e inline SQLite | **ENTRA** — `equivalent=true` contra PG real | §3.5 |
| Validación: rechaza CHECK fuera de gramática, referencia a dominio inexistente, tipo que no coincide, columna no neutra | **ENTRA** | §3.2, §3.3 |
| Round-trip vía metadata canónica (`__compat_schema`) en ambos motores | **ENTRA** — exacto (prueba principal) | §3.4 |
| Inspección externa SQLite | **Asimetría documentada** — no se reconstruye como dominio (aparece como columna+CHECK, exacto **como columna**) | §4 |
| Inspección externa PostgreSQL | **Unresolved honesto** (`domain_column`) — el deparse del CHECK usa `VALUE`, fuera de la gramática; no se degrada en silencio a texto | §3.5, §4 |

Regla de oro respetada: la ruta canónica round-tripea exacto; la inspección externa nunca fabrica un dominio equivocado ni degrada en silencio — cuando no puede reconstruirlo exacto, lo deja en `Unresolved`.

---

## 2. Diseño

### 2.1 Modelo (`compat/schema.go`)

`Schema` gana `Domains []Domain` (`omitempty`). `Domain{Name string, Type Type /* familia base */, Check *Expression, NotNull bool, Default *Expression}`.

**Cómo la columna referencia el dominio (decisión):** `Column` gana un campo aditivo `omitempty` `DomainRef string` (JSON `domain`). Se eligió **`DomainRef` sobre "reusar `Type.Family` como nombre del dominio"** porque es lo menos invasivo: una columna sin dominio serializa y compila byte-idéntico (campo omitido), y no contamina el conjunto cerrado de familias de tipo (`knownTypeFamilies`, `compileType`) que otras rutas recorren.

**Restricción no evidente que fijó el diseño:** la cadena de datos (`store.go`, prohibido tocar) canoniza cada valor con `column.Type.Family` en `exportTable`/`insertRow`. Por eso una columna con dominio **debe conservar `Type` igual al tipo base del dominio** — no puede tener `Type` vacío. Esto es en realidad lo más limpio: para la ruta de datos la columna es una columna normal tipada; sólo la compilación de DDL y la validación son conscientes del dominio. `Schema.Validate` exige, para una columna con `DomainRef`: dominio existente, `reflect.DeepEqual(column.Type, domain.Type)`, y neutralidad (`Nullable=true`, sin `Default` ni `Generated` propios) — así el dominio es la única fuente de la restricción y PG(nativo) ≡ SQLite(inline) sobre datos idénticos, trivialmente.

**Placeholder del valor:** el `CHECK` del dominio referencia el valor bajo prueba con el nodo `Expression{Kind:"domain_value"}`. No hace falta tocar `sqlparse.go`: en la ruta canónica el `Expression` viaja como JSON (nunca se re-parsea desde texto), y para compilar basta un caso nuevo en `compileExpressionIn`.

### 2.2 Compilación (`compat/ddl.go`)

- `CompileDDL` construye un `map[string]Domain` y emite `compileDomain` **antes** de las tablas. En SQLite `compileDomain` devuelve `""` (no hay `CREATE DOMAIN`) y no se emite nada.
- `compileDomain` (sólo PG): `CREATE DOMAIN "n" AS <tipo base> [DEFAULT …] [NOT NULL] [CHECK (…)]`. El `domain_value` del CHECK compila a `VALUE`.
- `compileTable` recibe el mapa de dominios; para una columna con `DomainRef` delega en `compileColumnWithDomain`:
  - **PG:** el tipo SQL de la columna es el nombre del dominio (`"col" "dominio"`); el dominio ya carga CHECK/NOT NULL/DEFAULT.
  - **SQLite:** `"col" <tipo base> [NOT NULL] [DEFAULT …] [CHECK (…)]`, con el CHECK reescrito por `substituteDomainValue` (reemplaza recursivamente los nodos `domain_value` por `column` con el nombre de la columna).
- Caso nuevo en `compileExpressionIn`: `"domain_value"` → `VALUE` en PG; en SQLite es un error (nunca se alcanza porque la ruta SQLite reescribe el placeholder antes de compilar). La validez gramatical del CHECK la impone `compileExpression`, igual que columnas generadas / `CHECK`.

### 2.3 Inspección (`compat/inspect.go`)

- **Metadata canónica:** automática — `Domains` es parte de `Schema` y se serializa en `__compat_schema`; `InspectSchema` la lee, valida y devuelve `Source=canonical_metadata`, `Exact=true`.
- **Externa PG:** `inspectPostgresColumns` añade `c.domain_name` al `SELECT` de `information_schema.columns`; si es no nulo, la columna es de tipo dominio → se agrega un `CatalogObject{Kind:"domain_column", Reason:…}` a `Unresolved` (bloquea `Exact`), en vez de degradarse en silencio al tipo escalar subyacente. Único cambio; las columnas no-dominio no se ven afectadas (`domain_name` nulo).
- **Externa SQLite:** sin cambios de código — un dominio inline aparece como columna + `CHECK` de tabla y el código existente ya lo reconstruye como columna normal + restricción `CHECK`. Documentado como la asimetría aceptada.

---

## 3. Verificación (salida real)

### 3.1 `.\scripts\check.ps1` (gofmt + vet + unit) — VERDE

```
gofmt: OK
go vet: OK
?   	example.com/sqlite-postgres-compat/cmd/compat	[no test files]
ok  	example.com/sqlite-postgres-compat/cmd/internal/cliout	2.509s
ok  	example.com/sqlite-postgres-compat/compat	2.439s
go test: OK
```

`go vet -tags=e2e ./e2e` → sin salida (VERDE, exit 0).

### 3.2 Unit congelado: compile + rechazo de CHECK fuera de gramática

`TestCompileDomainForBothEngines` — SQL exacto. PostgreSQL (4 sentencias, dominios antes de la tabla):

```
CREATE DOMAIN "positive_qty" AS BIGINT NOT NULL CHECK ((VALUE > 0))
CREATE DOMAIN "email_addr" AS TEXT CHECK ((LENGTH(VALUE) > 0))
CREATE DOMAIN "status" AS TEXT DEFAULT 'new' NOT NULL
CREATE TABLE "items" ("id" BIGINT NOT NULL, "qty" "positive_qty", "label" "email_addr", "st" "status", PRIMARY KEY ("id"))
```

SQLite (1 sentencia, sin `CREATE DOMAIN`, dominios inline por columna):

```
CREATE TABLE "items" ("id" INTEGER NOT NULL, "qty" INTEGER NOT NULL CHECK (("qty" > 0)), "label" TEXT CHECK ((LENGTH("label") > 0)), "st" TEXT NOT NULL DEFAULT 'new', PRIMARY KEY ("id"))
```

`TestCompileDomainRejectsOutOfGrammarCheck` — un CHECK con función fuera de la allowlist (`substr`) es rechazado por `CompileDDL` en ambos motores.

### 3.3 Validación (unit)

`TestSchemaValidateAcceptsDomain` pasa; `TestSchemaValidateRejectsInvalidDomains` (subtests): dominio sin nombre, duplicado, sin tipo base, tipo base desconocido, referencia a dominio inexistente, tipo que no coincide, y columna no neutra (NOT NULL / default propio / generada) — todos rechazados con mensaje esperado.

```
--- PASS: TestCompileDomainForBothEngines (0.00s)
--- PASS: TestCompileDomainRejectsOutOfGrammarCheck (0.00s)
--- PASS: TestSchemaValidateAcceptsDomain (0.00s)
--- PASS: TestSchemaValidateRejectsInvalidDomains (0.00s)
```

### 3.4 Round-trip vía metadata canónica (`__compat_schema`) — exacto

`TestInspectCanonicalMetadataDomainRoundTrip` — `ApplySchema` (SQLite, dominio inline) con un dominio usado por una columna; `InspectSchema` devuelve `Source=canonical_metadata`, `Exact=true` y `reflect.DeepEqual(schema, inspection.Schema)` (dominio + `DomainRef` incluidos).

```
--- PASS: TestInspectCanonicalMetadataDomainRoundTrip (0.00s)
```

### 3.5 EQUIVALENCIA contra PostgreSQL 17 REAL (E2E)

`TestSystemDomainCopyEquivalentAndExternalInspection` (tag `e2e`, `e2e/system_test.go`), dos fases:

**Fase 1 — copy SQLite→PG, `equivalent=true`.** Esquema con dos dominios (`cuboa5_positive_qty AS integer CHECK (VALUE > 0) NOT NULL`, `cuboa5_nonempty_label AS text CHECK (length(VALUE) > 0)`) usados por columnas de `cuboa5_items`. `ApplySchema` en SQLite real (dominios inline), inserta filas válidas, `ExportSnapshot` → `ImportSnapshot` en PG real (`CREATE DOMAIN` nativo + columnas que lo usan + filas) → re-export y `assertEquivalent` (`RequireEquivalent`). Además se prueba que el dominio nativo **aplica de verdad**: un `INSERT` con `qty=0` es rechazado por el motor.

**Fase 2 — inspección externa PG honesta.** DDL crudo (`CREATE DOMAIN cuboa5_ext_pos AS integer CHECK (VALUE > 0)`, tabla con columna de ese dominio) en un `public` recreado (sin metadata); `InspectSchema` → `Exact=false` y un `Unresolved{Kind:"domain_column", Name:"cuboa5_ext.qty"}`.

```
=== RUN   TestSystemDomainCopyEquivalentAndExternalInspection
--- PASS: TestSystemDomainCopyEquivalentAndExternalInspection (2.68s)
PASS
ok  	example.com/sqlite-postgres-compat/e2e	5.833s
```

Suite E2E completa contra PG real (`go test -tags=e2e ./e2e -count=1`): **47 pruebas** de nivel superior, **46 superadas** y **1 fallida de forma intencional** (`TestSystemClaimsExactCoverageForRequiredFeatureFamilies`, el gate del 100 % rojo por diseño — las familias genéricas siguen `unknown`). Sin ninguna otra falla. **0 bases** `compat_e2e_%`/`cuboa%` remanentes en PostgreSQL al terminar.

Conteo verificado: `(Select-String -Path e2e\*.go -Pattern "^func Test[A-Z]" | Where-Object { $_.Line -notmatch "TestMain" }).Count` → **47** (antes 46; +1 test top-level). `README.md` y `docs/TESTING.md` actualizados a 47 / 46 superadas.

---

## 4. Asimetría documentada (evidencia)

El mismo dominio se comporta distinto según la ruta de inspección — esto es inevitable porque un dominio es una restricción de esquema y SQLite no tiene el concepto:

| Ruta | Reconstrucción | Estado |
|---|---|---|
| Metadata canónica (`__compat_schema`), SQLite y PG | El `Domain` completo + `DomainRef`, byte-idéntico | **Exacto** (§3.4) |
| Inspección externa **SQLite** | El dominio nunca existió; la columna inline se reconstruye como **columna + CHECK** (exacta *como columna*, no como dominio) | Aceptado — no es divergencia de datos, sólo de forma |
| Inspección externa **PostgreSQL** | PG deparsa el CHECK con `VALUE` (y `::type`), fuera de la gramática; la columna de tipo dominio queda `domain_column` en `Unresolved` | **Unresolved honesto** (`Exact=false`), no degradación silenciosa (§3.5 fase 2) |

Esta es la diferencia respecto de features anteriores (columnas generadas, índices de expresión), que sí eran simétricas en inspección externa cuando el deparse caía en la gramática. Los dominios **no** lo son porque el valor bajo prueba (`VALUE`) y la ausencia del objeto en SQLite rompen la simetría por construcción, no por una limitación de implementación.

---

## 5. Trade-offs y notas

- **`Type` redundante en la columna con dominio.** Es obligatorio, no cosmético: `store.go` (prohibido tocar) canoniza los datos por `column.Type.Family`. Se valida que coincida con el tipo base del dominio, garantizando que PG(nativo) y SQLite(inline) almacenan el mismo valor. Es lo menos invasivo posible sin tocar la cadena de datos.
- **Columna con dominio debe ser neutra.** `Nullable=true`, sin `Default`/`Generated` propios. Mantiene el dominio como única fuente de la restricción y hace la equivalencia trivial; una columna que quiera añadir restricciones propias sobre un dominio queda fuera de alcance a propósito (se rechaza en `Validate`).
- **`domain_value` sin tocar `sqlparse.go`.** El placeholder se maneja en compilación (`compileExpressionIn` + `substituteDomainValue`), no en el parser. La ruta canónica nunca re-parsea el CHECK desde texto, así que no se necesita reconocer `VALUE` en el parser — lo que además explica por qué la inspección externa PG no puede reconstruir el dominio exacto (y por eso queda `Unresolved`).
- **`LIKE→ILIKE` heredado** aplica si un CHECK de dominio usa `LIKE` (igual que en `CHECK`/vistas): semánticamente equivalente, texto distinto entre motores. Los tests usan comparaciones numéricas/`length` para congelar SQL idéntico.
- **Sin cambios de firma pública.** `CompileDDL`/`ApplySchema`/`ImportSnapshot` inalterados; los helpers nuevos (`compileDomain`, `compileColumnWithDomain`, `substituteDomainValue`, `validateDomains`) son privados. `compileTable`/`validateTable`/`validateTableColumn` cambiaron de firma pero son privados de `compat`.

---

## 6. Limpieza

- Sin procesos en foreground que no terminen solos. Nada escrito fuera del repo (salvo un `chkdb.go` temporal en `%TEMP%` para contar bases remanentes, ya borrado). Password del DSN nunca en literal (enmascarada `***`). **0 bases** `compat_e2e_%`/`cuboa%` remanentes en PostgreSQL.
- `git status` sólo muestra los archivos permitidos modificados + este reporte.
