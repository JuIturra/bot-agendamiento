# Documentación técnica — cómo funciona el sistema

Índice de la lógica y el funcionamiento del código en `app/`. Para correrlo, ver
[app/README.md](../README.md). Para negocio, roadmap y
decisiones abiertas, ver el [README de la raíz](../../README.md).

| Documento | Tema |
|---|---|
| [arquitectura.md](arquitectura.md) | Principios, diagrama, flujo de un mensaje, stack, ejecutables, trampas, tests |
| [reglas-agenda.md](reglas-agenda.md) | Cómo se calculan los horarios libres y cuándo se acepta una reserva |
| [modelo-datos.md](modelo-datos.md) | Tablas, prevención de dobles reservas, fuente de verdad, retención |
| [proveedores-llm.md](proveedores-llm.md) | Cambiar de modelo o proveedor (Anthropic, OpenAI, Gemini, Ollama, compatibles), qué hace cada adaptador y límites de cuota |
| [conversacion-y-llm.md](conversacion-y-llm.md) | Tools, system prompt, historial, parámetros del modelo, cache, casos límite, prompt injection |
| [canales.md](canales.md) | Interfaz de canal, Telegram, WhatsApp Cloud API |
| [avisos-dueno.md](avisos-dueno.md) | Avisos al dueño por Telegram: registro con `/dueno <código>`, qué se avisa y cómo no se pierden |
| [google-calendar.md](google-calendar.md) | Configuración paso a paso, bloqueos del dueño (freebusy), eventos de reservas, sincronización con reintentos |

## Convenciones

- Un documento por tema. Si el tema ya existe, se complementa en lugar de crear otro.
- Cada documento indica su **estado** (diseño / implementado) y su fecha de última
  actualización.
- Lo implementado se cita con `archivo.go:línea`. Lo no verificado se marca **[verificar]**.
- No se documenta lo que el código o git ya dicen por sí solos.
