# Guía de uso y API

## Auditar un contrato

```powershell
go run ./cmd/compat audit .\examples\contract.example.json
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
go run ./cmd/compat copy .\examples\migration.example.json
```

El flujo audita las capacidades inferidas, exporta el origen, importa el destino y vuelve a exportarlo para verificar su hash canónico. El destino debe estar vacío para los objetos descritos. El proceso termina con código `1` ante cualquier error o falta de equivalencia exacta, y con código de salida 2 si el número de argumentos no es exactamente uno o se pasa un flag inesperado. En lugar de inlinear `schema`, podés usar `schema_ref` (ruta a un JSON con el `compat.Schema` canónico, resuelta relativa al archivo de config); debe haber exactamente uno de `schema` o `schema_ref`, si no `ERR_CONFIG` (ver cutover para el detalle). En fallo: en `ERR_AUDIT_NOT_EXACT` imprime el arreglo `[]Finding` a stderr antes del envelope; en `ERR_VERIFY_DIVERGED` (digests distintos) imprime el `VerificationReport` a stderr antes del envelope, así los digests quedan como JSON parseable y no sólo como texto libre en el `message`.

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

### Compatibilidad al actualizar la herramienta (reinstall de la captura)

`InstallChangeCapture` instala triggers cuya rama `DECIMAL`/`typeof='real'` emite un marcador versionado (`realDecimalMarker`) antes del texto `printf('%!.17g')`, para que el applier distinga un decimal REAL-stored de TEXT arbitrario. Los triggers instalados por una versión **anterior** de la herramienta emiten esa rama **sin** el marcador. Esto es una **frontera de versión de la herramienta**, no un formato de journal autodescriptivo; respetala al actualizar la herramienta a través de ese límite:

- **Reinstalar la captura tras actualizar (reinstall).** Los triggers instalados por una versión anterior deben **reinstalarse** (`InstallChangeCapture`) en cada store antes de capturar cambios nuevos con el código nuevo: los triggers viejos no emiten el marcador que el applier nuevo espera.
- **Drenar o descartar journals in-flight pre-actualización.** Un journal capturado por triggers pre-actualización (sin marcador) **no** debe aplicarse con el código nuevo: drenaló al destino antes de actualizar, o descartálo y re-snapshoteá desde un origen limpio. El contrato de migración documentado (instalar captura → snapshot → catch-up que drena el journal) ya te mantiene del lado seguro; un journal in-flight mixto entre versiones está fuera de ese contrato.
- **La divergencia la detecta verify, nunca es silenciosa.** Si aun así se aplica un journal pre-actualización con el código nuevo, `ApplyChanges` no errora, pero `VerifySnapshots` reporta `Equivalent == false` para los decimales REAL-stored de alta magnitud afectados (el código nuevo lee el texto sin marcador verbatim mientras el origen canoniza el `float64` REAL). No hay corrupción silenciosa: un paso de `verify` expone la divergencia.

### Compatibilidad al actualizar la herramienta (familia `date`)

`date` ahora compila a **`TEXT`** en PostgreSQL (antes era `DATE` nativo). Una columna `DATE` nativa la devuelve pgx como `time.Time`, que la capa canónica plegaría a `TimestampValue` (`"2020-01-01T00:00:00Z"`) y divergiría siempre del origen SQLite `TEXT` (`"2020-01-01"`). `TEXT` replica el mapeo protector de `timestamp`/`json`/`uuid` y preserva la fecha byte-for-byte. Es una **frontera de mapeo de schema**; respetala al actualizar la herramienta a través de ese límite:

- **Recrear el schema del destino legado.** Un destino creado por una versión **anterior** de la herramienta todavía tiene columnas `DATE` nativas. Recreá el schema del destino (drop y re-`ApplySchema`, o re-corre la migración completa de `compat copy`/`compat cutover` desde un origen limpio) para que las columnas pasen a `TEXT`. `canonicalValue` es defensivo: cuando la familia es `date` igual canoniza un `time.Time` de una columna `DATE` nativa legada al formato date-only, así una re-verificación aislada contra esa columna converge en vez de diverger — pero el camino soportado es recrear el schema.
- **La divergencia la detecta verify, no es silenciosa.** La primera verificación de `compat copy`/`compat cutover` contra un destino con `DATE` nativo legado reporta `ERR_VERIFY_DIVERGED` / `status=diverged` (exit `1`); no hay corrupción silenciosa. Recreá el schema del destino para limpiarla.

## Cutover sin ventana con la CLI

`compat cutover` orquesta un cutover SQLite → PostgreSQL sin ventana de corte: audita el contrato, instala captura en el origen, importa el snapshot en el destino, drena el journal con `ApplyChangesTolerant` (resolviendo el solapamiento inherente) y verifica equivalencia. Configura `examples/cutover.example.json`:

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
go run ./cmd/compat cutover .\cutover.json
```

En lugar de inlinear el `schema`, podés apuntar `schema_ref` a un archivo JSON que contenga el objeto `compat.Schema` canónico (mismo shape que el campo `schema` inline). La ruta se resuelve **relativa al archivo de config**, no al cwd:

```json
{
  "source_dsn": "source.db",
  "destination_dsn": "postgres://user:password@localhost:5432/database?sslmode=disable",
  "contract": { "...": "..." },
  "schema_ref": "schema.json",
  "options": {"poll_interval_ms": 1000, "drain_polls": 3, "batch_limit": 500}
}
```

Debe haber **exactamente uno** de `schema` o `schema_ref`: ambos o ninguno es `ERR_CONFIG`. Un archivo `schema_ref` ilegible o con JSON inválido también es `ERR_CONFIG`. `compat copy` soporta `schema_ref` igual que `compat cutover`.

El flujo: audita las capacidades inferidas (detiene con código `1` si alguna no es exacta), instala captura en el origen, exporta el snapshot y lo importa en el destino, drena el journal leyendo lotes y aplicándolos con `ApplyChangesTolerant` hasta `drain_polls` lecturas vacías consecutivas, y verifica los digests. Si son equivalentes imprime `{"status":"ready","source_digest":...,"destination_digest":...,"changes_applied":N}` y termina con código `0`; si divergen imprime `{"status":"diverged","code":"ERR_VERIFY_DIVERGED",...}` y termina con código `1`. Código de salida `2` si el número de argumentos no es exactamente uno o se pasa un flag inesperado. El corte del DSN de la aplicación NO es responsabilidad de esta herramienta: cortá la conexión de la app manualmente tras recibir `status=ready`.

### Plan de sólo lectura con `--dry-run`

```powershell
go run ./cmd/compat cutover --dry-run .\cutover.json
```

`--dry-run` (opcional, antes del JSON posicional) ejecuta sólo fases de lectura: parsea la config, audita el contrato, conecta y hace ping a ambos stores, cuenta las filas del origen por cada tabla del esquema del contrato y verifica si el destino ya contiene esas tablas. Imprime un plan JSON a stdout y termina con código `0`:

```json
{"status":"plan","audit":[{"feature":"tables","status":"exact"}],
 "source_tables":[{"name":"entries","rows":N}],
 "destination_has_tables":false,
 "phases":["install_capture","snapshot","catch_up","verify"]}
```

- `audit`: el arreglo de `Finding` para las features requeridas + inferidas.
- `source_tables`: un `{name, rows}` por tabla del esquema, con conteo vía `SELECT count(*)`.
- `destination_has_tables`: `true` sólo si todas las tablas del contrato ya existen en el destino (un cutover real chocaría con ellas al importar).
- `phases`: la secuencia fija que ejecutaría un cutover real después del plan.

`--dry-run` **no escribe nada** en origen ni destino: no instala captura, no crea tablas, no importa snapshot, no escribe journal. Si la auditoría no es exacta o una conexión falla, emite el JSON de error tipado correspondiente y termina con código `1`.

### Códigos de error tipados (los 3 subcomandos)

Ante cualquier fallo, cada subcomando sale con su código actual (`1`, o `2` para `ERR_USAGE`) y emite una señal JSON legible por máquina. La forma de esa señal depende del camino de fallo, así que un agente debe parsear el contrato por caso en vez de asumir un único layout fijo:

- **Envelope de error simple (por defecto).** La mayoría de los fallos imprime a **stdout** una sola línea JSON, y nada más legible por máquina:

```json
{"status":"error","code":"<CODE>","message":"<detalle>"}
```

- **`compat cutover` diverged.** `ERR_VERIFY_DIVERGED` emite **una sola** línea JSON en stdout — el `cutoverReport` con el `code` tipado embebido (`{"status":"diverged","code":"ERR_VERIFY_DIVERGED",...}`). No hay un envelope `{"status":"error",...}` separado en este camino.
- **`compat copy` not-exact o diverged.** Antes del envelope de error simple en stdout, `compat copy` emite a **stderr** un payload JSON estructurado: el arreglo `[]Finding` en `ERR_AUDIT_NOT_EXACT`, o el objeto `VerificationReport` en `ERR_VERIFY_DIVERGED`. El envelope plano igual sigue en stdout con el mismo `code`; un agente lee el detalle estructurado de stderr y el código tipado de stdout.

Cada línea de envelope es un objeto JSON parseable (el mensaje va JSON-encodeado, así que los newlines embebidos nunca la rompen). Ramificá por `code`. La taxonomía es cerrada; el CLI elige el código más específico aplicable. El detalle libre sigue yendo a stderr para humanos.

| Código | Cuándo se emite | Exit |
|---|---|---|
| `ERR_USAGE` | Cantidad de argumentos incorrecta (o flag inesperado: cualquier argumento que empiece con `-` y no sea un flag reconocido, p. ej. `--bogus`). | `2` |
| `ERR_CONFIG` | La config no se puede leer, falla el decode, o `Audit` rechaza el contrato. Toda config se decodifica con `json.Decoder.DisallowUnknownFields`, así que una key desconocida es un error explícito (no se dropea en silencio); una violación de `schema`/`schema_ref` (ambos, ninguno, o un archivo `schema_ref` ilegible/JSON inválido) también es `ERR_CONFIG`. | `1` |
| `ERR_AUDIT_NOT_EXACT` | Una feature requerida (o inferida) no es `exact` (`RequireExact` falla). `compat audit` imprime el arreglo de findings a stdout y después este envelope; `compat copy` y `compat cutover` imprimen el arreglo de findings a stderr y después este envelope. | `1` |
| `ERR_CONNECT_SOURCE` | No se puede abrir o hacer ping al store origen. | `1` |
| `ERR_CONNECT_DESTINATION` | No se puede abrir o hacer ping al store destino. | `1` |
| `ERR_SCHEMA` | Falla `Schema.Validate` o `ApplySchema`. | `1` |
| `ERR_SNAPSHOT` | Falla `ExportSnapshot` o `ImportSnapshot`. | `1` |
| `ERR_REPLICATION_CONFLICT` | Se raises un `ConflictError` al reaplicar el journal en el catch-up. | `1` |
| `ERR_CAPTURE` | Falla `InstallChangeCapture` o `ReadCapturedChanges`. | `1` |
| `ERR_VERIFY_DIVERGED` | Los digests difieren (`Equivalent == false`). `compat cutover` emite una sola línea JSON — su resultado `diverged` con este `code` embebido (sin envelope separado). `compat copy` emite su `VerificationReport` a stderr y después el envelope con este `code`. | `1` |
| `ERR_INTERNAL` | Cualquier fallo no cubierto arriba. | `1` |

La clasificación es por **heurística de fase** (qué paso del flujo falló) más `errors.As` contra el `ConflictError` ya exportado; la API pública de `compat/` no se extiende.

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
