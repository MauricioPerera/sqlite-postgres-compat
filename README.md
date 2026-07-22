# sqlite-postgres-compat

Base de un motor de compatibilidad bidireccional SQLite ↔ PostgreSQL.

El proyecto no usa una aplicación concreta como modelo. Su contrato de entrada declara las versiones de los motores y las capacidades que deben preservarse. El motor no debe degradar una capacidad sin reportarla explícitamente.

Las capas implementadas son el contrato de compatibilidad, la representación canónica de esquemas, la auditoría de capacidades, compiladores DDL SQLite/PostgreSQL, un diario canónico de mutaciones y adaptadores para exportar/importar snapshots. Las siguientes capas serán análisis SQL, representación canónica de consultas, captura de cambios, replicación y pruebas de equivalencia contra ambos motores.

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

La suite comprueba el núcleo portable, precisión decimal, JSON, UUID, timestamps, claves foráneas y la cobertura declarada de familias avanzadas. Una prueba fallida representa una capacidad del objetivo total que el sistema todavía no cumple.

Ejemplo de `contract.json`:

```json
{
  "source": {"engine": "sqlite", "version": {"major": 3, "minor": 45, "patch": 0}},
  "destination": {"engine": "postgres", "version": {"major": 17, "minor": 0, "patch": 0}},
  "required_features": ["tables", "foreign_keys", "transactions"]
}
```
