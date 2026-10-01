# app — bot de agendamiento

Código del bot. Cómo funciona: [docs/](docs/README.md).

## Requisitos

- Go (el `go.mod` pide 1.26; si tienes uno más viejo, Go descarga el toolchain solo).
- Docker, para el Postgres local.
- Una API key del proveedor de LLM (Anthropic, OpenAI...) u Ollama corriendo en local.
- Para Telegram: un bot creado con [@BotFather](https://t.me/BotFather).

## Arranque

```bash
cp .env.example .env      # completar ANTHROPIC_API_KEY (y TELEGRAM_BOT_TOKEN)
make db-up                # Postgres en localhost:5433
make chat                 # conversar desde la terminal
make run                  # o levantar el bot de Telegram
```

## Elegir el modelo

El modelo se indica como `proveedor/modelo`, en `modelo_llm` del YAML o con
`LLM_MODEL` (que tiene prioridad):

```bash
make chat                                   # anthropic/claude-opus-5 (default)
LLM_MODEL=openai/gpt-5 make chat            # requiere OPENAI_API_KEY
LLM_MODEL=gemini/gemini-3.6-flash make chat # requiere GEMINI_API_KEY
LLM_MODEL=ollama/qwen3:8b make chat         # Ollama local, sin clave
LLM_MODEL=openai-compatible/llama-3.3-70b-versatile \
  LLM_BASE_URL=https://api.groq.com/openai/v1 LLM_API_KEY=... make chat
```

| Variable | Para qué |
|---|---|
| `LLM_MODEL` | `proveedor/modelo`; pisa el YAML |
| `ANTHROPIC_API_KEY` | Proveedor `anthropic` |
| `OPENAI_API_KEY` | Proveedor `openai` |
| `LLM_BASE_URL` | URL de un servidor `openai-compatible`, o de Ollama si no está en `localhost:11434` |
| `LLM_API_KEY` | Clave para `openai-compatible` u Ollama remoto |
| `GEMINI_API_KEY` | Proveedor `gemini` |
| `LLM_LIMITE_RPM`, `LLM_LIMITE_TPM`, `LLM_LIMITE_RPD` | Límites de cuota (vacío = sin límite). Para la capa gratuita de Gemini: 5, 250000, 20 |
| `LLM_LIMITE_ZONA_RESET`, `LLM_LIMITE_ESPERA_MAX` | Zona del reinicio diario (default `America/Los_Angeles`) y espera máxima por cupo (default `90s`) |

Solo hace falta la credencial del proveedor elegido. Detalle en
[docs/proveedores-llm.md](docs/proveedores-llm.md).

Las variables definidas en el entorno tienen prioridad sobre `.env`.

## Google Calendar (opcional)

| Variable | Para qué |
|---|---|
| `GOOGLE_APPLICATION_CREDENTIALS` | Ruta al JSON de la cuenta de servicio, p. ej. `secrets/google-sa.json` |
| `GOOGLE_CALENDAR_ID` | Calendario del negocio; pisa `google_calendar_id` del YAML |

`make calendar-check` verifica la configuración. Paso a paso en
[docs/google-calendar.md](docs/google-calendar.md#configuración).

## Avisos al dueño (opcional)

Con `CODIGO_DUENO=<secreto>` en `.env`, el dueño le escribe al bot `/dueno <secreto>` y
desde ahí recibe un mensaje por cada reserva y cancelación. Detalle en
[docs/avisos-dueno.md](docs/avisos-dueno.md).

## Notas

Las migraciones se aplican solas al arrancar. El negocio se carga desde `TENANT_FILE`
(por defecto `tenants/barberia-demo.yaml`) en cada arranque.

## Tests

```bash
make test               # unitarios
make test-integration   # + Postgres (reservas concurrentes, cancelación)
```

## Agregar un negocio

Copiar `tenants/barberia-demo.yaml`, editarlo y apuntar `TENANT_FILE` al nuevo archivo.
En la Fase 0, cada proceso atiende a un solo negocio.
