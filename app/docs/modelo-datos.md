# Modelo de datos

> **Estado:** esquema implementado en `migrations/00001_init.sql`,
> `00002_llm_requests.sql`, `00003_gcal_sync.sql` y `00004_avisos_dueno.sql`. En la Fase 0 se usan
> `tenants`, `customers`, `conversations`, `messages` y `appointments`; `channels` e
> `inbox` están creadas pero sin uso. Última actualización: 2026-09-29.

El SQL está en la migración; aquí va lo que no se deduce de leerla.

## Tablas y para qué sirven

| Tabla | Uso |
|---|---|
| `tenants` | Un negocio. `owner_notify` guarda el chat del dueño registrado para avisos (`{telegram_chat_id, registrado_at}`); el upsert de arranque no lo toca. `config` guarda el YAML completo como JSONB. Se hace upsert por `slug` en cada arranque (`internal/store/store.go:63`), así que **el YAML manda**: editar la fila a mano no sirve, se pisa en el siguiente arranque |
| `channels` | Qué bot o número de WhatsApp pertenece a qué negocio. Sin uso hasta que haya webhooks multi-tenant (en la Fase 0 el token viene de una variable de entorno) |
| `customers` | Cliente final, único por `(tenant_id, canal, external_user_id)`. En Telegram, `external_user_id` es el user id; en consola, el valor de `-usuario` |
| `conversations` | Agrupa mensajes. Se abre una nueva tras 24 h sin actividad (`store.go:104`). `estado = 'humano'` pausa el bot |
| `messages` | `rol` puede ser `user`, `assistant` o `tool`. `contenido` es `{"text": ...}` para user/assistant y `{name, input, result, is_error}` para tool |
| `inbox` | Cola de mensajes entrantes para la Fase 1 |
| `appointments` | Reservas. Las columnas `gcal_*` guardan el estado de la sincronización con Google Calendar (migración `00003`): confirmada sin `gcal_event_id` = falta crear el evento; no confirmada con `gcal_event_id` = falta borrarlo. Ver [google-calendar.md](google-calendar.md#eventos-de-las-reservas-sincronización). Las columnas `aviso_*` hacen lo mismo con los avisos al dueño (migración `00004`, ver [avisos-dueno.md](avisos-dueno.md#cómo-funciona-no-se-pierden-avisos)) |
| `llm_requests` | Un registro por request a un proveedor de LLM, para los límites de cuota (migración `00002`). Se purgan los de más de 2 días. Ver [proveedores-llm.md](proveedores-llm.md#límites-de-cuota) |

## Dobles reservas

`appointments_sin_solape` (`migrations/00001_init.sql:87`) es una restricción
`EXCLUDE USING gist (tenant_id WITH =, periodo WITH &&) WHERE (estado = 'confirmada')`.
Postgres rechaza físicamente dos reservas confirmadas que se solapen en el mismo
negocio. `store.CrearReserva` traduce ese error (SQLSTATE `23P01`, `store.go:205`) a
`booking.ErrOcupado`.

Esto importa porque la validación en Go no alcanza: dos clientes que piden la misma hora
al mismo tiempo leen la agenda antes de que el otro escriba y los dos pasan la
validación. **Verificado** con `TestReservasConcurrentesMismoHorario`: 10 goroutines
reservan el mismo horario y exactamente una lo consigue.

Detalles:

- `periodo` es `[inicio, fin)`, **sin** el buffer. La restricción evita solapes reales;
  el buffer entre citas lo aplica `booking.Validar`. En una carrera se podrían colar
  dos citas pegadas sin buffer. Se aceptó porque no es una doble reserva.
- Una reserva cancelada no participa en la restricción, así que el horario queda libre
  de inmediato.
- Requiere la extensión `btree_gist` (para usar `=` sobre `uuid` en un índice gist).
- Para varios profesionales habrá que agregar `resource_id` a la tabla y a la
  restricción.

## Fuente de verdad

- **Postgres** es la fuente de verdad de las reservas del bot.
- **Google Calendar** es un espejo para el dueño y una fuente de bloqueos manuales
  (ver [google-calendar.md](google-calendar.md)).
- Disponibilidad = horario (config) − reservas confirmadas (Postgres) − tramos
  ocupados de Calendar.

## Cancelación segura

`store.CancelarReserva` (`store.go:246`) filtra por `tenant_id`, `customer_id` y
`estado = 'confirmada'` en el mismo `UPDATE`. Compara `id::text = $1` para que un id
mal formado que invente el modelo devuelva "no encontrada" en vez de un error de
tipo de Postgres. **Verificado** con `TestCancelarSoloPropia`.

## Tokens

Cada mensaje `assistant` guarda `tokens_in`, `tokens_out` y `tokens_cache_read`,
sumados sobre todas las llamadas del loop de tools de ese turno
(`internal/engine/engine.go:111`). `tokens_in` incluye los tokens leídos y escritos en
el cache. Con esto se calcula el costo real por negocio:

```sql
SELECT t.slug, count(*) AS turnos, sum(tokens_in) AS tin, sum(tokens_out) AS tout,
       sum(tokens_cache_read) AS cache
FROM messages m JOIN conversations c ON c.id = m.conversation_id
JOIN tenants t ON t.id = c.tenant_id
WHERE m.rol = 'assistant' GROUP BY t.slug;
```

## Retención (pendiente, Fase 1)

Un job diario borrará `messages` con más de N meses y anonimizará `appointments`
viejas: conservará fecha y servicio para métricas y borrará los datos personales.
