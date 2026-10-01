# Deploy (Docker y Railway)

> **Estado:** imagen Docker verificada en local el 2026-09-30: construye, arranca con
> base + Calendar + Telegram en contenedores y se apaga limpio con SIGTERM. **Railway:
> configurado pero todavía no desplegado.** Última actualización: 2026-09-30.

## Imagen

`Dockerfile` en `app/` (contexto de build = `app/`):

- Build multi-stage con `golang:1.26` y binario estático (`CGO_ENABLED=0`).
- Imagen final `gcr.io/distroless/static-debian12:nonroot`: certificados CA, usuario
  sin privilegios y nada más (ni shell). Pesa **52 MB**; el grueso es el cliente de
  Google.
- Las zonas horarias van dentro del binario (`import _ "time/tzdata"` en
  `cmd/server/main.go`), porque distroless no trae tzdata.
- Las migraciones van embebidas y se aplican al arrancar.
- Se copian `tenants/` (los YAML de negocios). **No** se copian `.env`, `secrets/`,
  `bin/`, `docs/` ni tests (`.dockerignore`). Verificado listando la imagen: solo
  `app/server` y `app/tenants/`.

## Local con Docker

| Comando | Qué hace |
|---|---|
| `make docker-build` | Construye la imagen, la misma que usa Railway |
| `make docker-up` | Levanta base + bot en contenedores (perfil `bot` de `docker-compose.yml`) |
| `make docker-logs` | Sigue los logs del bot |

En compose, el bot toma `.env`, pero `DATABASE_URL` apunta a `db:5432` (la red interna)
y el JSON de Google se monta en `/secrets` como solo lectura. `make db-up` sigue
levantando solo la base, para desarrollar con `make run` / `make chat`.

**Ojo: un mismo bot de Telegram no puede correr en dos lugares a la vez.** Si el bot
corre en Docker, en `make run` o en Railway al mismo tiempo, Telegram responde 409 a
uno de ellos (ver [canales.md](canales.md#telegram-implementado)).

## Railway

Archivo `railway.json` (en `app/`): builder `DOCKERFILE`, 1 réplica, reinicio
`ON_FAILURE` con hasta 10 reintentos.

**Por qué 1 réplica:** el bot usa long polling y Telegram solo admite un lector por
bot. Con más réplicas, habría que pasar a webhook (pendiente para cuando llegue
WhatsApp, que lo exige).

**Por qué long polling y no webhook:** con una instancia funciona igual, y no necesita
dominio público, validación de firma ni la cola `inbox`. Durante un redeploy pueden
convivir unos segundos la versión vieja y la nueva: la nueva recibe 409 y reintenta
cada 3 s hasta que la vieja se apaga. **[verificar]** cuánto dura ese solapamiento en
Railway.

### Configuración del servicio

1. **New Project → Deploy from GitHub repo** → elegir el repositorio.
2. En el servicio: **Settings → Root Directory = `/app`**.
3. **Settings → Config-as-code → `/app/railway.json`**. La documentación de Railway no
   aclara si lo encuentra solo cuando el código está en una subcarpeta, así que se
   indica la ruta explícita (consultado el 2026-09-30).
4. **New → Database → PostgreSQL** en el mismo proyecto.
5. Variables del servicio del bot:

| Variable | Valor |
|---|---|
| `DATABASE_URL` | `${{Postgres.DATABASE_URL}}` (referencia a la base del proyecto) |
| `TELEGRAM_BOT_TOKEN` | Token del bot de producción |
| `LLM_MODEL` | p. ej. `gemini/gemini-3.6-flash` |
| `GEMINI_API_KEY` (o la del proveedor elegido) | |
| `LLM_LIMITE_RPM`, `LLM_LIMITE_TPM`, `LLM_LIMITE_RPD` | Si se usa una capa gratuita |
| `GOOGLE_CREDENTIALS_JSON` | **El contenido** del JSON de la cuenta de servicio (Railway no permite subir archivos) |
| `GOOGLE_CALENDAR_ID` | Calendario del negocio |
| `CODIGO_DUENO` | Código para `/dueno` |

6. Deploy. En los logs debería aparecer `bot de Telegram escuchando`.

**[verificar]** en el primer deploy:
- que la migración `00001` pueda hacer `CREATE EXTENSION btree_gist` en el Postgres
  de Railway. Su documentación dice que no agregan extensiones a la plantilla;
  `btree_gist` viene con Postgres, pero requiere permisos. Si falla, el bot no
  arranca y el error queda en el log;
- precios y límites de uso vigentes de Railway.

### Después del primer deploy

- **La base de Railway arranca vacía.** El dueño tiene que registrarse de nuevo con
  `/dueno <código>`. Las reservas hechas en local no existen allá.
- **La cuota de Gemini es por proyecto de Google, no por base.** Si el bot corre en
  local y en Railway con la misma key, cada uno cuenta sus requests en su propia base
  y entre los dos pueden pasar el límite real. Mejor no usar la misma key en los dos.
