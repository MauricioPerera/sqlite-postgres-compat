# Guía de uso y API

## Auditar un contrato

```powershell
go run ./cmd/compat-audit .\examples\contract.example.json
```

La salida es JSON. El proceso termina con código `1` si cualquier capacidad requerida no es exacta, y con código de salida 2 si el número de argumentos no es exactamente uno.

```json
{
  "source": {"engine": "sqlite", "version": {"major": 3, "minor": 45, "patch": 0}},
  "destination": {"engine": "postgres", "version": {"major": 17, "minor": 0, "patch": 0}},
  "required_features": ["tables", "canonical_foreign_keys", "transactions"]
}
```

Usa capacidades `canonical_*` cuando el objeto procede del AST del proyecto. No cambies una familia genérica `unknown` por su equivalente canónico sin traducir primero el objeto.

## Copiar un snapshot con la CLI

Configura `examples/migration.example.json`:

```json
{
  "source_dsn": "source.db",
  "destination_dsn": "postgres://user:password@localhost:5432/database?sslmode=disable",
  "contract": {
    "source": {"engine": "sqlite", "version": {"major": 3, "minor": 45, "patch": 0}},
    "destination": {"engine": "postgres", "version": {"major": 17, "minor": 0, "patch": 0}}
  },
  "schema": {
    "tables": [{
      "name": "entries",
      "columns": [
        {"name": "id", "type": {"family": "integer"}, "nullable": false},
        {"name": "title", "type": {"family": "text"}, "nullable": false}
      ],
      "constraints": [{"kind": "primary_key", "columns": ["id"]}]
    }]
  }
}
```

Ejecuta:

```powershell
go run ./cmd/compat-copy .\examples\migration.example.json
```

El flujo audita las capacidades inferidas, exporta el origen, importa el destino y vuelve a exportarlo para verificar su hash canónico. El destino debe estar vacío para los objetos descritos. El proceso termina con código `1` ante cualquier error o falta de equivalencia exacta, y con código de salida 2 si el número de argumentos no es exactamente uno.

## Usar el paquete Go

### Crear y aplicar un esquema

```go
ctx := context.Background()

schema := compat.Schema{Tables: []compat.Table{{
    Name: "entries",
    Columns: []compat.Column{
        {Name: "id", Type: compat.Type{Family: compat.IntegerType}},
        {Name: "title", Type: compat.Type{Family: compat.TextType}},
    },
    Constraints: []compat.Constraint{{
        Kind: compat.PrimaryKey, Columns: []string{"id"},
    }},
}}}

sqlite, err := compat.OpenSQLite(compat.Version{Major: 3}, "app.db")
if err != nil { panic(err) }
defer sqlite.Close()

if err := sqlite.ApplySchema(ctx, schema); err != nil { panic(err) }
```

### Migrar y verificar un snapshot

```go
snapshot, err := source.ExportSnapshot(ctx, schema)
if err != nil { panic(err) }

if err := destination.ImportSnapshot(ctx, snapshot); err != nil { panic(err) }

copied, err := destination.ExportSnapshot(ctx, schema)
if err != nil { panic(err) }

report, err := compat.VerifySnapshots(snapshot, copied)
if err != nil { panic(err) }
if err := compat.RequireEquivalent(report); err != nil { panic(err) }
```

### Inspeccionar una base externa

```go
inspection, err := source.InspectSchema(ctx)
if err != nil { panic(err) }
if !inspection.Exact {
    for _, object := range inspection.Unresolved {
        log.Printf("%s %s: %s", object.Kind, object.Name, object.Reason)
    }
    return
}

schema := inspection.Schema
```

No migres automáticamente un catálogo externo cuando `Exact` sea `false`: el esquema resultante es parcial y `Unresolved` describe lo que falta.

## Replicación incremental automática

Instala la captura en ambos extremos después de aplicar el mismo esquema:

```go
if err := sqlite.InstallChangeCapture(ctx, schema); err != nil { panic(err) }
if err := postgres.InstallChangeCapture(ctx, schema); err != nil { panic(err) }
```

Lee y aplica un lote SQLite → PostgreSQL:

```go
changes, err := sqlite.ReadCapturedChanges(ctx, schema, sqliteCursor, 500)
if err != nil { panic(err) }

if err := postgres.ApplyChanges(ctx, schema, changes); err != nil {
    var conflict *compat.ConflictError
    if errors.As(err, &conflict) {
        log.Printf("conflicto en %s: %v", conflict.Table, conflict.PrimaryKey)
    }
    panic(err)
}

if len(changes) > 0 {
    sqliteCursor = changes[len(changes)-1].Sequence
    // Persistir el cursor sólo después de ApplyChanges exitoso.
}
```

Repite el flujo en dirección contraria con un cursor independiente. Nunca compartas cursores entre orígenes.

`ApplyChanges` es idempotente por motor, versión y secuencia de origen. Los cambios replicados no se vuelven a registrar en el journal del destino: la supresión anti-eco es transaccional, y en Postgres se implementa con el GUC local `compat.suppress` (`set_config('compat.suppress','1',true)`), invisible a otras transacciones bajo MVCC y reiniciado al hacer COMMIT/ROLLBACK. `ConflictError` expone `Table`, `PrimaryKey`, `Expected` y `Actual` para diagnosticar exactamente qué valores divergieron. `Version{0,0,0}` es inválida y se rechaza: no es segura como clave de deduplicación entre orígenes.

### Catch-up tolerante para cutover sin ventana

`ApplyChangesTolerant` aplica el mismo stream ordenado que `ApplyChanges`, pero resuelve conflictos de solapamiento: un cambio journaled después de instalar la captura puede haber viajado ya dentro del snapshot, de modo que reaplicarlo produciría un `ConflictError` espurio. Si el estado ACTUAL del destino ya coincide con el estado FINAL del cambio (insert: la fila existe y `rowsEqual(after, actual)`; update: `rowsEqual(after, actual)` aunque `Before` no matchee; delete: la fila ya no existe), el cambio se marca como aplicado y se continúa. Cualquier otra divergencia sigue siendo `ConflictError` estricto: el modo tolerante es una conveniencia de catch-up, no un bypass.

```go
if err := postgres.ApplyChangesTolerant(ctx, schema, changes); err != nil {
    var conflict *compat.ConflictError
    if errors.As(err, &conflict) {
        log.Printf("conflicto real en %s: %v", conflict.Table, conflict.PrimaryKey)
    }
    panic(err)
}
```

`ApplyChanges` queda intacto; usá `ApplyChangesTolerant` sólo durante el catch-up de un cutover.

## Cutover sin ventana con la CLI

`compat-cutover` orquesta un cutover SQLite → PostgreSQL sin ventana de corte: audita el contrato, instala captura en el origen, importa el snapshot en el destino, drena el journal con `ApplyChangesTolerant` (resolviendo el solapamiento inherente) y verifica equivalencia. Configura `examples/cutover.example.json`:

```json
{
  "source_dsn": "source.db",
  "destination_dsn": "postgres://user:password@localhost:5432/database?sslmode=disable",
  "contract": {
    "source": {"engine": "sqlite", "version": {"major": 3, "minor": 45, "patch": 0}},
    "destination": {"engine": "postgres", "version": {"major": 17, "minor": 0, "patch": 0}}
  },
  "schema": {
    "tables": [{
      "name": "entries",
      "columns": [
        {"name": "id", "type": {"family": "integer"}, "nullable": false},
        {"name": "title", "type": {"family": "text"}, "nullable": false}
      ],
      "constraints": [{"kind": "primary_key", "columns": ["id"]}]
    }]
  },
  "options": {"poll_interval_ms": 1000, "drain_polls": 3, "batch_limit": 500}
}
```

`options` es opcional; los defaults son `poll_interval_ms=1000`, `drain_polls=3`, `batch_limit=500`. Ejecuta:

```powershell
go run ./cmd/compat-cutover .\cutover.json
```

El flujo: audita las capacidades inferidas (detiene con código `1` si alguna no es exacta), instala captura en el origen, exporta el snapshot y lo importa en el destino, drena el journal leyendo lotes y aplicándolos con `ApplyChangesTolerant` hasta `drain_polls` lecturas vacías consecutivas, y verifica los digests. Si son equivalentes imprime `{"status":"ready","source_digest":...,"destination_digest":...,"changes_applied":N}` y termina con código `0`; si divergen imprime `{"status":"diverged",...}` y termina con código `1`. Código de salida `2` si el número de argumentos no es exactamente uno. El corte del DSN de la aplicación NO es responsabilidad de esta herramienta: cortá la conexión de la app manualmente tras recibir `status=ready`.

## Ejecutar una rutina traducida

```go
err := store.CallRoutine(ctx, schema, "create_entry", map[string]compat.Value{
    "id":    {Kind: compat.IntegerValue, Value: "1"},
    "title": {Kind: compat.TextValue, Value: "ejemplo"},
})
```

Las rutinas canónicas se ejecutan en una transacción. Las acciones admitidas son `INSERT`, `UPDATE` y `DELETE`: `UPDATE` exige asignaciones y un `WHERE`; `DELETE` exige un `WHERE` y no admite asignaciones. El `WHERE` de una rutina se restringe a comparaciones columna↔parámetro/literal (`=`, `<>`, `<`, `<=`, `>`, `>=`, `LIKE`) compuestas con `AND`/`OR`/`NOT`, más `IS NULL`/`IS NOT NULL`; los cualificadores `tabla.columna` y cualquier construcción fuera de esa gramática se rechazan con error explícito. `LIKE` se compila a `ILIKE` en PostgreSQL para preservar la semántica de SQLite. Las rutinas externas traducibles son procedimientos SQL/PLpgSQL parametrizados con esas acciones; las funciones con retorno, modos avanzados o control de flujo quedan sin resolver.

## Búsqueda textual común

```go
results, err := store.SearchText(ctx, "entries", "id", []string{"title", "body"}, "árbol go")
```

La búsqueda usa tokenización Unicode y coincidencia determinista en Go. No intenta reproducir ranking, stemming o diccionarios específicos de FTS5 o PostgreSQL.
