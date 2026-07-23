# FIX-AUDIT11-REPORT — Cierre del Hallazgo A11-1 (self-reference de CTE por case-fold)

**Fecha:** 2026-07-23
**Autor:** dev senior Go, sesión aislada.
**Hallazgo cerrado:** AUDIT11 **A11-1** (MEDIA) — `WITH T AS (SELECT id FROM t) …` (CTE `T`
mayúscula, tabla base `t` minúscula, identificadores NO citados) no se detectaba como
auto-referencia porque la comparación era case-sensitive (`==`), reintroduciendo la misma
clase de divergencia runtime que el fix M2 (commit `338edb3`) se propuso cerrar.
**Motores de verificación:** SQLite real (`modernc.org/sqlite`) y **PostgreSQL 17 real**
(`postgres://postgres:***@31.220.22.176:5434/postgres?sslmode=disable`, password enmascarada).

---

## 1. Diagnóstico del AST: ¿preserva "citado vs no citado"?

**No.** El parser normaliza identificadores en `parseCatalogIdentifier` (compat/sqlparse.go:551):
para un identificador citado `"T"` **quita** las comillas dobles (y des-escapa `""`→`"`),
devolviendo la cadena desnuda `T`; para uno no citado devuelve `T` tal cual. En ambos casos el
resultado que llega a `CommonTableExpr.Name` y `TableSource.Table` es la misma cadena `T`, sin
ninguna marca de si vino citado. La señal citado/no-citado **se pierde en el parseo y no existe
en el AST**.

Preservar esa señal exigiría cambiar el tipo de retorno de `parseCatalogIdentifier` y propagar un
flag por todo el parser y el modelo (`schema.go`) — archivos **prohibidos** por el encargo
(`sqlparse.go`, `schema.go` no se tocan) y, aun permitidos, un cambio invasivo y transversal.

## 2. Diseño elegido y por qué

**Comparar identificadores case-insensitive para ASCII** en los dos únicos sitios de comparación
de nombres de la detección de self-reference, dejando intactos parser y modelo. Es exactamente la
opción "más simple y correcta" recomendada por el auditor y el fallback descrito en el encargo.

Se agregó un helper local en `compat/sqlselect.go`:

```go
func identifiersFoldEqual(a, b string) bool { // ASCII-only case fold, byte a byte
	if len(a) != len(b) { return false }
	for i := 0; i < len(a); i++ {
		if asciiLower(a[i]) != asciiLower(b[i]) { return false }
	}
	return true
}
func asciiLower(b byte) byte { if b >= 'A' && b <= 'Z' { return b + ('a' - 'A') }; return b }
```

y se reemplazaron las dos comparaciones `==`:

- `cteQueryReferencesName` (sqlselect.go): el guard de shadowing `cte.Name == name` → `identifiersFoldEqual(cte.Name, name)`.
- `tableSourceReferencesName` (sqlselect.go): `source.Table == name` → `identifiersFoldEqual(source.Table, name)`.

**Un solo punto de verdad cubre parse y compile:** tanto `stripCatalogWith` (parser, sqlselect.go:329)
como `compileWith` (compilador de AST a mano, ddl.go:734) invocan `cteQueryReferencesName`. Arreglar
el helper cierra los dos caminos sin tocar `ddl.go` en la lógica de comparación.

**ASCII-only, no Unicode:** se usa un plegado ASCII propio en vez de `strings.EqualFold` porque
SQLite pliega identificadores **sólo** para A–Z ASCII (`sqlite3StrICmp`) y PostgreSQL baja a minúscula
los no citados con downcasing ASCII; `EqualFold` haría plegado Unicode más amplio (p.ej. `İ`/`i`,
`ß`/`ss`) que ningún motor aplica, lo que podría rechazar por error nombres que ambos motores tratan
como distintos. Los bytes no-ASCII (incluidos los de runes UTF-8 multibyte) deben coincidir exactos.

## 3. Trade-off asumido (documentado)

Como el AST no distingue `"T"` citado de `T` sin citar, la comparación **no puede** preservar el
caso —legítimo en ambos motores— en que alguien cita `"T"` deliberadamente para que sea un nombre
**distinto** de la tabla base `t`. Plegar el case rechazará esa vista rara. Es la elección
**conservadora y preferida**: ese patrón (un identificador citado en mayúscula que colisiona bajo el
plegado de un motor con uno no citado) es en sí un riesgo de divergencia entre motores que preferimos
rechazar antes que arriesgar DDL byte-idéntico con runtime divergente. A cambio, se cierra el flanco
A11-1 para **todo** el camino no citado — el común y peligroso. Ningún falso positivo entre nombres
genuinamente distintos (verificado abajo).

---

## 4. Verificación (salida real)

### 4.1 Unit congelados (compat) — `go test ./compat/ -run '...' -v`

```
=== RUN   TestCompileRejectsSelfReferentialCTE
--- PASS: TestCompileRejectsSelfReferentialCTE (0.00s)
=== RUN   TestCompileRejectsCaseFoldSelfReferentialCTE
--- PASS: TestCompileRejectsCaseFoldSelfReferentialCTE (0.00s)
=== RUN   TestParseCatalogSelectRejectsRecursiveCTE
--- PASS: TestParseCatalogSelectRejectsRecursiveCTE (0.00s)
=== RUN   TestParseCatalogSelectRejectsSelfReferentialCTE
--- PASS: TestParseCatalogSelectRejectsSelfReferentialCTE (0.00s)
=== RUN   TestParseCatalogSelectRejectsSelfReferentialCTEInJoin
--- PASS: TestParseCatalogSelectRejectsSelfReferentialCTEInJoin (0.00s)
=== RUN   TestParseCatalogSelectAcceptsSiblingCTEReference
--- PASS: TestParseCatalogSelectAcceptsSiblingCTEReference (0.00s)
=== RUN   TestParseCatalogSelectRejectsCaseFoldSelfReferentialCTE
--- PASS: TestParseCatalogSelectRejectsCaseFoldSelfReferentialCTE (0.00s)
=== RUN   TestParseCatalogSelectAcceptsDistinctNamedCTE
--- PASS: TestParseCatalogSelectAcceptsDistinctNamedCTE (0.00s)
PASS
ok  	example.com/sqlite-postgres-compat/compat	2.387s
```

Cobertura de los tres requisitos del encargo:
- **`WITH T AS (SELECT id FROM t)` ahora se rechaza en parse y compile, ambos motores:**
  `TestParseCatalogSelectRejectsCaseFoldSelfReferentialCTE` (parse) +
  `TestCompileRejectsCaseFoldSelfReferentialCTE` (compile, itera `SQLite` y `Postgres`).
- **No-regresión del caso exacto-case de M2:** `TestParseCatalogSelectRejectsSelfReferentialCTE`,
  `TestParseCatalogSelectRejectsSelfReferentialCTEInJoin`, `TestCompileRejectsSelfReferentialCTE` siguen PASS.
- **No falsos positivos con nombres claramente distintos:** `TestParseCatalogSelectAcceptsDistinctNamedCTE`
  (`WITH summary AS (SELECT id FROM orders) …`) + `TestParseCatalogSelectAcceptsSiblingCTEReference` PASS.

### 4.2 Confirmación contra PG17 real — reproducción exacta de A11-1

Test e2e nuevo `TestSystemRejectsAudit11CaseFoldSelfReferentialCTE`: tabla base `t` real + vista con
CTE `T` que lee de `t` (AST hand-built, el mismo camino que A11-1 demostró divergente). Confirma que
`ApplySchema` **rechaza en compilación** en SQLite y en PG17 **antes** de emitir DDL, y que la vista
**no existe** en PostgreSQL tras el rechazo.

```
=== RUN   TestSystemRejectsAudit10DivergentDDLBeforeEngines
=== RUN   TestSystemRejectsAudit10DivergentDDLBeforeEngines/M1_gen_random_uuid_in_CHECK
=== RUN   TestSystemRejectsAudit10DivergentDDLBeforeEngines/M2_self-referential_CTE
--- PASS: TestSystemRejectsAudit10DivergentDDLBeforeEngines (0.81s)
    --- PASS: TestSystemRejectsAudit10DivergentDDLBeforeEngines/M1_gen_random_uuid_in_CHECK (0.41s)
    --- PASS: TestSystemRejectsAudit10DivergentDDLBeforeEngines/M2_self-referential_CTE (0.40s)
=== RUN   TestSystemRejectsAudit11CaseFoldSelfReferentialCTE
--- PASS: TestSystemRejectsAudit11CaseFoldSelfReferentialCTE (0.37s)
PASS
ok  	example.com/sqlite-postgres-compat/e2e	4.398s
```

`ApplySchema` devolvió error conteniendo `self-referential` en ambos motores y
`information_schema.views` en PG quedó en 0 filas para `audit11_casefold_selfref` → **ningún DDL
escapó a los motores**, exactamente el comportamiento que A11-1 pedía.

### 4.3 Gate completo y vet

`.\scripts\check.ps1` (gofmt + `go vet ./...` + `go test ./...`):

```
gofmt: OK
go vet: OK
go test: OK
?   	example.com/sqlite-postgres-compat/cmd/compat	[no test files]
ok  	example.com/sqlite-postgres-compat/cmd/internal/cliout	2.493s
ok  	example.com/sqlite-postgres-compat/compat	2.311s
```

`go vet -tags=e2e ./e2e`: **verde** (sin salida).

Suite e2e completa contra PG17 real (`go test -tags=e2e ./e2e -count=1`): el **único** fallo es el
intencional documentado `TestSystemClaimsExactCoverageForRequiredFeatureFamilies` (las familias
genéricas no-canónicas quedan `unknown` a propósito). Un `grep` de fallos excluyendo ese test devolvió
vacío → **0 regresiones**.

### 4.4 Conteo de tests e2e top-level

Se agregó 1 test top-level e2e (`TestSystemRejectsAudit11CaseFoldSelfReferentialCTE`).

```
$ grep -rhE "^func Test[A-Z]" e2e/*.go | grep -v TestMain | wc -l
50
```

Antes 49, ahora **50** (49 superadas + 1 fallo intencional). Actualizado en `README.md` y
`docs/TESTING.md`.

---

## 5. Archivos tocados

| Archivo | Cambio |
|---|---|
| `compat/sqlselect.go` | Helper `identifiersFoldEqual`/`asciiLower`; comparaciones `==` → fold en `cteQueryReferencesName` y `tableSourceReferencesName`. |
| `compat/sqlselect_test.go` | `TestParseCatalogSelectRejectsCaseFoldSelfReferentialCTE`, `TestParseCatalogSelectAcceptsDistinctNamedCTE`. |
| `compat/ddl_test.go` | `TestCompileRejectsCaseFoldSelfReferentialCTE`. |
| `e2e/system_test.go` | `TestSystemRejectsAudit11CaseFoldSelfReferentialCTE` (PG17 real). |
| `README.md`, `docs/TESTING.md` | Conteo e2e 49→50, superadas 48→49. |
| `docs/COMPATIBILITY.md`, `AGENTS.md` | Matiz: comparación de nombres de self-reference ahora ASCII case-insensitive, con el trade-off del identificador citado. |
| `docs/reports/FIX-AUDIT11-REPORT.md` | Este reporte. |

No se tocó `compat/schema.go`, `sqlparse.go`, `inspect.go`, `store.go`, `capture.go`,
`replicate.go`, `cmd/**` ni reportes previos. Sin `git add/commit/stash`. Password siempre `***`.
Ninguna base PG persistente creada por este fix (el harness e2e crea y dropea su base efímera; no se
usaron bases `fixaudit11_`). Sin procesos en foreground colgados.
