# Proveedores de LLM (plug and play)

> **Estado:** implementado. **Gemini (`gemini-3.6-flash`) probado contra la API real**
> el 2026-09-29, con el flujo completo: precio, disponibilidad, reserva, consulta y
> cancelación. Anthropic, OpenAI y Ollama solo están testeados contra servidores HTTP
> falsos. Última actualización: 2026-09-29.

## Cómo se elige el modelo

Un solo identificador con formato `proveedor/modelo`:

```yaml
# tenants/<negocio>.yaml
modelo_llm: ollama/qwen3:8b
```

O para todo el proceso, pisando el YAML:

```bash
LLM_MODEL=openai/gpt-5 make chat
```

Prioridad: `LLM_MODEL` > `modelo_llm` del YAML > `anthropic/claude-opus-5`
(`internal/bootstrap/bootstrap.go`). Sin prefijo se asume `anthropic/`. Un prefijo
desconocido es un error al arrancar, no un fallback silencioso
(`internal/llm/providers/providers.go:70`).

| Proveedor | Ejemplo | Credenciales | URL por defecto |
|---|---|---|---|
| `anthropic` | `anthropic/claude-opus-5` | `ANTHROPIC_API_KEY` | API de Anthropic |
| `openai` | `openai/gpt-5` | `OPENAI_API_KEY` | `https://api.openai.com/v1` |
| `gemini` | `gemini/gemini-3.6-flash` | `GEMINI_API_KEY` | `https://generativelanguage.googleapis.com/v1beta/openai` |
| `ollama` | `ollama/qwen3:8b` | ninguna | `http://localhost:11434/v1` |
| `openai-compatible` | `openai-compatible/llama-3.3-70b-versatile` | `LLM_API_KEY` | `LLM_BASE_URL` (obligatoria) |

`LLM_BASE_URL` también pisa la URL de `ollama` (p. ej. Ollama en otra máquina) y de los
demás proveedores. `openai-compatible` sirve para Groq, OpenRouter, DeepSeek, vLLM, LM
Studio. **[verificar]** cada uno antes de usarlo: el soporte de tools varía.

### Gemini

Usa el adaptador OpenAI-compatible contra el endpoint de compatibilidad de Google,
que según su documentación sigue en beta (consultado el 2026-09-29).

- **Modelos retirados:** la familia 1.5 se apagó el 29-09-2025. `gemini-2.0-flash`
  figura como deprecado, con apagado desde el 01-06-2026 (fecha "más temprana
  posible") y reemplazo `gemini-3.6-flash`. Fuente:
  https://ai.google.dev/gemini-api/docs/deprecations
  **Verificado** con `make gemini-modelos` el 2026-09-29: ni `gemini-1.5-*` ni
  `gemini-2.0-flash` aparecen entre los modelos disponibles para una key nueva.
- `make gemini-modelos` lista los modelos que la key puede usar realmente.
- **Schemas estrictos:** el endpoint acepta `strict: true` y `additionalProperties:
  false` sin cambios (verificado).
- **`thought_signature` (verificado, costó una prueba fallida):** cada tool call de
  Gemini 3.x trae `extra_content.google.thought_signature`, y si no se devuelve en la
  siguiente llamada la API responde `400 Function call is missing a
  thought_signature`. El adaptador guarda el mensaje original del asistente en
  `Message.Native` (`internal/llm/openai/openai.go:136`) y lo reenvía tal cual
  (`openai.go:219`), con `ExtraContent` como `json.RawMessage` (`openai.go:72`). Así
  se conserva también cualquier campo extra de otros proveedores compatibles.
  Cubierto por `TestReenviaExtraContent`. La documentación de Google no muestra la
  ruta JSON exacta; se sacó de una respuesta real.
- **Capa gratuita de `gemini-3.6-flash`:** 5 RPM, 250 000 TPM y 20 RPD (datos del
  usuario; el 429 confirma el de 5 RPM: `generate_content_free_tier_requests, limit:
  5`). Un turno con herramientas usa 2 o 3 requests: da para unos 2 mensajes por
  minuto y **7 a 10 mensajes por día**. Sirve para desarrollo, no para un demo en vivo
  ni para producción. Para respetarlos, ver [Límites de cuota](#límites-de-cuota).
- **503 "high demand":** Gemini devuelve 503 cuando el modelo está sobrecargado
  (visto el 2026-09-29). Es temporal; se reintenta una vez.
- **Cache:** `cache_read` dio 0 en todas las llamadas de la prueba. **[verificar]** si
  el cache implícito de Gemini necesita un prefijo más largo o no se informa por este
  endpoint.

La credencial se valida al arrancar, antes de conectar a la base, así que un
`modelo_llm` mal configurado falla de inmediato con un mensaje claro.

## Diseño

```
engine ──▶ llm.Provider (interfaz)
              ├── llm/anthropic  → SDK oficial de Anthropic
              └── llm/openai     → HTTP a /chat/completions (OpenAI, Ollama, Groq...)
```

El motor (`internal/engine/engine.go:111`) y las tools solo conocen los tipos de
`internal/llm/llm.go`: `Request`, `Message`, `ToolCall`, `ToolResult`, `Response`,
`Usage`. Cada adaptador traduce a su API. Agregar un proveedor = implementar
`Chat` y `Name`, y registrarlo en `providers.go:33`.

Dos adaptadores alcanzan porque casi todo el mercado expone la API Chat Completions
de OpenAI. Si en algún momento hace falta algo propio de un proveedor (p. ej. la API
nativa de Gemini), se agrega un tercero.

### `Message.Native`: lo que no se puede traducir

Anthropic devuelve bloques de thinking firmados, y Gemini firma cada tool call. En
los dos casos, dentro de un loop de tools hay que reenviarlos intactos. El tipo común no los modela: el adaptador guarda su mensaje
original en `Message.Native` (`llm.go:53`) y, si lo recibe de vuelta, lo usa tal cual
(`anthropic.go:143`, `openai.go:219`). Otro proveedor lo ignora y arma el mensaje con los campos
comunes. **Verificado** en `TestLoopConThinking`: la firma del thinking llega en la
segunda llamada.

`Native` no se persiste: entre turnos el historial es solo texto (ver
[conversacion-y-llm.md](conversacion-y-llm.md#historial)), así que cambiar de
proveedor a mitad de una conversación funciona.

## Diferencias entre proveedores que resuelven los adaptadores

| Tema | Anthropic | OpenAI-compatible |
|---|---|---|
| System prompt | Campo `system`, con `cache_control` | Primer mensaje con rol `system` |
| Resultados de tools | Bloques `tool_result` dentro de un mensaje `user` | Un mensaje con rol `tool` por resultado |
| Marcar un error de tool | `is_error: true` | No existe: se antepone `ERROR: ` al contenido |
| Límite de salida | `max_tokens` | `max_completion_tokens` en OpenAI, `max_tokens` en el resto (`MaxTokensField`) |
| Tools estrictas | `strict: true` | `strict: true` (Ollama lo ignora) |
| Cache | Explícito, con `cache_control` | Automático en OpenAI; se lee `cached_tokens` |
| Rechazos | `stop_reason: refusal` | `finish_reason: content_filter` |
| Thinking | Adaptativo, `effort: low`, salvo en Haiku | No se configura. `<think>...</think>` en el contenido se elimina (`openai.go:256`), porque algunos modelos locales razonan ahí |
| Reintentos | Los hace el SDK (2) | Uno ante 429/5xx (`openai.go:168`) |
| Timeout | El del SDK | 2 min; 5 min para Ollama |

Un detalle: algunos servidores compatibles devuelven `tool_calls` con
`finish_reason: "stop"`. El adaptador decide por la presencia de llamadas, no por el
`finish_reason`.

La configuración específica de Anthropic (thinking, effort, cache y fallback ante
refusal de Opus 5) vive en `anthropic.go:79` y `anthropic.go:104`; el motor ya no
sabe nada de eso.

## Límites de cuota

`internal/llm/limit/` envuelve cualquier proveedor y respeta hasta tres límites antes
de llamarlo. Se activa solo si alguna variable tiene valor:

| Variable | Qué limita | Si se alcanza |
|---|---|---|
| `LLM_LIMITE_RPM` | Requests en el último minuto | Espera a que se libere cupo |
| `LLM_LIMITE_TPM` | Tokens de **entrada** en el último minuto | Espera |
| `LLM_LIMITE_RPD` | Requests desde la medianoche de `LLM_LIMITE_ZONA_RESET` | Falla de inmediato hasta el reinicio |
| `LLM_LIMITE_ZONA_RESET` | Zona del reinicio diario. Default `America/Los_Angeles` | — |
| `LLM_LIMITE_ESPERA_MAX` | Cuánto se espera como máximo por RPM/TPM. Default `90s` | Falla |

Para la capa gratuita de Gemini:

```bash
LLM_LIMITE_RPM=5
LLM_LIMITE_TPM=250000
LLM_LIMITE_RPD=20
```

Según la documentación de Google (consultada el 2026-09-29): el RPD se reinicia a
medianoche hora del Pacífico, el TPM cuenta solo tokens de entrada y los límites son
**por proyecto y por modelo**, no por key. Por eso la clave del contador es
`proveedor/modelo`. Con Chile en UTC-3 y el Pacífico en UTC-7, el reinicio cae a las
04:00 hora de Chile.

### Cómo funciona

- **Los requests se registran en Postgres** (`llm_requests`, migración `00002`), no en
  memoria: `make chat` reinicia el proceso en cada corrida y un contador en memoria
  se reiniciaría con él. También cuenta bien con varios procesos a la vez (bot +
  chat): `Store.Reserve` (`internal/store/llm_quota.go:14`) toma un advisory lock por
  clave, lee el uso y registra el request en la misma transacción. **Verificado** con
  `TestCuotaLLMCompartidaEntreProcesos`: 10 goroutines con cupo de 3 → exactamente 3.
- **Ventana deslizante de 60 s** para RPM y TPM. `Espera` (`limit.go:175`) calcula
  cuántos de los requests más antiguos tienen que salir de la ventana y cuánto falta
  para eso, con 500 ms de margen.
- **El TPM se estima antes de la llamada** con el input del request anterior, porque
  el real se conoce recién después. Un request en curso cuenta 0 tokens hasta que
  termina.
- **Reintentos:** con límites activos, los adaptadores no reintentan (`NoRetry`) y
  el limitador reintenta una vez ante errores temporales (`llm.TemporaryError`, 5xx),
  pasando de nuevo por la cuota (`limit.go:94`). Antes, un 503 generaba dos requests
  HTTP y se contaba uno. Un 429 no se reintenta: con la cuota bien configurada no
  debería aparecer, y si aparece, reintentar no ayuda.
- **Qué ve el cliente** si se agota la cuota: "en este momento no puedo responder,
  ¿me escribes de nuevo en un rato?" (`engine.go:35`). Si una tool ya había reservado
  o cancelado, recibe la confirmación de esa acción (ver
  [conversacion-y-llm.md](conversacion-y-llm.md#casos-límite-del-loop)).
- El log muestra `cuota LLM usados_hoy=N limite_dia=M` en cada request y `esperando
  cupo de LLM espera=...` cuando espera.

**Limitación:** el contador solo conoce los requests hechos a través del bot. Si la
key se usa desde otro lado (AI Studio, curl), el bot no se entera y el proveedor
puede cortar antes.

## Qué esperar de cada opción

- **Modelos chicos locales (Ollama):** el costo por token es cero, pero seguir
  instrucciones y usar tools bien les cuesta más. Riesgos típicos: ofrecer horarios
  sin consultar, inventar precios o no llamar a `agendar`. Las reglas duras (slots,
  validación, dobles reservas) están en Go y no dependen del modelo, pero la calidad
  de la conversación sí. Además, Railway no tiene GPU: un modelo local en producción
  implica otro servidor.
- **Comparar proveedores con datos:** `messages.tokens_*` guarda el consumo por turno
  y cada línea de log incluye `llm=<proveedor/modelo>`. Se puede correr la misma
  conversación con distintos `LLM_MODEL` y comparar costo y calidad.

## Tests

| Test | Qué verifica |
|---|---|
| `llm/anthropic/anthropic_test.go` | Tools estrictas, cache, thinking/effort, fallback de Opus 5, parseo de tool_use y usage, reenvío del thinking, Haiku sin thinking |
| `llm/openai/openai_test.go` | Roles y orden de mensajes, `tool` con `ERROR:`, `max_completion_tokens`, auth, tool_calls con `finish_reason: stop`, `<think>`, errores HTTP, reenvío de `extra_content` (firma de Gemini) |
| `llm/providers/providers_test.go` | Parseo de `proveedor/modelo` (incluye modelos con barras), errores y credenciales faltantes |
| `engine/engine_integration_test.go` | Loop completo con un proveedor simulado: consultar → agendar → respuesta, contra Postgres |
| `llm/limit/limit_test.go` | Cálculo de espera por RPM/TPM, espera máxima, reinicio diario en la zona del proveedor, reintentos que cuentan en la cuota. Con reloj simulado: no espera de verdad |
| `store/store_test.go` (`TestCuotaLLM…`) | Cupo diario compartido entre procesos, contra Postgres |
