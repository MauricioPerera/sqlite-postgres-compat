# sqlite-postgres-compat

Base de un motor de compatibilidad bidireccional SQLite ↔ PostgreSQL.

El proyecto no usa una aplicación concreta como modelo. Su contrato de entrada declara las versiones de los motores y las capacidades que deben preservarse. El motor no debe degradar una capacidad sin reportarla explícitamente.

Las capas implementadas son el contrato de compatibilidad, la representación canónica de esquemas, persistencia e inspección exacta del contrato, traducción de catálogos externos para claves primarias, restricciones `UNIQUE`, claves foráneas con acciones referenciales, valores por defecto, expresiones `CHECK`, índices, vistas `SELECT` con joins y agregaciones, triggers de auditoría y procedimientos de inserción parametrizados dentro de la gramática SQL común, auditoría de capacidades, compiladores DDL SQLite/PostgreSQL, adaptadores para snapshots, captura automática de cambios, replicación incremental transaccional e idempotente con detección de conflictos y supresión de ecos, y un runtime común para vistas, triggers, rutinas y búsqueda textual canónicos. La siguiente capa es completar el análisis de dialectos externos.

## Auditoría

```powershell
go run ./cmd/compat-audit .\contract.json
```

## Migración de snapshot

```powershell
go run ./cmd/compat-copy .\migration.json
```

`compat-copy` exporta el origen al formato canónico y lo importa en el destino. Antes de modificar el destino, audita el esquema inferido y detiene la operación si alguna capacidad no está marcada como equivalente exacta. Después exporta el destino y compara hashes canónicos de ambos snapshots.

## Validación integral

La batería integral usa instancias reales de SQLite y PostgreSQL. Crea una base PostgreSQL temporal, ejecuta migraciones SQLite → PostgreSQL → SQLite, valida datos y comportamiento y elimina la base al terminar.

```powershell
.\scripts\test-system.ps1
```

La suite comprueba el núcleo portable, CLI completa, precisión decimal, JSON, UUID, timestamps, claves foráneas, restricciones `CHECK`, índices únicos/parciales/descendentes, inspección de objetos creados con SQL nativo sin metadatos, vistas, triggers, rutinas, búsqueda textual y la cobertura declarada de familias arbitrarias avanzadas. Una prueba fallida representa una capacidad del objetivo total que el sistema todavía no cumple.

Ejemplo de `contract.json`:

```json
{
  "source": {"engine": "sqlite", "version": {"major": 3, "minor": 45, "patch": 0}},
  "destination": {"engine": "postgres", "version": {"major": 17, "minor": 0, "patch": 0}},
  "required_features": ["tables", "canonical_foreign_keys", "transactions"]
}
```
