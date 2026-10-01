# Arquitectura

> **Estado:** Fase 0 implementada (núcleo + Telegram por long polling). Inbox,
> webhooks, Google Calendar y avisos al dueño siguen en diseño.
> Última actualización: 2026-09-29.

## Principios

1. **El LLM conversa, el código decide.** El modelo entiende al cliente y elige qué
   herramienta llamar. La disponibilidad y la validez de una reserva las calcula Go
   (`internal/booking/slots.go:44`). El modelo nunca "inventa" un horario libre.
2. **Un binario, un proceso.** Monolito en Go con Postgres. Sin colas externas ni
   Redis hasta que haga falta.
3. **El canal es un detalle.** El motor recibe un `engine.Inbound` normalizado
   (`internal/engine/engine.go:39`) y no sabe por dónde llegó el mensaje.
4. **Multi-tenant desde el día 1.** Todo registro lleva `tenant_id`, aunque en la
   Fase 0 el proceso carga un solo negocio.
5. **El `tenant_id` y el cliente nunca vienen del LLM.** Salen del canal y viajan en
   `tools.Session` (`internal/tools/tools.go:21`). Ninguna tool acepta esos IDs como
   input (ver [conversacion-y-llm.md](conversacion-y-llm.md#seguridad-prompt-injection)).

## Diagrama

### Hoy (Fase 0)

```
Telegram ──getUpdates──▶ channel/telegram ──▶ engine.Handle ──▶ llm.Provider
                                                                 (Anthropic, OpenAI, Ollama...)
   ▲       (long poll)    cola por chat          │   ▲
   └────── sendMessage ◀─────────────────────────┘   │ tool_use / tool_result
                                                  tools ──▶ booking ──▶ store ──▶ Postgres

Terminal ──▶ cmd/chat ──▶ engine.Handle (mismo motor, canal "consola")
```

### Objetivo (Fase 1+)

```
┌──────────────┐   ┌──────────────┐
│   Telegram   │   │  WhatsApp    │
└──────┬───────┘   └──────┬───────┘
       │ webhook          │ webhook
┌──────▼──────────────────▼──────────────────────────────────┐
│  channel/   → valida firma, normaliza, deduplica            │
│  inbox      → cola en Postgres (SKIP LOCKED), debounce      │
│  engine/    → loop LLM ↔ herramientas                       │
│     ├── tools/ ─ booking/ ─ store/                          │
│     ├── calendar/   Google Calendar (freebusy + eventos)    │
│     └── notify/     avisos al dueño                         │
│  scheduler/ → recordatorios, purga, reintentos de sync      │
└────────────────────────────────────────────────────────────┘
```

## Flujo de un mensaje (implementado)

1. `telegram.Bot.Run` (`internal/channel/telegram/telegram.go:58`) hace long polling.
   Solo acepta mensajes de texto de chats privados. Confirma cada update avanzando el
   `offset`, así Telegram no lo reenvía.
2. Cada chat tiene su propia goroutine y cola: los mensajes de un mismo cliente se
   procesan **en orden, uno a la vez**, y los de clientes distintos en paralelo.
   Mientras tanto se muestra "escribiendo..." (`telegram.go:142`).
3. `engine.Handle` (`internal/engine/engine.go:67`):
   - identifica al cliente (`store.UpsertCustomer`) y su conversación vigente. Si no
     hubo actividad en 24 h, abre una nueva (`store.go:104`);
   - si la conversación está en estado `humano`, no responde;
   - antepone la fecha y hora al texto, lo guarda y arma el historial de los últimos
     20 turnos de texto.
4. `engine.loop` (`engine.go:111`): llama al proveedor de LLM. Mientras `stop_reason` sea
   `tool_use`, ejecuta las tools y devuelve todos los resultados en un solo mensaje.
   Corta a las 6 iteraciones (`engine.go:21`).
5. Guarda la respuesta con el consumo de tokens sumado de todas las iteraciones y la
   devuelve al canal.

**Pendiente (Fase 1):** en producción, los webhooks deben guardar en `inbox` y
responder `200` sin esperar al LLM; agrupar ráfagas ("hola" / "quería hora") con un
debounce; y usar un lock por conversación en Postgres para que funcione con más de
una réplica. La cola por chat en memoria de la Fase 0 solo sirve con un proceso.

## Stack

| Pieza | Elección | Estado |
|---|---|---|
| Lenguaje | Go 1.26 (`go.mod`; el toolchain se descarga solo si el local es más viejo) | ✅ |
| DB | Postgres 16 + `pgx/v5`, SQL escrito a mano en `internal/store/` | ✅ |
| Migraciones | `goose`, embebidas con `go:embed` (`migrations/embed.go`) y aplicadas al arrancar (`store.go:46`) | ✅ |
| LLM | Interfaz `llm.Provider` con adaptadores para Anthropic (SDK oficial v1.76) y OpenAI-compatible (HTTP; cubre OpenAI, Ollama, Groq...). Ver [proveedores-llm.md](proveedores-llm.md) | ✅ |
| Telegram | HTTP directo al Bot API, sin librería | ✅ |
| Logs | `log/slog`: JSON en el server y texto a stderr en `cmd/chat` | ✅ |
| Config del negocio | YAML (`tenants/*.yaml`) → se guarda en `tenants.config` al arrancar | ✅ |
| Calendar | `google.golang.org/api/calendar/v3` con cuenta de servicio. Ver [google-calendar.md](google-calendar.md) | ✅ |
| WhatsApp | HTTP directo a Meta Cloud API | Fase 2 |
| Deploy | Dockerfile multi-stage → `distroless/static` (52 MB), Railway. Ver [deploy.md](deploy.md) | ✅ imagen / Railway pendiente |

**Cambio respecto del plan:** se postergó `sqlc`. Con ~10 queries, escribirlas con
`pgx` evita una herramienta y un paso de generación. Conviene reevaluarlo si las
queries pasan de ~30.

### Loop propio en vez del Tool Runner del SDK

El SDK de Anthropic trae `toolrunner` (beta). Se escribió el loop a mano
(`engine.go:111`), y hoy es además lo que permite cambiar de proveedor: el loop solo
habla con `llm.Provider`. También sirve para
guardar cada llamada a tool en `messages`, sumar tokens por turno, cortar por número
de iteraciones y distinguir errores de negocio (se le devuelven al modelo) de errores
de infraestructura (se registran en el log y el modelo recibe un error genérico).

## Ejecutables

- `cmd/server`: bot de Telegram. Requiere `DATABASE_URL`, `TELEGRAM_BOT_TOKEN` y la
  credencial del proveedor de LLM elegido.
- `cmd/chat`: chat por terminal contra el mismo motor, sin Telegram. Con
  `-usuario otro` se simula un cliente nuevo.
- `cmd/calcheck` (`make calendar-check`): verifica la cuenta de servicio y el acceso al
  calendario.

Los dos primeros usan `bootstrap.Build` (`internal/bootstrap/bootstrap.go:26`): carga el YAML,
conecta, migra, guarda el negocio y arma el motor.

## Trampas conocidas

- **Los tests no pueden usar la base de desarrollo.** Pasó el 2026-09-30:
  `TestCicloSyncCalendar` dejó un negocio de prueba con un calendario falso en la base
  `agenda`, y el bot real, al arrancar, intentó sincronizarlo contra Google. Ahora los
  tests usan `agenda_test` (`Makefile`) y `openTest` falla si `TEST_DATABASE_URL` es
  igual a `DATABASE_URL` (`internal/store/store_test.go`).
- **`.env` no debe pisar el entorno:** con `include .env` en el Makefile, un
  `LLM_MODEL=` vacío en `.env` pisaba `LLM_MODEL=x make chat`, porque las variables
  del Makefile tienen prioridad sobre el entorno. Ahora los programas cargan `.env`
  por su cuenta sin pisar lo ya definido (`internal/config/dotenv.go:12`) y el
  Makefile no lo incluye.
- **Zona horaria con distroless:** la imagen no trae tzdata. Los dos `main.go`
  importan `time/tzdata` (`cmd/server/main.go:13`).
- **Cambio de horario a medianoche:** en Chile el horario de verano empieza a las
  00:00, así que ese día la medianoche local no existe (00:00 → 01:00). Iterar días
  sumando 24 h a la medianoche desfasa el día y corta rangos antes de tiempo. `Slots`
  itera usando el mediodía (`slots.go:151`) y arma cada horario con `time.Date` a partir
  de hora y minuto explícitos. Cubierto por `TestSlotsCambioDeHorario`.
- **El token de Telegram va en la URL:** los errores de `net/http` incluyen la URL. Por
  eso `telegram.call` loguea solo la causa (`telegram.go:188`).

## Estructura del código

```
app/
├── cmd/server/          # bot de Telegram
├── cmd/chat/            # chat por terminal
├── internal/
│   ├── bootstrap/       # arma dependencias comunes
│   ├── config/          # variables de entorno
│   ├── tenant/          # Config del negocio, carga y validación del YAML
│   ├── booking/         # slots (lógica pura) + Service (reglas sobre el Store)
│   ├── store/           # Postgres
│   ├── calendar/        # Google Calendar: cliente + sincronizador
│   ├── notify/          # avisos al dueño + registro con /dueno
│   ├── bg/              # ciclo común de los procesos en segundo plano
│   ├── engine/          # loop de conversación
│   ├── llm/             # interfaz Provider, system prompt, prefijo de fecha
│   │   ├── anthropic/   # adaptador Claude
│   │   ├── openai/      # adaptador OpenAI-compatible (OpenAI, Gemini, Ollama...)
│   │   ├── limit/       # límites de cuota (RPM, TPM, RPD) sobre cualquier proveedor
│   │   └── providers/   # "proveedor/modelo" → Provider
│   ├── tools/           # definiciones y ejecución de tools
│   ├── formato/         # fechas y precios en español
│   └── channel/telegram/
├── migrations/
├── tenants/             # YAML de negocios
└── docs/
```

## Tests

| Test | Qué cubre | Requiere |
|---|---|---|
| `internal/booking/slots_test.go` | Horarios, pausa de almuerzo, franjas, feriados, anticipación, buffer, cambio de horario, errores de `Validar` | — |
| `internal/store/store_test.go` | 10 reservas concurrentes al mismo horario → exactamente 1 OK; un cliente no puede cancelar reservas ajenas | Postgres |
| `internal/calendar/*_test.go` | Freebusy (incluye calendario no compartido), creación idempotente (409), borrado de eventos inexistentes, id válido, sincronizador con Google caído. Contra un servidor falso que imita a Google | — |
| `internal/booking/service_test.go` | Bloqueos de Calendar descuentan horarios y bloquean reservas; degradación si Calendar falla | — |
| `internal/tools/tools_test.go` | Definiciones estrictas y estables entre llamadas | — |
| `internal/engine/engine_test.go` | Normalización del historial | — |
| `internal/engine/engine_integration_test.go` | Loop completo con un proveedor simulado: consultar → agendar → respuesta; tokens sumados | Postgres |
| `internal/llm/*/…_test.go` | Traducción de cada adaptador a su API, contra servidores HTTP falsos (ver [proveedores-llm.md](proveedores-llm.md#tests)) | — |
| `internal/tenant/tenant_test.go` | El YAML de demo carga; YAMLs inválidos fallan | — |

`make test` corre los tests unitarios; `make test-integration` además levanta Postgres
y corre los que usan la base, en una base **separada** (`agenda_test`, que se crea sola). **No hay tests contra APIs reales de LLM**: la calidad de
la conversación se prueba a mano con `make chat`.
