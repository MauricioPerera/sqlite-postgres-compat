# sqlite-postgres-compat

Motor experimental de compatibilidad bidireccional SQLite ↔ PostgreSQL escrito en Go.

El proyecto usa un esquema canónico independiente de ambos dialectos. Su contrato declara las versiones de origen y destino y las capacidades que deben conservarse. Una capacidad desconocida o no traducida detiene la operación en vez de degradarse silenciosamente.

Las capas implementadas son el contrato de compatibilidad, la representación canónica de esquemas, persistencia e inspección exacta del contrato, traducción de catálogos externos para claves primarias, restricciones `UNIQUE`, claves foráneas con acciones referenciales, valores por defecto, expresiones `CHECK`, índices, vistas `SELECT` con joins y agregaciones, triggers con acciones `INSERT`/`UPDATE`/`DELETE` y procedimientos de inserción parametrizados dentro de la gramática SQL común, auditoría de capacidades, compiladores DDL SQLite/PostgreSQL, adaptadores para snapshots, captura automática de cambios, replicación incremental transaccional e idempotente con detección de conflictos y supresión de ecos, y un runtime común para vistas, triggers, rutinas y búsqueda textual canónicos. La siguiente capa es completar el análisis de dialectos externos.

> Estado: el núcleo canónico funciona y está probado en ambos motores, pero el proyecto todavía no ofrece compatibilidad total con cualquier SQL arbitrario de SQLite y PostgreSQL. La prueba global permanece roja hasta que esa afirmación sea verdadera.

## Requisitos

- Go 1.26 o la versión indicada por `go.mod`.
- PostgreSQL accesible para las pruebas E2E. SQLite se ejecuta mediante `modernc.org/sqlite` y no requiere CGO.
- Una base de destino vacía para `compat-copy`; la CLI no elimina ni reemplaza objetos existentes.

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

## Auditoría

```powershell
go run ./cmd/compat-audit .\contract.json
```

## Migración de snapshot

```powershell
go run ./cmd/compat-copy .\migration.json
```

`compat-copy` exporta el origen al formato canónico y lo importa en el destino. Antes de modificar el destino, audita el esquema inferido y detiene la operación si alguna capacidad no está marcada como equivalente exacta. Después exporta el destino y compara hashes canónicos de ambos snapshots.

## Cutover sin ventana

```powershell
go run ./cmd/compat-cutover .\cutover.json
```

`compat-cutover` orquesta un cutover SQLite → PostgreSQL sin ventana de corte: audita el contrato, instala captura en el origen, importa el snapshot en el destino, drena el journal con `ApplyChangesTolerant` (resolviendo el solapamiento inherente entre captura y snapshot) y verifica equivalencia. Termina con código `0` e imprime `{"status":"ready",...}` cuando los digests coinciden, con código `1` si divergen o alguna capacidad requerida no es exacta, y con código `2` si el número de argumentos no es uno. El corte del DSN de la aplicación es manual: córtalo tras recibir `status=ready`.

## Validación integral

La batería integral usa instancias reales de SQLite y PostgreSQL. Crea una base PostgreSQL temporal, ejecuta migraciones SQLite → PostgreSQL → SQLite, valida datos y comportamiento y elimina la base al terminar.

```powershell
.\scripts\test-system.ps1
```

La suite comprueba el núcleo portable, CLI completa, precisión decimal, JSON, UUID, timestamps, claves foráneas, restricciones `CHECK`, índices únicos/parciales/descendentes, inspección de objetos creados con SQL nativo sin metadatos, vistas, triggers, rutinas, búsqueda textual y la cobertura declarada de familias arbitrarias avanzadas. Una prueba fallida representa una capacidad del objetivo total que el sistema todavía no cumple.

## Documentación

- [Arquitectura](docs/ARCHITECTURE.md)
- [Guía de uso y API](docs/USAGE.md)
- [Matriz de compatibilidad](docs/COMPATIBILITY.md)
- [Operación, concurrencia y recuperación](docs/OPERATIONS.md)
- [Pruebas y criterios de aceptación](docs/TESTING.md)
- [Informe de la última validación](docs/reports/VALIDATION_REPORT.md)

Ejemplo de `contract.json`:

```json
{
  "source": {"engine": "sqlite", "version": {"major": 3, "minor": 45, "patch": 0}},
  "destination": {"engine": "postgres", "version": {"major": 17, "minor": 0, "patch": 0}},
  "required_features": ["tables", "canonical_foreign_keys", "transactions"]
}
```
