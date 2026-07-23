# FIX-A7-REPORT — Cierre de AUDIT7 MEDIA H1/H2

Fecha: 2026-07-22
Autor: dev de continuación (retoma trabajo dejado a medias por cuota del proveedor).
Repo: `sqlite-postgres-compat`, branch `main`. PG real usado para verificación:
`postgres://postgres:***@31.220.22.176:5434/postgres?sslmode=disable` (password enmascarado).

## 1. Qué dejó hecho el dev anterior (H1, ya verde, no tocado)

Hallazgo AUDIT7-A **H1**: el parser de catálogo (`compat/sqlparse.go`,
`compat/sqlselect.go`) exigía exactamente un espacio simple en operadores y
keywords multi-palabra (`IS NULL`, `IS NOT NULL`, `AS SELECT`), mientras que
`GROUP BY`/`ORDER BY`/`LEFT OUTER JOIN` ya toleraban cualquier run de
whitespace vía `keywordMatchSpan`/`topLevelKeyword`. Con doble espacio o tabs,
`col IS  NULL` o `... AS\tSELECT ...` fallaban con error de parseo aunque
SQLite y Postgres los tratan igual que con un solo espacio.

El fix (ya en el árbol, verificado en verde por el PM):

- `compat/sqlparse.go`: nueva función `catalogOperatorSpan` (reemplaza el
  cálculo de `start` fijo por ancho de operador en `splitCatalogOperator`).
  Para operadores sin espacio hace match verbatim de ancho fijo; para
  operadores multi-palabra (`IS NULL`, `IS NOT NULL`) hace el mismo escaneo
  derecha-a-izquierda tolerante a whitespace que ya usaba `keywordMatchSpan`
  para `GROUP BY`/`ORDER BY`.
- `compat/sqlselect.go`: `stripCatalogSelectHeader` ahora ubica el separador
  `AS SELECT` con `topLevelKeyword`/`keywordMatchSpan` (el mismo mecanismo que
  ya usan las demás cláusulas) en vez de `strings.Index(upper, " AS SELECT ")`
  con ancho fijo.
- Tests nuevos en `compat/sqlparse_test.go` y `compat/sqlselect_test.go`
  cubriendo doble espacio y tabs en `IS NULL`, `IS NOT NULL`, `AS SELECT`.

No se tocó nada de esto: solo se leyó para entender el mecanismo de
tolerancia (`keywordMatchSpan` / `catalogOperatorSpan`) y reutilizarlo en el
barrido de la sección 3.

## 2. Fix del script (AUDIT7-C H2)

### Problema

`scripts/test-system.ps1` corría `go test -tags=e2e ./e2e -v -count=1
-timeout 60s` y propagaba `$LASTEXITCODE` tal cual. La suite e2e tiene
exactamente un fallo intencional y documentado —
`TestSystemClaimsExactCoverageForRequiredFeatureFamilies`
(`e2e/system_test.go:1012`) — que afirma cobertura `exact` para familias
genéricas (`foreign_keys`, `check_constraints`, `indexes`, `triggers`,
`views`, `stored_routines`, `full_text`) que son `unknown` por diseño
(`compat/features.go`). Es un contrato de baseline, no un bug: no se puede
usar `t.Skip` ni borrarlo (instrucción explícita del PM). Como el script
propagaba el exit code crudo del `go test`, **siempre** terminaba en `exit 1`,
inutilizándolo como gate de CI.

### Fix

Reescrito `scripts/test-system.ps1` (mismo estilo que `scripts/check.ps1`:
`$ErrorActionPreference='Stop'`, `Write-Host` con color, exit temprano):

1. Corre `go test ./...` y `go vet ./...` igual que antes, propagando su exit
   code sin cambios.
2. Corre la suite e2e con `-json` en vez de `-v` (parseo estructurado en vez
   de scraping de texto) y arma un diccionario `Test → pass/fail` solo con
   resultados **top-level** (se excluyen subtests, que tienen `/` en el
   nombre — los 7 subtests de la familia genérica fallan siempre y no deben
   contarse como fallos "extra").
3. Lógica de veredicto:
   - Si no se pudo parsear ningún resultado (fallo de build/setup antes de
     correr ningún test) → `exit 1` con el output crudo.
   - Si hay algún test top-level fallado que **no sea**
     `TestSystemClaimsExactCoverageForRequiredFeatureFamilies` → `exit 1`
     (fallo real, no el baseline).
   - Si el test intencional **no** aparece como fallado (pasó, o no corrió)
     → `exit 1` con mensaje explícito: puede significar que la cobertura
     mejoró de verdad (hay que actualizar `docs/COMPATIBILITY.md` y el
     baseline del script) o que algo anda mal en la corrida.
   - Si el único fallo top-level es exactamente ese test → `exit 0` con
     mensaje claro.
4. Parámetro nuevo `-E2ERun` (opcional, default vacío): pasa `-run` a
   `go test` para la fase e2e. Solo sirve para diagnosticar el propio script
   (demostrado en la verificación negativa abajo); el uso normal no lo
   necesita.

Archivo tocado: `scripts/test-system.ps1` únicamente.

## 3. Barrido de completitud del H1

Objetivo: encontrar en `compat/sqlparse.go`, `compat/sqlselect.go`,
`compat/ddl.go` literales de keywords multi-palabra con espacio simple
hardcodeado que el fix de H1 no haya cubierto (mecanismo de tolerancia
existente: `keywordMatchSpan`/`topLevelKeyword` en `sqlselect.go`,
`catalogOperatorSpan` en `sqlparse.go`).

### Comandos y resultado

```
$ grep -n '"[A-Z]* [A-Z]" \|"[A-Z]* [A-Z]*"' compat/sqlparse.go compat/sqlselect.go compat/ddl.go
```
```
$ grep -nE '"[A-Z][A-Z ]* [A-Z][A-Z ]*"' compat/sqlparse.go compat/sqlselect.go compat/ddl.go
```

Resultado: todos los literales multi-palabra usados para **parsear** SQL de
entrada ya pasan por el mecanismo tolerante:

| Sitio | Keyword | Mecanismo |
|---|---|---|
| `sqlparse.go:25` | `IS NOT NULL`, `IS NULL` | `catalogOperatorSpan` (fix H1) |
| `sqlselect.go:78,82` | `AS SELECT` | `topLevelKeyword`/`keywordMatchSpan` (fix H1) |
| `sqlselect.go:105` | `GROUP BY` | `topLevelKeyword`/`keywordMatchSpan` (ya existente antes de H1) |
| `sqlselect.go:120` | `ORDER BY` | `topLevelKeyword`/`keywordMatchSpan` (ya existente) |
| `sqlselect.go:435` | `LEFT OUTER JOIN` | `topLevelKeyword`/`keywordMatchSpan` (ya existente) |
| `sqlselect.go:436-438` | `LEFT JOIN`, `INNER JOIN`, `CROSS JOIN` | ídem |

Los demás literales multi-palabra encontrados en `compat/ddl.go` (`DOUBLE
PRECISION`, `SET NULL`, `SET DEFAULT`, `NO ACTION`, `FOR EACH ROW`, etc.) no
son parseo de entrada: `ddl.go` es el **compilador** (`compat.Schema`/AST Go
→ texto SQL de salida hacia SQLite/Postgres), nunca vuelve a parsear ese
texto, así que no hay whitespace de usuario que tolerar ahí — son
concatenaciones de string fijas que el propio código emite.

Caso límite revisado y descartado: `parsePostgresCatalogDefault`
(`sqlparse.go:158-173`) hace `switch` sobre el nombre de tipo de un cast
Postgres (`character varying`, `double precision`, `timestamp without time
zone`, etc.) extraído de `DEFAULT ...::tipo` **leído de vuelta desde el
catálogo de Postgres** (`information_schema`/`pg_catalog`), no tecleado por
un usuario en SQL libre. Postgres normaliza esos nombres de tipo al
serializar el default (siempre un espacio simple, sin tabs), así que no hay
variabilidad de whitespace real que tolerar en ese punto; se dejó sin tocar.

**Conclusión del barrido: no se encontró ningún sitio adicional sin
tolerancia.** No hizo falta tocar `compat/sqlparse.go`, `compat/sqlselect.go`
ni `compat/ddl.go` ni sus tests.

## 4. Verificación obligatoria (salida real)

### 4.1 `.\scripts\check.ps1`

```
$ .\scripts\check.ps1
gofmt: OK
go vet: OK
?    example.com/sqlite-postgres-compat/cmd/compat  [no test files]
ok   example.com/sqlite-postgres-compat/cmd/internal/cliout  2.385s
ok   example.com/sqlite-postgres-compat/compat  2.378s
go test: OK
```

Verde.

### 4.2 `.\scripts\test-system.ps1` — caso positivo (contra PG real del VPS)

DSN usado (password enmascarado en este reporte, valor real usado en la
corrida): `postgres://postgres:***@31.220.22.176:5434/postgres?sslmode=disable`

```
$ .\scripts\test-system.ps1 -PostgresDsn $dsn
?    example.com/sqlite-postgres-compat/cmd/compat  [no test files]
ok   example.com/sqlite-postgres-compat/cmd/internal/cliout  (cached)
ok   example.com/sqlite-postgres-compat/compat  2.384s
test-system: OK - the only e2e failure is the documented baseline 'TestSystemClaimsExactCoverageForRequiredFeatureFamilies' (docs/reports/AUDIT7-C.md, finding H2).
EXITCODE=0
```

`exit 0`, detectó el único fallo intencional (7 subtests de familias
`unknown`) y no lo contó como fallo del gate.

### 4.3 `.\scripts\test-system.ps1` — caso negativo (ausencia del fallo intencional)

Sin tocar el repo: se usó el parámetro `-E2ERun` para correr la suite e2e con
un filtro que **excluye** el test intencional (solo corre
`TestDatabaseDSNPreservesConnectionParameters`, que no necesita PG real).
Esto simula el escenario "el fallo intencional no ocurrió" que el script
debe detectar y rechazar:

```
$ .\scripts\test-system.ps1 -PostgresDsn $dsn -E2ERun 'TestDatabaseDSNPreservesConnectionParameters'
?    example.com/sqlite-postgres-compat/cmd/compat  [no test files]
ok   example.com/sqlite-postgres-compat/cmd/internal/cliout  (cached)
ok   example.com/sqlite-postgres-compat/compat  (cached)
test-system: expected intentional failure 'TestSystemClaimsExactCoverageForRequiredFeatureFamilies' did not fail (it passed or did not run).
Either exact coverage improved for a previously-unknown feature family (update docs/COMPATIBILITY.md and this script's baseline), or the -run filter excluded it, or something else changed. Investigate before treating this as green.
EXITCODE=1
```

`exit 1`, mensaje explícito distinguiendo "no corrió" de "falló". Esto cubre
también el otro caso negativo pedido (cualquier otro fallo real dispara la
misma rama de `$unexpectedFailures`, verificada por lectura de código: un
`Where-Object { $_.Value -eq 'fail' } | Where-Object { $_ -ne
$IntentionalFailTest }` no vacío basta para `exit 1`).

## 5. Trade-offs

- **`-json` en vez de `-v` + grep de texto**: más robusto (formato
  estructurado, no depende de que el texto de `go test -v` no cambie entre
  versiones de Go), pero pierde el output humano en vivo línea por línea; en
  fallo se vuelca el output crudo igual, así que no se pierde información de
  diagnóstico, solo el streaming en tiempo real.
- **Filtro por nombre exacto del test intencional** (constante en el script)
  en vez de, por ejemplo, contar "exactamente 1 fallo top-level": es más
  explícito y a prueba de que aparezca *otro* fallo nuevo que casualmente
  deje el conteo en 1 (ese caso sí se detecta como fallo inesperado). Costo:
  si se renombra el test hay que tocar el script a mano (documentado en el
  comentario del script apuntando a `docs/reports/AUDIT7-C.md` H2).
- **`-E2ERun` como parámetro del script**: agrega superficie no usada en el
  camino feliz, pero es la única forma de demostrar el caso negativo sin
  tocar el repo (no se puede editar el test intencional ni el `.go` de la
  suite). Es opt-in (default `""`) y no cambia el comportamiento normal.
- **No se filtran subtests** en el conteo de fallos "extra": si en el futuro
  un test top-level distinto de la baseline tuviera subtests fallados, el
  test top-level ya cuenta como `fail` vía el evento `Action` de Go, así que
  el filtro por `Test.Contains('/')` solo evita contar los 7 subtests de la
  familia genérica como 7 fallos separados — el top-level ya los resume en 1.
