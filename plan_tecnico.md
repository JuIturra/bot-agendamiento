# Plan técnico — roadmap, costos y riesgos

> Complementa a [plan.md](plan.md) (idea y negocio). Última actualización: 2026-09-29.
> La lógica y el funcionamiento del sistema están en [app/docs/](app/docs/README.md).
> Lo marcado **[verificar]** son supuestos que hay que confirmar antes de depender de ellos.

## Resumen

Monolito en Go + Postgres en Railway. El LLM (Claude) conversa y elige herramientas; Go
calcula disponibilidad y confirma reservas. Telegram para desarrollo, WhatsApp Cloud API
para demo y producción, Google Calendar como espejo para el dueño.
Detalle en [app/docs/arquitectura.md](app/docs/arquitectura.md).

---

## 1. Costos del LLM **[estimación, medir con tráfico real]**

Supuesto: una conversación típica ("¿tienen hora mañana?" → opciones → elige → confirma)
son ~4 turnos del cliente y ~8 llamadas al LLM contando el loop de tools. Cada llamada
lleva ~4 000 tokens de entrada (tools + system + historial) y produce ~300 de salida
(incluye thinking). En total: **~32 000 tokens de entrada y ~2 400 de salida**, sin
cache.

Precios de lista de Anthropic (USD por millón de tokens, entrada / salida):

| Modelo | Precio | Costo por conversación | 200 conv/mes | 500 conv/mes |
|---|---|---|---|---|
| `claude-opus-5` | $5 / $25 | ~$0.22 | ~$44 | ~$110 |
| `claude-sonnet-5` | $2 / $10 | ~$0.09 | ~$18 | ~$44 |
| `claude-haiku-4-5` | $1 / $5 | ~$0.04 | ~$9 | ~$22 |

Con el cache funcionando, la entrada repetida se cobra mucho más barata y estos números
bajan bastante. Aun así, **el punto clave es este**: 25 000 CLP son unos 26 USD
**[verificar tipo de cambio]**. Con Opus 5 sin cache, un negocio con 200 conversaciones
al mes ya cuesta más de lo que paga. Opciones:

- Elegir el modelo por tenant según el volumen del negocio. Desde el 2026-09-29 el
  proveedor también es intercambiable (`anthropic/…`, `openai/…`, `ollama/…` o
  cualquier API compatible con OpenAI), así que la comparación puede incluir
  modelos de otros proveedores ([proveedores-llm.md](app/docs/proveedores-llm.md)).
- Poner un tope de conversaciones por plan (p. ej. básico = 150/mes).
- Medir la calidad de cada modelo con conversaciones reales antes de fijar precios.

---

## 2. Riesgos

| Riesgo | Mitigación |
|---|---|
| El costo del LLM se come el margen | Modelo por tenant, cache, tope por plan, medir tokens desde el día 1 |
| El bot confirma algo que no existe | Solo `agendar` confirma ([conversacion-y-llm.md](app/docs/conversacion-y-llm.md)) |
| Doble reserva | Restricción `EXCLUDE` en Postgres ([modelo-datos.md](app/docs/modelo-datos.md)) |
| Prompt injection | Tools limitadas al cliente y al tenant actuales; los IDs salen del contexto, no del modelo |
| Política o precios de WhatsApp | [verificar] antes de la fase 2 ([canales.md](app/docs/canales.md)) |
| **Ley 21.719 de protección de datos (Chile)** | [verificar] fecha de vigencia y obligaciones. Clínicas dentales y kinesiólogos manejan datos de salud (sensibles). Minimizar lo que se guarda, retención corta, contrato de encargado de tratamiento con cada negocio |
| Capa gratuita de Railway | [verificar] condiciones actuales; presupuestar el plan pago mínimo desde el primer cliente |
| El dueño responde a mano y el bot también | Estado `humano` en la conversación; el bot se pausa |

---

## 3. MVP por fases

### Fase 0 — Núcleo en local con Telegram ✅ código listo (2026-09-29)

- [x] Esqueleto Go, Postgres local (docker compose), migraciones.
- [x] Un tenant demo cargado desde `app/tenants/barberia-demo.yaml`.
- [x] Adaptador Telegram con long polling.
- [x] `engine` con el loop manual de tools y el historial en Postgres.
- [x] `booking` con cálculo de slots y tests (sin Google Calendar todavía).
- [x] Tools: `consultar_disponibilidad`, `agendar`, `mis_reservas`, `cancelar_reserva`.
- [x] Log de tokens por mensaje.
- [x] Extra: `make chat`, para conversar desde la terminal sin Telegram.
- [x] Primera conversación real contra un modelo: Gemini 3.6 Flash (2026-09-29).
  Flujo completo OK. Destapó un bug (el cliente no se enteraba de una reserva hecha
  si el modelo fallaba después), ya corregido.
- [x] Bot de Telegram probado desde el teléfono (2026-09-30): saludo, servicios,
  disponibilidad y reserva real, con Gemini.
- [ ] Primera conversación real con Claude (falta `ANTHROPIC_API_KEY`).
- [ ] Medir costo real por conversación con `messages.tokens_*` y actualizar §1.
  Primer dato (Gemini, 5 turnos: precio → disponibilidad → reserva → consulta →
  cancelación): **~19 600 tokens de entrada y ~450 de salida**, contra los ~32 000 /
  2 400 estimados. Falta convertirlo a CLP con el precio vigente del modelo
  [verificar] y repetirlo con otros modelos.

**Listo cuando:** se puede preguntar precios, pedir hora, reservar, consultar y cancelar
por Telegram, y dos reservas simultáneas al mismo horario no pasan. **Cumplido:** las
reservas simultáneas están cubiertas por un test de integración; precios, horas y
reserva se probaron por Telegram, y consultar y cancelar por terminal, los dos con
Gemini. Falta probar consultar y cancelar desde Telegram.

### Fase 1 — Integraciones reales (objetivo: usable por un negocio)

- [x] Google Calendar (freebusy + crear y borrar eventos), con reintentos
  (2026-09-30). Verificado con un calendario real desde Telegram: reserva → evento,
  bloqueo manual respetado, cancelación → evento borrado.
- [ ] Leer bloqueos de varios calendarios (negocio + personal del dueño): en la
  prueba real, un bloqueo anotado en el calendario personal fue invisible para el bot.
- [x] Avisos al dueño por Telegram de reservas y cancelaciones (2026-09-30). El
  dueño se registra con `/dueno <código>`. Verificado desde Telegram: registro y aviso
  de reserva.
- [ ] `derivar_a_humano` y el estado `humano`.
- [ ] Inbox con debounce, dedupe y lock por conversación.
- [x] Deploy en Railway (Postgres gestionado), 2026-09-30. Se usa long polling en
  vez de webhook mientras haya una sola réplica (ver [deploy.md](app/docs/deploy.md)).
- [ ] Decidir always-on vs. webhook + Serverless: con long polling, el apagado por
  inactividad de Railway no aplica, porque el bot nunca queda inactivo, y si se
  durmiera, no despertaría con los mensajes.
- [ ] Job de purga y retención.

**Listo cuando:** el bot corre 24/7 en Railway, las reservas aparecen en el Google
Calendar del dueño y este recibe un aviso por cada una.

### Fase 2 — WhatsApp y demo de alto impacto

- [ ] Adaptador WhatsApp Cloud API (webhook, firma, envío).
- [ ] Plantilla aprobada para recordatorios y job de recordatorio del día anterior.
- [ ] 2–3 tenants demo (barbería, taller, veterinaria) con configs creíbles.
- [ ] Guion de demo: el prospecto escribe al número desde su propio teléfono y ve el
      evento aparecer en un Google Calendar proyectado en pantalla.

### Fase 3 — Operar varios clientes

- [ ] Panel mínimo (o edición de YAML + comando de carga) para configurar un negocio
      nuevo en menos de 30 minutos.
- [ ] Resumen diario al dueño.
- [ ] Métricas por tenant: conversaciones, reservas, costo LLM y tasa de conversión (el
      argumento de venta para renovar).
- [ ] Varios profesionales o recursos por negocio.

---

## 4. Decisiones pendientes

1. **Modelo y proveedor por defecto**: definir con una prueba de calidad y costo
   usando conversaciones reales de la fase 0. Se puede correr el mismo guion con
   distintos `LLM_MODEL` y comparar `messages.tokens_*`.
2. **Precio y topes por plan**: dependen del punto 1.
3. **Primer nicho para la demo**: recomiendo barbería o peluquería. Tienen agenda simple
   (duración fija y un recurso) y muchas consultas fuera de horario. Los talleres
   requieren cotización, lo que es más difícil de automatizar.
