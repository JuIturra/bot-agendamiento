# Avisos al dueño

> **Estado:** implementado (Fase 1) para reservas nuevas y cancelaciones, por
> Telegram. **Verificado desde Telegram** el 2026-09-30: registro con `/dueno` y aviso
> de una reserva nueva, enviado en el mismo segundo que la reserva. Las reservas
> canceladas antes del registro no se avisaron, como corresponde. El aviso de
> cancelación está cubierto por tests pero falta probarlo en vivo. `derivar_a_humano`,
> urgencias y el resumen diario siguen en diseño. Última actualización: 2026-09-30.

## Qué recibe el dueño

```
📅 Nueva reserva
Corte de pelo, viernes 02-10-2026 a las 10:00
Cliente: Ana (Telegram)
```

```
❌ Reserva cancelada
Corte de pelo, viernes 02-10-2026 a las 10:00
Cliente: Ana (Telegram)
El horario quedó libre.
```

Los arma `notify.Mensaje` (`internal/notify/notify.go:94`) con texto fijo. No pasan
por el modelo: no gastan cuota y siempre dicen lo mismo.

**No se avisa:**
- **Lo anterior al registro del dueño.** Si no, al registrarse le llegaría todo el
  historial de golpe.
- **Una cancelación de una reserva de la que el dueño nunca supo.** Por ejemplo, una
  reserva hecha y cancelada mientras Telegram estaba caído: se resuelve sin mensaje.

## Registro del dueño

El dueño le escribe al **mismo bot** que usan los clientes:

```
/dueno <código>
```

- El código sale de `CODIGO_DUENO` en el entorno. Si está vacío, el registro queda
  desactivado y hay un warning al arrancar.
- `notify.Registro.Handle` (`notify.go:138`) atiende el comando antes de que el
  mensaje llegue al modelo (`cmd/server/main.go:66`). No gasta cuota y no queda en el
  historial de conversaciones.
- La comparación del código es en tiempo constante, y los intentos fallidos quedan
  como warning en el log.
- El chat queda en `tenants.owner_notify` como `{telegram_chat_id, registrado_at}`
  (`internal/store/avisos.go:62`). Volver a registrarse, desde otro chat o el mismo,
  reemplaza el chat y la fecha de registro.
- El comando va sin tilde porque Telegram solo acepta `[a-z0-9_]` en comandos.
- **Por qué un código y no un chat_id en la configuración:** el dueño de una
  barbería no sabe qué es un chat_id. Con el código, el registro toma 10 segundos y
  se puede hacer en la visita de instalación.

## Cómo funciona (no se pierden avisos)

Mismo patrón que la sincronización con Calendar
([google-calendar.md](google-calendar.md#eventos-de-las-reservas-sincronización)):
**el estado del aviso vive en la reserva** (`migrations/00004_avisos_dueno.sql`):

| Estado de la reserva | Significa |
|---|---|
| `confirmada` y `aviso_creacion_at` nulo | Falta avisar la reserva |
| `cancelada` y `aviso_cancelacion_at` nulo | Falta avisar la cancelación |

Si el proceso se cae entre la reserva y el aviso, el aviso sale cuando vuelva. No hay
una cola aparte que se pueda desincronizar.

- `booking.Service` llama a `App.cambio` después de cada reserva o cancelación, y este
  despierta tanto al sincronizador de Calendar como al de avisos
  (`internal/bootstrap/bootstrap.go:39`). El aviso sale en segundos.
- `Notifier.EnviarPendientes` (`notify.go:65`) toma de
  `store.AvisosPendientes` (`avisos.go:13`) las reservas con aviso pendiente de
  negocios con dueño registrado. Si Telegram falla, registra `aviso_error` y reintenta
  con backoff de 1, 2, 4... minutos, con tope de 1 hora (`avisos.go:50`).
- Además del aviso inmediato, barre cada 1 minuto. El ciclo es `bg.Loop`
  (`internal/bg/bg.go:38`), compartido con Calendar.
- El aviso al dueño suele llegar **antes** que la confirmación al cliente: sale apenas
  se crea la reserva, mientras la respuesta al cliente espera a que el modelo termine
  de redactar (visto en la prueba real). En chats distintos no importa.
- **Solo `cmd/server` envía avisos**, porque necesita el bot de Telegram. Una reserva
  hecha desde `make chat` queda pendiente y se avisa la próxima vez que corra el
  servidor (siempre que sea posterior al registro del dueño).

## Tests

| Test | Qué cubre |
|---|---|
| `notify/notify_test.go` | Textos, cancelación sin aviso previo, Telegram caído, registro con código correcto, incorrecto y desactivado |
| `store/store_test.go` (`TestCicloAvisosDueno`) | Lo anterior al registro no se avisa; reserva → pendiente → marcada; cancelación → pendiente con "ya avisada"; backoff después de un error |

## Pendiente

- **`derivar_a_humano`:** el bot le pasa la conversación al dueño con un resumen y se
  pausa (estado `humano`). Requiere este mismo canal de avisos.
- **Urgencias configurables** ("choque", "perro no respira"): avisar aunque no haya
  reserva.
- **Resumen diario** con las conversaciones que no terminaron en reserva.
- Para responderle al cliente, hoy el dueño solo sabe su nombre: Telegram no entrega
  el teléfono. Con WhatsApp el aviso podrá incluir el número.
