// Servidor del bot. En la fase 0 atiende Telegram por long polling.
package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	_ "time/tzdata" // la imagen distroless no trae zonas horarias

	"agendamiento/internal/bootstrap"
	"agendamiento/internal/channel/telegram"
	"agendamiento/internal/config"
	"agendamiento/internal/engine"
	"agendamiento/internal/notify"
)

func main() {
	if err := run(); err != nil && !errors.Is(err, context.Canceled) {
		slog.Error("fin", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load("DATABASE_URL", "TELEGRAM_BOT_TOKEN")
	if err != nil {
		return err
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	app, err := bootstrap.Build(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer app.Close()
	eng := app.Engine

	bot := telegram.New(cfg.TelegramBotToken, log)
	username, err := bot.Me(ctx)
	if err != nil {
		return err
	}
	log.Info("bot de Telegram escuchando", "bot", "@"+username)

	// Avisos al dueño de cada reserva y cancelación.
	notifier := &notify.Notifier{Store: app.Store, Sender: bot, Log: log.With("tenant", eng.Tenant.Slug)}
	app.OnCambio(notifier.Kick)
	go notifier.Run(ctx)
	registro := &notify.Registro{
		Store: app.Store, TenantID: eng.TenantID, Negocio: eng.Tenant.Nombre,
		Codigo: cfg.CodigoDueno, Log: log,
	}
	if cfg.CodigoDueno == "" {
		log.Warn("CODIGO_DUENO vacío: el dueño no puede registrarse para recibir avisos")
	}

	return bot.Run(ctx, func(ctx context.Context, m telegram.Message) (string, error) {
		// "/dueno <código>" no pasa por el modelo.
		if resp, ok, err := registro.Handle(ctx, m.ChatID, m.Texto); ok {
			return resp, err
		}
		return eng.Handle(ctx, engine.Inbound{
			Canal:          "telegram",
			ExternalUserID: strconv.FormatInt(m.UserID, 10),
			Nombre:         m.Nombre,
			Texto:          m.Texto,
			Enviado:        m.Enviado,
		})
	})
}
