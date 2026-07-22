# Informe de validación integral

Fecha: 2026-07-22

## Resultado

El sistema todavía **no cumple el objetivo de compatibilidad total SQLite ↔ PostgreSQL**.

La batería se ejecutó contra SQLite real y PostgreSQL 17.5 real proporcionado por Laragon en `127.0.0.1:5432`. Cada ejecución creó una base PostgreSQL temporal y la eliminó al finalizar.

## Comprobaciones superadas

- Compilación y pruebas unitarias de todos los paquetes.
- Análisis estático con `go vet`.
- Migración SQLite → PostgreSQL → SQLite del núcleo portable.
- Ejecución completa de la CLI `compat-copy` y comprobación de filas en PostgreSQL.
- Preservación de decimales de 38 dígitos y 18 decimales.
- Preservación canónica de JSON, UUID y timestamps con nanosegundos.
- Comportamiento equivalente de claves foráneas.
- Acciones referenciales canónicas `CASCADE`, `RESTRICT`, `SET NULL`, `SET DEFAULT` y `NO ACTION`, con verificación conductual de actualización y eliminación en cascada.
- Restricciones `CHECK` canónicas aplicadas y rechazando los mismos datos inválidos.
- Índices canónicos únicos, parciales y descendentes creados y aplicados en ambos motores.
- Reconstrucción desde catálogos externos, sin metadatos del framework, de claves primarias, restricciones `UNIQUE`, claves foráneas compuestas, restricciones `CHECK` e índices comunes.
- Reconstrucción de valores por defecto literales de texto, enteros, decimales, booleanos, `NULL` y `CURRENT_TIMESTAMP`, incluidos casts nativos conocidos de PostgreSQL.
- Reconstrucción de vistas externas con proyecciones, alias, filtros, agrupación, orden, límite y desplazamiento dentro de la gramática `SELECT` común.
- Reconstrucción de vistas con joins `INNER`, `LEFT` y `CROSS`, condiciones compuestas y agregaciones `COUNT`, `SUM`, `AVG`, `MIN` y `MAX`.
- Reconstrucción de triggers externos `BEFORE`/`AFTER` para `INSERT`, `UPDATE` y `DELETE` con condición e inserciones de auditoría basadas en `NEW`/`OLD`.
- Acciones canónicas `INSERT`, `UPDATE` y `DELETE` dentro de triggers, con predicados y asignaciones basadas en `NEW`/`OLD`.
- Detección explícita de funciones y procedimientos PostgreSQL independientes todavía no traducidos.
- Traducción de procedimientos PostgreSQL `SQL`/`PLpgSQL` parametrizados con inserciones canónicas y ejecución equivalente mediante el runtime común sobre SQLite y PostgreSQL.
- Detección explícita de defaults no portables, identidades y columnas generadas.
- Rechazo explícito de expresiones, métodos, colaciones y clases de operador que no pertenecen a la gramática canónica comprobada.
- Vistas canónicas con joins, filtros, agrupaciones y agregaciones.
- Triggers canónicos con efectos equivalentes.
- Rutinas transaccionales ejecutadas por el runtime común.
- Búsqueda textual Unicode determinista ejecutada por el runtime común.
- Reconstrucción exacta del esquema canónico persistido desde SQLite y PostgreSQL.
- Replicación incremental SQLite → PostgreSQL y PostgreSQL → SQLite.
- Captura automática mediante triggers nativos de `INSERT`, `UPDATE` y `DELETE` en ambos motores.
- Lectura ordenada por cursor y supresión transaccional de ecos al aplicar cambios remotos.
- Preservación de datos binarios en el journal automático.
- Reintentos idempotentes de secuencias ya aplicadas.
- Detección de conflictos antes de sobrescribir cambios divergentes (cubierta por la suite unitaria, no por la batería E2E).
- Eliminación de las bases PostgreSQL temporales después de la prueba.

## Incumplimiento que permanece

La prueba de cobertura integral falla porque el sistema aún no proporciona equivalencia exacta demostrada para variantes arbitrarias específicas de cada dialecto de:

- Claves foráneas con modos `MATCH`, diferimiento u otras extensiones específicas.
- Restricciones `CHECK`.
- Índices y expresiones de índice.
- Triggers con control de flujo, SQL dinámico u otra sintaxis procedural arbitraria.
- Vistas con subconsultas, operaciones de conjuntos, ventanas u otras extensiones todavía fuera del parser común.
- Funciones con retorno, parámetros avanzados o lógica procedural fuera de las inserciones canónicas.
- Búsqueda de texto completo.

El resultado de la batería es 15 pruebas superiores superadas y 1 fallida. Esta proporción no representa un porcentaje de compatibilidad total; el fallo significa que el objetivo del 100% no está cumplido.

## Defectos detectados y corregidos durante la ejecución

- Pérdida de precisión decimal por almacenamiento SQLite `REAL`; ahora usa representación canónica sin pérdida.
- Redondeo de timestamps PostgreSQL de nanosegundos a microsegundos; ahora conserva la representación canónica completa.
- Diferencias de normalización JSON; ahora se normaliza antes de comparar.
- Claves foráneas desactivadas en conexiones SQLite; ahora se habilitan en todas las conexiones del adaptador.
- Bloqueo al inspeccionar el catálogo SQLite con una sola conexión; ahora el catálogo se materializa antes de consultar columnas.

## Ejecución

```powershell
.\scripts\test-system.ps1
```

La ejecución debe seguir terminando con código distinto de cero mientras exista cualquier familia requerida sin equivalencia exacta.
