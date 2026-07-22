# Operación, concurrencia y recuperación

## SQLite en un sitio multiusuario

El adaptador abre SQLite con una sola conexión activa para asegurar que `PRAGMA foreign_keys=ON` se aplique a todas las operaciones. Esto simplifica la consistencia, pero serializa el acceso realizado mediante ese `Store`.

SQLite admite múltiples lectores, pero mantiene un solo escritor a la vez. Para una aplicación web pequeña o mediana puede ser suficiente si:

- las transacciones de escritura son cortas;
- no se mantienen consultas abiertas durante operaciones lentas;
- el archivo está en almacenamiento local, no en una carpeta de red;
- se vigilan errores de bloqueo y latencia;
- el proceso de aplicación es el propietario principal del archivo.

PostgreSQL es el destino apropiado cuando la concurrencia de escritura, varias instancias de aplicación o la administración remota dejan de encajar en esas condiciones.

## DSN

SQLite:

```text
app.db
file:app.db
file:C:/datos/app.db
```

El adaptador añade internamente el pragma de claves foráneas. No incluyas credenciales en archivos versionados.

PostgreSQL:

```text
postgres://usuario:clave@servidor:5432/base?sslmode=require
```

Usa TLS y un usuario con privilegios mínimos en producción. Las pruebas E2E son la excepción: necesitan crear y eliminar una base temporal.

## Despliegue inicial

1. Audita el contrato.
2. Inspecciona el catálogo externo, si existe, y exige `Inspection.Exact == true`.
3. Crea una base destino vacía.
4. Exporta e importa el snapshot.
5. Verifica el snapshot del destino.
6. Instala captura automática sólo después de tener el mismo esquema en ambos extremos.
7. Persiste un cursor independiente para cada origen.
8. Inicia la replicación incremental.

No ejecutes `compat-copy` repetidamente sobre las mismas tablas: la importación no reemplaza ni elimina objetos existentes.

## Cursores y entrega

Un cursor representa la última secuencia confirmada de un origen. Debe actualizarse únicamente después de que `ApplyChanges` termine correctamente.

Si el proceso falla antes de guardar el cursor, el lote se repite. Esto es seguro porque el destino registra las secuencias aplicadas. Si falla después de aplicar y antes de guardar, el mismo mecanismo evita duplicados.

Los cursores SQLite y PostgreSQL son independientes. No se debe usar el máximo de uno como cursor del otro.

## Conflictos

Una actualización o eliminación compara la imagen `before` del origen con la fila actual del destino. Si difieren, `ApplyChanges` aborta toda la transacción y devuelve `ConflictError`.

La aplicación debe decidir una política explícita:

- revisión manual;
- prioridad fija de un origen;
- reintento después de reconciliar;
- creación de una nueva versión de negocio.

El motor no aplica automáticamente “última escritura gana”, porque podría destruir datos sin una regla de negocio.

## Journal y retención

`__compat_change_journal` crece con cada mutación local. Todavía no existe una API de poda incorporada. Antes de eliminar entradas, confirma que todos los consumidores hayan persistido un cursor posterior a esas secuencias y conserva una copia de seguridad recuperable.

Las tablas `__compat_*` son parte del estado operacional. No deben editarse manualmente durante la replicación.

## Cambios de esquema

La captura automática registra cambios de filas, no DDL. Un cambio de esquema requiere:

1. detener temporalmente la replicación;
2. drenar los journals pendientes;
3. auditar el nuevo esquema;
4. aplicar una migración coordinada en ambos motores;
5. reinstalar la captura con el nuevo `Schema`;
6. reanudar desde los cursores confirmados.

No mezcles cambios de columnas con streams capturados mediante una versión anterior del esquema.

## Copias de seguridad

Antes de una migración o cambio de esquema:

- respalda el archivo SQLite con una operación consistente;
- usa una copia lógica o física verificable de PostgreSQL;
- conserva el contrato, el esquema canónico y los cursores junto con el respaldo;
- prueba la restauración, no sólo la creación del backup.

## Observabilidad mínima

Registra y alerta sobre:

- última secuencia capturada y aplicada por origen;
- diferencia entre cursor y secuencia máxima;
- antigüedad del cambio pendiente más antiguo;
- cantidad y tamaño del journal;
- conflictos de replicación;
- duración de exportación, importación y verificación;
- errores de bloqueo SQLite y conexiones PostgreSQL;
- objetos `Inspection.Unresolved` después de cambios de esquema.

## Limitaciones operacionales actuales

- No hay servicio de red, scheduler ni daemon incluidos; la aplicación debe ejecutar el bucle de replicación.
- No hay poda automática del journal.
- No hay resolución automática de conflictos.
- No hay replicación automática de DDL.
- SQLite no expone un identificador de transacción equivalente a PostgreSQL; no uses `TransactionID` como identificador global entre motores.
- La compatibilidad con SQL arbitrario continúa incompleta; consulta [COMPATIBILITY.md](COMPATIBILITY.md).
