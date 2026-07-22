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
- Vistas canónicas con joins, filtros, agrupaciones y agregaciones.
- Triggers canónicos con efectos equivalentes.
- Rutinas transaccionales ejecutadas por el runtime común.
- Búsqueda textual Unicode determinista ejecutada por el runtime común.
- Reconstrucción exacta del esquema canónico persistido desde SQLite y PostgreSQL.
- Eliminación de las bases PostgreSQL temporales después de la prueba.

## Incumplimiento que permanece

La prueba de cobertura integral falla porque el sistema aún no proporciona equivalencia exacta demostrada para variantes arbitrarias específicas de cada dialecto de:

- Triggers.
- Vistas.
- Rutinas almacenadas.
- Búsqueda de texto completo.

El resultado de la batería es 11 pruebas superiores superadas y 1 fallida. Esta proporción no representa un porcentaje de compatibilidad total; el fallo significa que el objetivo del 100% no está cumplido.

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
