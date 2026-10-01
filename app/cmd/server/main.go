// Servidor del bot. En la fase 0 atiende Telegram por long polling.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	_ "time/tzdata" // la imagen distroless no trae zonas horarias

	"agendamiento/internal/bootstrap"
	"agendamiento/internal/channel/telegram"
	"agendamiento/internal/config"
	"agendamiento/internal/engine"
	"agendamiento/internal/notify"
	"agendamiento/internal/panel"
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

	// Panel del operador y /healthz, en el mismo proceso.
	srv, err := panelServer(cfg, app, bot, log)
	if err != nil {
		return err
	}
	go func() {
		log.Info("panel escuchando", "port", cfg.Port, "activo", cfg.PanelPassword != "")
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("panel", "err", err)
			stop() // sin HTTP, Railway no puede chequear la salud: mejor salir y reiniciar
		}
	}()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(ctx)
	}()

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

func panelServer(cfg config.Config, app *bootstrap.App, bot *telegram.Bot, log *slog.Logger) (*http.Server, error) {
	zona, err := time.LoadLocation(cfg.LLMLimits.ResetZone)
	if err != nil {
		return nil, err
	}
	eng := app.Engine
	p, err := panel.New(panel.Config{
		Datos: app.Store, TenantID: eng.TenantID, Negocio: eng.Tenant.Nombre, Loc: eng.Tenant.Location(),
		ClaveLLM: eng.LLM.Name(), LimiteRPD: cfg.LLMLimits.RPD, ZonaReset: zona,
		UltimoPoll: bot.UltimoPoll,
		Usuario:    cfg.PanelUsuario, Password: cfg.PanelPassword,
		Log: log.With("componente", "panel"),
	})
	if err != nil {
		return nil, err
	}
	if cfg.PanelPassword == "" {
		log.Warn("PANEL_PASSWORD vacío: el panel no se publica (solo /healthz)")
	}
	return &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           p.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
	}, nil
}
