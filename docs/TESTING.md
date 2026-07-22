# Pruebas y criterios de aceptación

## Pruebas unitarias y análisis estático

```powershell
go test ./... -timeout 60s
go vet ./...
```

Estas pruebas deben terminar correctamente. Cubren AST, compilación DDL, parsers de catálogo, snapshots, journals, conflictos y runtime común.

## Pruebas E2E

La suite E2E necesita una conexión PostgreSQL administrativa capaz de crear y eliminar bases temporales:

```powershell
$env:COMPAT_POSTGRES_DSN = "postgres://usuario@127.0.0.1:5432/postgres?sslmode=disable"
go test -tags=e2e ./e2e -v -count=1 -timeout 60s
```

El script integrado usa por defecto el usuario actual de Windows:

```powershell
.\scripts\test-system.ps1
```

También acepta un DSN explícito:

```powershell
.\scripts\test-system.ps1 -PostgresDsn "postgres://usuario:clave@localhost:5432/postgres?sslmode=disable"
```

## Aislamiento

`TestMain` crea una base `compat_e2e_<timestamp>`, ejecuta la suite, termina conexiones abiertas y elimina la base. Las pruebas no deben usar una base de aplicación real.

## Por qué la batería integral termina con código 1

Hay dos niveles distintos:

1. Las pruebas funcionales superiores comprueban capacidades ya implementadas y deben pasar.
2. `TestSystemClaimsExactCoverageForRequiredFeatureFamilies` exige compatibilidad exacta de todas las familias genéricas del objetivo final.

La segunda prueba falla intencionalmente mientras haya capacidades `unknown`. El código distinto de cero evita presentar una suite parcialmente exitosa como prueba de compatibilidad total.

El conteo vigente y las familias restantes se registran en [VALIDATION_REPORT.md](../VALIDATION_REPORT.md).

## Qué valida extremo a extremo

- ida y vuelta SQLite → PostgreSQL → SQLite;
- CLI real `compat-copy`;
- precisión decimal, JSON, UUID, timestamp y binarios;
- claves y acciones referenciales;
- `CHECK` e índices;
- vistas, triggers y rutinas canónicas y externas traducibles;
- inspección sin metadatos propios;
- captura automática y replicación en ambas direcciones;
- idempotencia, conflictos y supresión de ecos;
- limpieza de la base PostgreSQL temporal.

## Interpretación de fallos

- Un fallo funcional es una regresión y debe corregirse.
- Un subtest `unknown` de cobertura global es una capacidad aún no implementada.
- Una base temporal restante después de la suite es un fallo de limpieza.
- `Inspection.Exact == false` no es un error del inspector: significa que detectó objetos cuya semántica no puede garantizar.
