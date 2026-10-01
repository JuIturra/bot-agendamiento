# Google Calendar

> **Estado:** implementado (Fase 1) y **verificado de punta a punta con un
> calendario real y Telegram** el 2026-09-30:
> - al arrancar, el sincronizador creó el evento de una reserva previa;
> - un bloqueo manual de 11:00 a 12:00 dejó de ofrecerse: quedó solo 12:30, porque
>   12:00 cae en el buffer;
> - al cancelar por Telegram, el evento se borró de Google en el mismo segundo.
>
> Última actualización: 2026-09-30.

## Qué hace

1. **Cada reserva aparece como evento** en el calendario del dueño ("Arreglo de barba —
   Julians Iturra"), y se borra si el cliente cancela.
2. **Los bloqueos manuales del dueño se respetan:** si pone "dentista 15:00" o
   "vacaciones" en su calendario, el bot no ofrece esos horarios ni deja reservarlos.

Calendar **no** es la fuente de verdad: lo es Postgres (ver
[modelo-datos.md](modelo-datos.md#fuente-de-verdad)).

## Configuración

| Qué | Dónde |
|---|---|
| JSON de la cuenta de servicio | `GOOGLE_APPLICATION_CREDENTIALS=secrets/google-sa.json` (la carpeta `secrets/` está en `.gitignore`) |
| Calendario del negocio | `google_calendar_id` en el YAML, o `GOOGLE_CALENDAR_ID`, que tiene prioridad (`internal/bootstrap/bootstrap.go:79`) |

Sin calendario configurado, el bot funciona igual que antes. Con calendario pero sin
credenciales, no arranca.

`make calendar-check` (`cmd/calcheck`) muestra el email de la cuenta de servicio, que
es con quien hay que compartir el calendario, y prueba leer la próxima semana.

### Paso a paso (una vez por proyecto de Google Cloud)

1. En https://console.cloud.google.com, crear un proyecto (o usar uno existente).
2. **APIs y servicios → Biblioteca → "Google Calendar API" → Habilitar.**
3. **IAM y administración → Cuentas de servicio → Crear cuenta de servicio.** No
   necesita roles de IAM: el acceso se da compartiendo el calendario.
4. En la cuenta creada: **Claves → Agregar clave → JSON.** Guardar el archivo en
   `app/secrets/google-sa.json`.
5. Por cada negocio: en Google Calendar, **Configuración del calendario → Compartir
   con determinadas personas → agregar el email de la cuenta de servicio** con permiso
   **"Hacer cambios en eventos"**. El id del calendario está en la misma página, en
   "Integrar el calendario" (para el calendario principal es el email del dueño).

**[verificar]** Algunas organizaciones de Google Workspace bloquean la creación de
claves de cuentas de servicio con una política (`iam.disableServiceAccountKeyCreation`).
Con una cuenta personal de Gmail no aplica.

## Autenticación: cuenta de servicio

`calendar.New` (`internal/calendar/calendar.go:28`) usa el JSON con los scopes
`calendar.events` y `calendar.freebusy`, los mínimos para crear y borrar eventos y leer
ocupados. No hay OAuth, ni pantalla de consentimiento, ni verificación de Google.

Más adelante, si pedirle al dueño que comparta el calendario se vuelve un obstáculo, se
puede agregar OAuth ("Conectar con Google"). Con scopes de Calendar, eso implica pasar
por la verificación de Google.

## Bloqueos del dueño (freebusy)

`booking.Service.Externos` (`internal/booking/service.go:57`) apunta a `Client.Busy`
(`calendar.go:47`). `ocupados` (`service.go:67`) junta las reservas de Postgres con los
tramos ocupados del calendario, tanto al consultar disponibilidad como al revalidar
antes de reservar. Cubierto por `TestExternosYAlCambiar`.

Decisiones:

- **Si Google no responde, se sigue con las reservas de Postgres** y queda un ERROR en
  el log. Es preferible arriesgar un choque con un bloqueo manual a que el bot deje de
  agendar. Si en la práctica pasa seguido, se puede cambiar a "no agendar".
- **Un calendario no compartido no da error HTTP:** freebusy responde 200 con
  `errors: [{reason: "notFound"}]` dentro del calendario. `Busy` lo detecta y lo
  informa (`TestBusy`). Sin esa revisión, un calendario mal compartido parecería vacío
  y los bloqueos del dueño se ignorarían sin aviso.
- **El buffer entre citas también se aplica después de un bloqueo manual.** Con
  buffer de 10 min, un bloqueo hasta las 12:00 deja libre recién desde las 12:10, así
  que no se ofrecen las 12:00 (visto en la prueba real).
- **Solo se ven los calendarios compartidos con la cuenta de servicio.** En la prueba
  real, el primer bloqueo se creó sin querer en el calendario personal y el bot lo
  ignoró: para la cuenta de servicio es invisible. Es un caso probable con dueños
  reales, que anotan sus compromisos en su calendario personal. Mejora pendiente:
  leer ocupados de varios calendarios (el del negocio + el personal, compartido solo
  como "ver disponible/ocupado"); freebusy acepta varios calendarios en una consulta.
- Los eventos del propio bot también aparecen como ocupados. No importa, porque
  coinciden con reservas que ya se descuentan.
- Al cancelar, el evento sigue en Google hasta que el sincronizador lo borra, que
  normalmente tarda segundos. En ese lapso el horario figura como ocupado.

## Eventos de las reservas (sincronización)

La reserva nunca espera a Google. `booking.Service.AlCambiar` avisa al sincronizador
(`Syncer.Kick`, `internal/calendar/sync.go:65`), que corre en segundo plano
(`bootstrap.go:123`):

- `SyncOnce` (`sync.go:94`) pide a `store.PendientesCalendar`
  (`internal/store/calendar.go:13`) las reservas **confirmadas sin evento** (crear) y
  las **canceladas que todavía tienen evento** (borrar). Solo considera negocios con
  `calendar_id` y no crea eventos de reservas que ya terminaron.
- Un fallo de Google en una reserva no corta el barrido. Se registra en `gcal_error` y
  se reintenta con backoff de 1, 2, 4... minutos, con tope de 1 hora
  (`store/calendar.go:48`).
- Además del aviso inmediato, barre cada 1 minuto.
- Al salir (`make chat`), `App.Close` (`bootstrap.go:36`) hace un último barrido con 15 s
  de timeout, para que una reserva hecha justo antes de salir no quede sin evento.

### Idempotencia: el id del evento sale de la reserva

`EventID` (`calendar.go:94`) convierte el UUID de la reserva en el id del evento
("agenda" + UUID sin guiones; Google exige caracteres `0-9a-v` y entre 5 y 1024 de
largo). Si el evento se crea en Google pero falla el `UPDATE` en Postgres, el reintento
recibe **409** y se toma como éxito (`calendar.go:99`), en vez de duplicar el evento.
Borrar un evento que ya no existe (404/410) también es éxito. Por eso tampoco importa
que dos procesos (bot y chat) sincronicen a la vez. Cubierto por
`TestCreateEventIdempotente` y `TestDeleteEvent`.

### Contenido del evento

- Título: `<servicio> — <nombre del cliente>`.
- Descripción: canal (telegram, whatsapp...) e id de la reserva.
- `extendedProperties.private.appointment_id` para reconciliar.
- Inicio y fin con la zona horaria del negocio. Los recordatorios son los que el dueño
  tenga configurados por defecto.
- No se incluye el teléfono: Telegram no lo entrega. Con WhatsApp sí se podrá.

## Limitaciones

- **Sincronización en un solo sentido.** Si el dueño borra o mueve el evento en
  Google, la reserva sigue igual en Postgres y el cliente no se entera. El bloqueo sí
  se respeta, porque freebusy lo ve. La sincronización en ambos sentidos queda para
  más adelante (notificaciones push de Calendar o un barrido de reconciliación).
- Las reservas hechas antes de configurar el calendario se sincronizan al activarlo,
  si todavía no pasaron.
