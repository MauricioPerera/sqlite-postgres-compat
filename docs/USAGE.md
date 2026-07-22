# Guía de uso y API

## Auditar un contrato

```powershell
go run ./cmd/compat-audit .\contract.example.json
```

La salida es JSON. El proceso termina con código `1` si cualquier capacidad requerida no es exacta.

```json
{
  "source": {"engine": "sqlite", "version": {"major": 3, "minor": 45, "patch": 0}},
  "destination": {"engine": "postgres", "version": {"major": 17, "minor": 0, "patch": 0}},
  "required_features": ["tables", "canonical_foreign_keys", "transactions"]
}
```

Usa capacidades `canonical_*` cuando el objeto procede del AST del proyecto. No cambies una familia genérica `unknown` por su equivalente canónico sin traducir primero el objeto.

## Copiar un snapshot con la CLI

Configura `migration.example.json`:

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
go run ./cmd/compat-copy .\migration.example.json
```

El flujo audita las capacidades inferidas, exporta el origen, importa el destino y vuelve a exportarlo para verificar su hash canónico. El destino debe estar vacío para los objetos descritos.

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

`ApplyChanges` es idempotente por motor, versión y secuencia de origen. Los cambios replicados no se vuelven a registrar en el journal del destino.

## Ejecutar una rutina traducida

```go
err := store.CallRoutine(ctx, schema, "create_entry", map[string]compat.Value{
    "id":    {Kind: compat.IntegerValue, Value: "1"},
    "title": {Kind: compat.TextValue, Value: "ejemplo"},
})
```

Las rutinas canónicas se ejecutan en una transacción. Actualmente las rutinas externas traducibles son comandos parametrizados de inserción; funciones con retorno o control de flujo quedan sin resolver.

## Búsqueda textual común

```go
results, err := store.SearchText(ctx, "entries", "id", []string{"title", "body"}, "árbol go")
```

La búsqueda usa tokenización Unicode y coincidencia determinista en Go. No intenta reproducir ranking, stemming o diccionarios específicos de FTS5 o PostgreSQL.
