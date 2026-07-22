# QUAL-C5 — Actualización de dependencias directas y alineación de submódulo

**Fecha:** 2026-07-22
**Módulo raíz:** `example.com/sqlite-postgres-compat` (Go 1.26.4, windows/amd64)
**Submódulo:** `experiments/vector` (`example.com/vector-exp`, replace `=> ../..`)
**Alcance:** solo `go.mod` / `go.sum` (raíz y `experiments/vector`). Ningún `.go` modificado.

## 1. Resumen ejecutivo

| Dep | Antes (raíz) | Después (raíz) | Después (vector) | Resultado |
|---|---|---|---|---|
| `github.com/jackc/pgx/v5` | v5.7.6 | **v5.10.0** | **v5.10.0** | OK, sin breaking |
| `modernc.org/sqlite` | v1.39.1 | **v1.54.0** | **v1.54.0** | OK, sin breaking |

Ambas subidas a las versiones objetivo se mantuvieron **verdes** en la suite del raíz.
**No hubo retroceso de versión**: ningún breaking change rompió la compilación o los tests.

## 2. Módulo raíz — trabajo realizado

Comandos:
```
go get github.com/jackc/pgx/v5@v5.10.0 modernc.org/sqlite@v1.54.0
go mod tidy
```

Indirectas arrastradas automáticamente por `go get`/`tidy` (no agregadas a mano):
- `github.com/ncruces/go-strftime` v0.1.9 → v1.0.0
- `github.com/stretchr/testify` v1.10.0 → v1.11.1
- `golang.org/x/sync` v0.16.0 → v0.21.0
- `golang.org/x/sys` v0.36.0 → v0.46.0
- `golang.org/x/text` v0.24.0 → v0.29.0
- `modernc.org/libc` v1.66.10 → v1.74.1

(nuevo indirecto descargado: `modernc.org/gc/v3 v3.1.4`)

### Verificación raíz (salida real)

```
$ go build ./...
$ go vet ./...
$ go test ./... -count=1
?       example.com/sqlite-postgres-compat/cmd/compat-audit  [no test files]
?       example.com/sqlite-postgres-compat/cmd/compat-copy   [no test files]
?       example.com/sqlite-postgres-compat/cmd/compat-cutover [no test files]
ok      example.com/sqlite-postgres-compat/cmd/internal/cliout      2.162s
ok      example.com/sqlite-postgres-compat/compat                    1.921s
```

`build` y `vet` sin salida (éxito). `test`: 2 paquetes con tests en verde, 3 sin tests. Sin FALL.

## 3. Submódulo `experiments/vector` — trabajo realizado

El submódulo **ya tenía** pgx v5.10.0 antes de empezar; faltaba alinear sqlite y las transitorias al raíz.

Comandos:
```
cd experiments/vector
go mod tidy
```

Tras el `tidy`, `go.mod` quedó alineado al raíz:
- `github.com/jackc/pgx/v5` **v5.10.0** (directo, ya estaba)
- `modernc.org/sqlite` **v1.54.0** (indirecto, arrastrado por tidy — coincide con raíz)
- Transitorias alineadas: `go-strftime` v1.0.0, `sync` v0.21.0, `sys` v0.46.0, `text` v0.29.0, `libc` v1.74.1.

### Verificación vector (salida real)

```
$ go build ./...
BUILD_OK
$ go vet ./...
VET_OK
```

### Tests de vector — todos requieren red/DB externos

Los 11 tests del submódulo abren conexiones externas en su setup:
- **libSQL** vía HTTP a `http://localhost:8081/v2/pipeline`
- **Postgres** a `localhost:5434` (`user=postgres database=postgres`)

Funciones de test (todas fallan por conexión rechazada, no por el cambio de deps):
```
TestPostVectorTypeApplySchema
TestPostVectorTypeSnapshotAndVerify
TestPostVectorTypeIncrementalReplication
TestPostVectorTypeInspectPG
TestPostVectorTypeInspectPGNonVector
TestSanityDirectLibSQL
TestSanityDirectPgvector
TestInspectF32BlobMapsToVectorFamily
TestSnapshotBinaryRoute
TestTextReplicationRoute
TestCatalogParserRejectsVectorDistanceCos
```

Ejemplo de error (todos del mismo tipo — `dial tcp [::1]:8081: connectex: ... denegó expresamente dicha conexión` / `dial tcp [::1]:5434`):
```
--- FAIL: TestSnapshotBinaryRoute (0.00s)
    vector_test.go:301: exec "DROP TABLE IF EXISTS vexp_bin": failed to execute SQL:
        Post "http://localhost:8081/v2/pipeline": dial tcp [::1]:8081: connectex: No se puede establecer una conexión ya que el equipo de destino denegó expresamente dicha conexión.
    panic.go:694: pg cleanup query: failed to connect to `user=postgres database=postgres`:
        [::1]:5434 (localhost): dial error: dial tcp [::1]:5434: connectex: ...
FAIL    example.com/vector-exp    3.087s
FAIL
```

**No existe ningún test unitario puro (sin red/DB) en `experiments/vector`.** Conforme al objetivo, se verificó `go build ./...` y `go vet ./...` (ambos verdes) y se documenta que la suite completa requiere servicios externos no disponibles en este entorno. Los fallos son de infraestructura, no de la actualización.

## 4. Versiones finales (`go list -m`)

```
=== RAIZ (example.com/sqlite-postgres-compat) ===
github.com/jackc/pgx/v5 v5.10.0
modernc.org/sqlite v1.54.0

=== VECTOR (example.com/vector-exp) ===
github.com/jackc/pgx/v5 v5.10.0
modernc.org/sqlite v1.54.0
```

## 5. Notas

- No se subieron indirectas a mano; todo lo de la sección 2 lo arrastró `go mod tidy`.
- Ningún `.go` fue modificado (cumple la restricción de no tocar `compat/*.go` ni ningún otro código).
- No se detectó drift residual entre raíz y `experiments/vector` para pgx/sqlite.
- No se hicieron `git add`/`commit`/`stash`.