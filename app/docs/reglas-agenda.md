# Reglas de agenda

> **Estado:** implementado (`internal/booking/`). Última actualización: 2026-09-29.

Cómo decide el sistema qué horarios ofrecer y cuándo acepta una reserva. Toda esta
lógica es Go puro y determinista; el LLM no participa.

## Configuración (YAML del negocio)

| Campo | Default | Efecto |
|---|---|---|
| `horario.<dia>` | cerrado | Rangos `HH:MM-HH:MM`. Claves sin tilde (`miercoles`, `sabado`) |
| `intervalo_minutos` | 30 | Cada cuánto puede empezar una hora, contado desde el inicio de cada rango |
| `buffer_minutos` | 0 | Tiempo libre obligatorio después de cada reserva |
| `anticipacion_minima_minutos` | 0 | No se ofrece nada que empiece antes de ahora + este valor |
| `dias_maximos_adelanto` | 30 | Hasta qué día se puede reservar |
| `feriados` | — | Fechas `AAAA-MM-DD` cerradas |
| `servicios[].duracion_minutos` | obligatorio | Cuánto ocupa la reserva |

La validación del YAML está en `internal/tenant/tenant.go:96`: si algo está mal
(rango invertido, servicio repetido, zona inválida), el proceso no arranca.

## Cálculo de horarios libres

`booking.Slots` (`internal/booking/slots.go:44`). Para cada día del rango pedido y cada
tramo de atención, recorre inicios cada `intervalo_minutos` y acepta un horario si:

1. **El servicio completo cabe en el tramo.** Con atención de 10:00 a 14:00, un
   servicio de 60 min puede empezar a las 13:00 pero no a las 13:30. No cruza la pausa
   de almuerzo.
2. **Está en la franja pedida.** "Mañana" es lo que empieza antes de las 13:00
   (`slots.go:27`); "tarde", desde las 13:00. Un horario a las 13:30 de un tramo
   matinal cuenta como tarde.
3. **Respeta la anticipación mínima** y el máximo de días.
4. **No choca con nada ocupado, incluido el buffer.** Con buffer de 10 min y una
   reserva de 11:00 a 11:30:
   - 10:30 no sirve: termina a las 11:00 y el buffer lo lleva hasta las 11:10.
   - 11:30 tampoco: la reserva anterior necesita su buffer hasta las 11:40.
   - 12:00 sí.

El buffer **no** se aplica contra el cierre: un servicio puede terminar justo a la
hora de cierre.

`consultar_disponibilidad` recorta la consulta a 14 días (`service.go:13`) y le
devuelve al modelo como máximo 30 horarios (`tools.go:36`).

## Validación al reservar

`booking.Validar` (`slots.go:88`) vuelve a comprobar el horario elegido justo antes de
insertar y explica por qué falla:

| Error | Cuándo |
|---|---|
| `ErrMuyLejos` | Supera `dias_maximos_adelanto` |
| `ErrMuyPronto` | No cumple la anticipación mínima |
| `ErrFueraDeHorario` | Día cerrado, feriado, hora que no coincide con el intervalo (10:15) o servicio que no cabe en el tramo |
| `ErrOcupado` | Choca con otra reserva o con su buffer |

Aunque `Validar` pase, la base puede rechazar la reserva si otro cliente tomó ese
horario en el mismo instante; también devuelve `ErrOcupado` (ver
[modelo-datos.md](modelo-datos.md#dobles-reservas)).

## Limitaciones actuales

- **Un recurso por negocio:** una barbería con 3 sillas atendería a un cliente a la
  vez. Para varios profesionales hay que agregar `resource_id`.
- **Un intervalo para todos los servicios:** un servicio de 45 min con intervalo de 30
  deja huecos de 15 min sin usar.
- **Sin bloqueos manuales todavía:** hasta integrar Google Calendar (Fase 1), el dueño
  no puede bloquear horas sin tocar el YAML.
