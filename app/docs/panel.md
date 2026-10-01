# Panel del operador

> **Estado:** implementado y verificado en local el 2026-09-30 contra la base de
> desarrollo con conversaciones reales (`make panel`). **Todavía no está publicado
> en Railway.** El panel para el dueño queda pendiente. Última actualización:
> 2026-09-30.

Panel web de solo lectura para ver qué está pasando sin consultar la base ni leer
logs.

## Páginas

| Ruta | Qué muestra |
|---|---|
| `/` | **Resumen de 7 días:** conversaciones, cuántas terminaron en reserva (%), reservas y cancelaciones, próximas reservas, requests al LLM hoy contra el límite diario, tokens. **Salud:** último contacto con Telegram, eventos de Calendar pendientes o con error, avisos al dueño pendientes o con error, con el último mensaje de error |
| `/conversaciones` | Las 100 más recientes: cliente, canal, último mensaje, vista previa, mensajes, tokens y si reservó |
| `/conversaciones/{id}` | La conversación completa tal como la vio el modelo: mensajes del cliente (con el prefijo de fecha), respuestas del bot con sus tokens y **cada herramienta llamada, con input y resultado** |
| `/reservas` | Próximas y de los últimos 7 días, con estado, sincronización con Calendar y aviso al dueño, y sus errores |
| `/healthz` | `ok` sin contraseña, para el health check de Railway |

Definiciones:
- **"Terminó en reserva"** = alguna llamada a `agendar` de esa conversación respondió
  OK (`sqlAgendoOK`, `internal/store/panel.go`).
- **Telegram "sin conexión"** = más de 2 minutos sin un `getUpdates` exitoso. Cada poll
  dura hasta 30 s, así que es una señal confiable. `telegram.Bot.UltimoPoll`
  (`internal/channel/telegram/telegram.go:122`).
- **Requests al LLM hoy** cuenta la tabla `llm_requests`, que solo se llena con
  límites de cuota activos (ver [proveedores-llm.md](proveedores-llm.md#límites-de-cuota)).
  Sin límites marca 0.

## Cómo se usa

| Dónde | Cómo |
|---|---|
| Producción (Railway) | El servidor (`cmd/server`) sirve el panel en `PORT` junto al bot. Requiere `PANEL_PASSWORD` y un dominio público del servicio (ver [deploy.md](deploy.md)) |
| Local | `make panel` (`cmd/panel`): **solo el panel, sin bot**, en `127.0.0.1:8080`, contra `DATABASE_URL`. Sirve para revisar la base local o la de producción sin competir con el bot de Telegram que esté corriendo. Ahí la tarjeta de Telegram dice "no disponible" |

Variables: `PANEL_PASSWORD` (obligatoria para publicarlo), `PANEL_USUARIO` (default
`admin`) y `PORT` (default `8080`).

## Seguridad

Muestra nombres y conversaciones de clientes reales, que son datos personales (y con
clínicas, de salud).

- **Sin `PANEL_PASSWORD` no se publica nada:** todas las rutas, salvo `/healthz`,
  responden 404 (`internal/panel/panel.go:148`).
- **Basic auth**, comparando usuario y contraseña en tiempo constante y siempre los
  dos (`panel.go:168`). Los intentos fallidos quedan como warning en el log. En
  Railway va sobre HTTPS; sin HTTPS, basic auth expone la contraseña.
- **Solo lectura y sin JavaScript.** No hay formularios ni acciones, así que no hace
  falta protección CSRF.
- **Encabezados:** `Cache-Control: no-store`, `X-Frame-Options: DENY`, `nosniff`,
  `Referrer-Policy: no-referrer` y una CSP que bloquea scripts, frames y formularios.
- **Escapado:** el nombre y los mensajes del cliente los escribe cualquiera por
  Telegram. `html/template` los escapa; `TestContenidoYEscape` verifica que un
  `<script>` en el nombre o en un mensaje no llegue sin escapar.
- **Aislamiento por negocio:** todas las consultas filtran por `tenant_id`, y el id de
  conversación de la URL se compara como texto, así que un id inventado es 404
  (`TestConsultasPanel`).
- `cmd/panel` escucha solo en `127.0.0.1`.

## Implementación

- `internal/panel/`: handlers, formato y plantillas (`templates/*.html`, embebidas en
  el binario). Tema claro y oscuro según el sistema; se adapta al ancho del teléfono.
- `internal/store/panel.go`: las consultas. El resumen es una sola query con
  subconsultas.
- **Las condiciones de "pendiente" de Calendar y de avisos están repetidas** en
  `store/panel.go` (`sqlCalendarPendiente`, `sqlAvisoPendiente`) y en los
  sincronizadores (`store/calendar.go`, `store/avisos.go`). Si cambia una, hay que
  cambiar la otra.
- `/healthz` responde 200 aunque Telegram no conecte: durante un redeploy, la
  instancia nueva no puede leer de Telegram hasta que se apaga la vieja (409). Si el
  health check dependiera de Telegram, el deploy fallaría siempre.

## Trampa encontrada

`Conversacion` embebe `ConversacionFila`. Cuando los dos tenían un campo `Mensajes`
(uno el contador y el otro la lista), en la plantilla ganaba el de afuera y el
encabezado imprimía la lista entera. Se vio con datos reales; los tests con datos
falsos no lo detectaron porque no revisaban el encabezado. Se renombró el contador a
`MensajesCliente` y el test ahora lo verifica.

## Pendiente

- **Panel para el dueño:** próximas reservas, resumen semanal y conversaciones, con
  acceso por enlace temporal que manda el bot (`/panel`), sin contraseñas.
- Costo estimado en pesos por negocio, cuando se fijen los precios por modelo.
- Paginación y búsqueda de conversaciones, cuando sean más de 100.
