# Bot de agendamiento — centro de conocimiento

Asistente de WhatsApp + IA que responde y agenda para negocios tradicionales.

## Estructura

```
bot_agendamiento/
├── README.md          ← este índice
├── CLAUDE.md          ← instrucciones para sesiones de Claude Code
├── plan.md            ← idea, nichos y modelo de negocio
├── plan_tecnico.md    ← roadmap, costos, riesgos y decisiones pendientes
└── app/               ← código del proyecto (Go); ver app/README.md para correrlo
    └── docs/          ← lógica y funcionamiento del sistema
```

## Índice

### Negocio y planificación (raíz)

| Documento | Tema |
|---|---|
| [plan.md](plan.md) | Idea, nichos objetivo, modelo de cobro, estrategia de demo |
| [plan_tecnico.md](plan_tecnico.md) | Costos del LLM, riesgos, MVP por fases, decisiones pendientes |

### Funcionamiento del sistema (`app/docs/`)

Ver [app/docs/README.md](app/docs/README.md).

## Qué va dónde

- **Raíz**: por qué y qué construir. Negocio, clientes, precios, roadmap, decisiones,
  investigación de proveedores, riesgos.
- **`app/docs/`**: cómo funciona lo construido. Arquitectura, flujos, modelo de datos,
  integraciones, trampas del código.

Si dudas: si un cambio en el código lo dejaría desactualizado, va en `app/docs/`.
