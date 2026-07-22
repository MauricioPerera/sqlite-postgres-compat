# sqlite-postgres-compat

Capa de compatibilidad para Go con contratos de esquema canónico, migración por snapshot, replicación incremental bidireccional y cutover en vivo de SQLite/libSQL hacia PostgreSQL.

## ¿Por qué?

El caso de uso: arrancar un SaaS con SQLite o libSQL (cero operación, sin servidor de base de datos que administrar) y migrar a PostgreSQL más adelante, cuando el crecimiento lo exija, sin ventana de corte y sin apagar la aplicación. Cada fase del camino —auditoría de capacidades, snapshot, replicación, cutover— produce un veredicto determinista (exit code y JSON) en vez de una promesa optimista: si una capacidad no se puede garantizar en ambos motores, el proceso se detiene y lo dice, no degrada en silencio.

## Características

- **Contrato de esquema canónico auditable**: declara motor, versión y capacidades requeridas; `Audit` devuelve un veredicto por capacidad (`exact`/`unknown`) y `RequireExact` corta la migración ante cualquier capacidad no garantizada.
- **Snapshot con verificación por digest**: exporta el esquema y los datos a una representación canónica, importa en el destino y compara hashes canónicos de ambos lados antes de dar por buena la copia.
- **Replicación incremental por triggers**: captura automática de cambios (`INSERT`/`UPDATE`/`DELETE`) vía triggers internos, aplicación idempotente por secuencia, detección de conflictos con `Expected`/`Actual`, y supresión de ecos transaccional (GUC local `compat.suppress` en Postgres, no filtra a transacciones ajenas bajo MVCC).
- **Cutover orquestado sin ventana**: `compat-cutover` audita, instala captura, hace snapshot, drena el journal con `ApplyChangesTolerant` (tolera el solapamiento captura/snapshot sin ser un bypass de conflictos reales) y verifica equivalencia; incluye un modo `--dry-run` de sólo lectura que imprime el plan sin escribir nada.
- **Tipo `vector(N)` de primera clase**: SQLite/libSQL (`F32_BLOB(N)`) ↔ PostgreSQL (`pgvector`), validado end-to-end contra motores reales (libSQL/sqld y pgvector), incluida inspección de dimensión, snapshot y replicación incremental hacia una columna `vector` nativa.
- **CLIs con salidas JSON y códigos de error tipados**, pensadas para ser consumidas por agentes de IA: cada fallo emite un envelope `{"status":"error","code":"<CODE>","message":"..."}` sobre una taxonomía cerrada de códigos.
- **Gramática canónica documentada para agentes** (`AGENTS.md`): especifica exactamente qué SQL/esquema es traducible, qué se rechaza y con qué error.

> Estado: el núcleo canónico funciona y está probado en ambos motores, pero el proyecto todavía no ofrece compatibilidad total con cualquier SQL arbitrario de SQLite y PostgreSQL. La prueba global permanece roja hasta que esa afirmación sea verdadera.

## Inicio rápido

```powershell
go test ./...
go vet ./...
go run ./cmd/compat-audit .\examples\contract.example.json
```

Para copiar un snapshot, edita `examples/migration.example.json` con DSN reales y ejecuta:

```powershell
go run ./cmd/compat-copy .\examples\migration.example.json
```

Para un cutover sin ventana (edita `examples/cutover.example.json` con DSN reales primero):

```powershell
go run ./cmd/compat-cutover --dry-run .\examples\cutover.example.json
```

## Los CLIs

| Comando | Qué hace | Salida / exit codes |
|---|---|---|
| `compat-audit <contract.json>` | Audita un contrato (`{source, destination, required_features}`) y evalúa cada capacidad requerida. | Array JSON de `Finding` en stdout. Exit `0` si todo es `exact`; `1` si alguna capacidad no lo es (`ERR_AUDIT_NOT_EXACT`); `2` si el número de argumentos no es uno (`ERR_USAGE`). |
| `compat-copy <migration.json>` | Migración por snapshot: audita, exporta el origen, importa en el destino (que debe estar vacío para esos objetos) y verifica digests. | `VerificationReport` JSON en stdout. Exit `0` si `equivalent=true`; `1` ante cualquier error tipado o divergencia (`ERR_VERIFY_DIVERGED`); `2` si el número de argumentos no es uno. |
| `compat-cutover [--dry-run] <cutover.json>` | Cutover sin ventana: audita, instala captura en el origen, hace snapshot al destino, drena el journal con `ApplyChangesTolerant` y verifica. `--dry-run` sólo audita, cuenta filas y prueba conectividad, sin escribir nada. | `cutoverReport` JSON (`status=ready`/`diverged`) o plan JSON con `--dry-run`. Exit `0` si `status=ready` (o plan); `1` ante error tipado o `status=diverged` (`ERR_VERIFY_DIVERGED`); `2` si el número de argumentos no es uno. El corte del DSN de la aplicación es manual, tras recibir `status=ready`. |

Los tres CLIs comparten una taxonomía cerrada de códigos de error (`ERR_USAGE`, `ERR_CONFIG`, `ERR_AUDIT_NOT_EXACT`, `ERR_CONNECT_SOURCE`, `ERR_CONNECT_DESTINATION`, `ERR_SCHEMA`, `ERR_SNAPSHOT`, `ERR_REPLICATION_CONFLICT`, `ERR_CAPTURE`, `ERR_VERIFY_DIVERGED`, `ERR_INTERNAL`); ver el detalle en [docs/USAGE.md](docs/USAGE.md).

## Uso con agentes de IA

`AGENTS.md` es la especificación machine-facing de la gramática canónica: qué tipos, constraints, expresiones, vistas, triggers y rutinas son traducibles, y con qué error explícito se rechaza todo lo demás. `contracts/migration.contract.example.md` es un contrato de migración ejecutable de ejemplo (frontmatter YAML + veredictos verificables comando por comando). Los tres CLIs emiten únicamente JSON parseable, incluidos los errores tipados descritos arriba, para que un agente pueda ramificar por código sin parsear texto libre.

## Estado de validación

La batería E2E (`e2e/system_test.go`, `e2e/suppress_test.go`, `e2e/cutover_test.go`) corre contra SQLite real y PostgreSQL 17.10 real: 28 pruebas de nivel superior, 27 superadas y 1 fallida de forma intencional (`TestSystemClaimsExactCoverageForRequiredFeatureFamilies`), que documenta que las familias genéricas no-canónicas (`foreign_keys`, `check_constraints`, `indexes`, `views`, `triggers`, `stored_routines`, `full_text`) permanecen `unknown` porque representan SQL arbitrario del dialecto, no cubierto todavía. Detalle completo en [docs/reports/VALIDATION_REPORT.md](docs/reports/VALIDATION_REPORT.md).

La compatibilidad del tipo `vector` fue validada por separado contra libSQL/sqld y pgvector reales (snapshot, replicación incremental e inspección de dimensión hacia una columna `vector(N)` nativa). Detalle en [docs/reports/VECTOR-COMPAT-REPORT.md](docs/reports/VECTOR-COMPAT-REPORT.md).

## Estructura del repo

```
compat/           # núcleo: esquema canónico, parser SQL, DDL, snapshots, journal, replicación, runtime
cmd/               # CLIs: compat-audit, compat-copy, compat-cutover
e2e/               # batería end-to-end contra SQLite y PostgreSQL reales
experiments/vector/# validación del tipo vector contra libSQL/sqld y pgvector reales
examples/          # contratos y configuraciones de ejemplo para los CLIs
contracts/         # contrato de migración de ejemplo para agentes/CI
docs/              # arquitectura, uso, compatibilidad, operaciones, pruebas y reportes
scripts/           # script de validación integral (test-system.ps1)
AGENTS.md          # gramática canónica machine-facing para agentes/LLMs
```

## Documentación

- [Arquitectura](docs/ARCHITECTURE.md)
- [Guía de uso y API](docs/USAGE.md)
- [Matriz de compatibilidad](docs/COMPATIBILITY.md)
- [Operación, concurrencia y recuperación](docs/OPERATIONS.md)
- [Pruebas y criterios de aceptación](docs/TESTING.md)
- [Informe de la última validación](docs/reports/VALIDATION_REPORT.md)
- [Compatibilidad vectorial (libSQL/sqld ↔ pgvector)](docs/reports/VECTOR-COMPAT-REPORT.md)
- [Especificación para agentes/LLMs](AGENTS.md): gramática canónica, CLIs y flujo de migración.
