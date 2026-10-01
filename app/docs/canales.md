# Canales (Telegram y WhatsApp)

> **Estado:** Telegram por long polling implementado y **probado desde un teléfono**
> el 2026-09-30 (saludo, consulta de servicios, disponibilidad y reserva real con
> Gemini 3.6 Flash). Webhooks y WhatsApp siguen en diseño. Última actualización:
> 2026-09-30.

## Contrato con el motor

Todo canal entrega un `engine.Inbound` (`internal/engine/engine.go:39`): canal, id
externo del usuario, nombre de perfil, texto y hora de envío. El motor devuelve el
texto de respuesta, o `""` si no debe responder.

Por ahora no hay una interfaz `Channel` formal: con un solo canal no hacía falta. Se
definirá al agregar WhatsApp, y probablemente tendrá esta forma:

```go
type Channel interface {
    Parse(r *http.Request) ([]InboundMessage, error) // valida firma y normaliza
    Send(ctx context.Context, to Recipient, msg OutboundMessage) error
}
```

## Telegram (implementado)

`internal/channel/telegram/telegram.go`

- **Long polling** con `getUpdates` y timeout de 30 s (`telegram.go:19`). No expone
  puertos, así que funciona en local sin ngrok.
- `interpretar` (`telegram.go:180`) decide qué hacer con cada update:
  - **Solo chats privados.** Los grupos se ignoran.
  - **`/start`**, lo primero que envía Telegram al abrir el bot, se convierte en
    "Hola" (también `/start algo` de un deep link y `/start@NombreBot`). `/ayuda` y
    `/help` se convierten en "¿En qué me puedes ayudar?". Antes, `/start` llegaba al
    modelo como texto literal.
  - **Foto o documento con texto al pie:** se usa ese texto.
  - **Audio, sticker, ubicación o foto sin texto:** se responde fijo "Por ahora solo
    puedo leer mensajes de texto" (`telegram.go:176`), sin llamar al modelo ni gastar
    cuota. Antes el cliente quedaba sin respuesta, y en Chile muchos clientes mandan
    audios. Transcribirlos queda como mejora futura.
- Los mensajes de más de 4000 caracteres se dividen en varios, cortando en espacios o
  saltos de línea (`telegram.go:216`). Telegram rechaza los de más de 4096.
- **Una goroutine por chat** con cola de 32 mensajes (`telegram.go:58`): orden
  garantizado por cliente y paralelismo entre clientes. Si la cola se llena, el mensaje
  se descarta y queda un warning en el log.
- Muestra "escribiendo..." mientras procesa y lo repite cada 4 s, porque Telegram lo
  apaga a los 5 (`telegram.go:142`).
- Si el motor falla, responde un mensaje genérico de error.
- **Error 409 "Conflict":** Telegram solo permite un lector por bot. Si hay otro
  `make run` abierto o un webhook configurado, `getUpdates` falla y el log lo explica
  (`telegram.go:98`). Por eso conviene un bot distinto para desarrollo y producción.
- **Limitación:** el `offset` se guarda en memoria. Si el proceso se reinicia con
  mensajes sin confirmar, Telegram los reenvía y se procesan. Como la confirmación es
  inmediata, en la práctica solo afecta al último lote recibido antes de caerse.

### Cómo probarlo

1. Crear un bot con @BotFather (`/newbot`) y copiar el token.
2. En `app/.env`: `TELEGRAM_BOT_TOKEN=...` y el modelo, p. ej.
   `LLM_MODEL=gemini/gemini-3.6-flash`.
3. `make db-up && make run`.

Primera prueba real (2026-09-30): 6 turnos usaron 8 requests de Gemini (hubo un 503 que
el limitador reintentó bien) y entre ~1500 y ~4300 tokens de entrada por turno. El
bot usó el nombre del perfil de Telegram para la reserva sin preguntarlo, como está
previsto (ver [conversacion-y-llm.md](conversacion-y-llm.md#fecha-y-hora)).

### Pendiente para producción (Fase 1)

- Cambiar a webhook (`setWebhook` con `secret_token`, validado en el header
  `X-Telegram-Bot-Api-Secret-Token`).
- Guardar en `inbox` y responder `200` de inmediato.
- Un bot por negocio, resuelto por `channels`.

## WhatsApp (diseño, Fase 2)

- **Meta WhatsApp Cloud API directo**, sin Twilio ni 360dialog.
- Requisitos: cuenta de Meta Business verificada, un número que **no** esté usando la app
  normal de WhatsApp y plantillas aprobadas para los mensajes que inicia el negocio.
- **Ventana de 24 h:** se puede responder libremente dentro de las 24 h desde el último
  mensaje del cliente. Fuera de esa ventana (recordatorios) solo se pueden enviar
  plantillas aprobadas, que Meta cobra por mensaje.
  **[verificar]** precios vigentes para Chile por categoría.
- Webhook único para todos los números; el negocio se resuelve por `phone_number_id`.
- Validar `X-Hub-Signature-256` y deduplicar por `message_id`, porque Meta reintenta.
- **[verificar]** la política vigente de Meta sobre chatbots de IA en WhatsApp Business
  antes de invertir en la demo.
- Para las demos alcanza el número de prueba que da Meta.
