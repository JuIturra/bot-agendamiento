// Package bootstrap arma las dependencias comunes a los ejecutables.
package bootstrap

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"agendamiento/internal/booking"
	"agendamiento/internal/calendar"
	"agendamiento/internal/config"
	"agendamiento/internal/engine"
	"agendamiento/internal/llm"
	"agendamiento/internal/llm/limit"
	"agendamiento/internal/llm/providers"
	"agendamiento/internal/store"
	"agendamiento/internal/tenant"
	"agendamiento/internal/tools"
)

// ModeloPorDefecto se usa si ni el negocio ni LLM_MODEL indican uno.
const ModeloPorDefecto = "anthropic/claude-opus-5"

// App agrupa lo que arma bootstrap.
type App struct {
	Engine *engine.Engine
	Store  *store.Store
	// Syncer es nil si el negocio no tiene Google Calendar configurado.
	Syncer *calendar.Syncer

	mu       sync.Mutex
	alCambio []func()
}

// OnCambio registra una función que se llama después de cada reserva o cancelación
// (p. ej. para que un proceso en segundo plano barra de inmediato).
func (a *App) OnCambio(f func()) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.alCambio = append(a.alCambio, f)
}

func (a *App) cambio() {
	a.mu.Lock()
	fs := append([]func(){}, a.alCambio...)
	a.mu.Unlock()
	for _, f := range fs {
		f()
	}
}

// Close hace un último barrido de sincronización con Calendar (para que una reserva
// hecha justo antes de salir no quede sin evento hasta el próximo arranque) y cierra
// la base.
func (a *App) Close() {
	if a.Syncer != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if _, err := a.Syncer.SyncOnce(ctx); err != nil {
			a.Engine.Log.Warn("último sync de calendar", "err", err)
		}
	}
	a.Store.Close()
}

// Build conecta a la base, aplica migraciones, carga el negocio y devuelve el motor
// listo para usar. Si el negocio tiene Google Calendar, arranca el sincronizador en
// segundo plano (se detiene con ctx). El llamador debe llamar a App.Close.
func Build(ctx context.Context, cfg config.Config, log *slog.Logger) (*App, error) {
	tc, err := tenant.Load(cfg.TenantFile)
	if err != nil {
		return nil, err
	}
	switch {
	case cfg.LLMModel != "":
		tc.ModeloLLM = cfg.LLMModel
	case tc.ModeloLLM == "":
		tc.ModeloLLM = ModeloPorDefecto
	}

	// El proveedor se crea antes de tocar la base: si falta una credencial, falla
	// rápido y con un mensaje claro.
	var provider llm.Provider
	provider, err = providers.New(tc.ModeloLLM, providers.Credentials{
		AnthropicAPIKey: cfg.AnthropicAPIKey,
		OpenAIAPIKey:    cfg.OpenAIAPIKey,
		GeminiAPIKey:    cfg.GeminiAPIKey,
		BaseURL:         cfg.LLMBaseURL,
		APIKey:          cfg.LLMAPIKey,
		// Con límites de cuota, reintenta el limitador para contar cada request.
		NoRetry: cfg.LLMLimits.RPM > 0 || cfg.LLMLimits.TPM > 0 || cfg.LLMLimits.RPD > 0,
	})
	if err != nil {
		return nil, fmt.Errorf("modelo_llm %q: %w", tc.ModeloLLM, err)
	}

	if cfg.GoogleCalendarID != "" {
		tc.GoogleCalendarID = cfg.GoogleCalendarID
	}
	// Calendar también se valida antes de tocar la base.
	var cal *calendar.Client
	if tc.GoogleCalendarID != "" {
		creds, err := cfg.GoogleCredentials()
		if err != nil {
			return nil, err
		}
		if creds == nil {
			return nil, fmt.Errorf("el negocio tiene google_calendar_id pero faltan las credenciales de Google: GOOGLE_APPLICATION_CREDENTIALS (ruta al JSON) o GOOGLE_CREDENTIALS_JSON (el JSON)")
		}
		if cal, err = calendar.New(ctx, creds); err != nil {
			return nil, err
		}
	}

	st, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return nil, err
	}
	tenantID, err := st.UpsertTenant(ctx, tc)
	if err != nil {
		st.Close()
		return nil, fmt.Errorf("guardando negocio: %w", err)
	}

	if l := cfg.LLMLimits; l.RPM > 0 || l.TPM > 0 || l.RPD > 0 {
		zona, err := time.LoadLocation(l.ResetZone)
		if err != nil {
			st.Close()
			return nil, fmt.Errorf("LLM_LIMITE_ZONA_RESET: %w", err)
		}
		provider = limit.Wrap(provider, limit.Limits{
			RPM: l.RPM, TPM: l.TPM, RPD: l.RPD, ResetZone: zona, MaxWait: l.MaxWait,
		}, st, log)
		log.Info("límites de LLM activos", "rpm", l.RPM, "tpm", l.TPM, "rpd", l.RPD, "reset", l.ResetZone)
	}

	app := &App{Store: st}
	svc := &booking.Service{Cfg: tc, TenantID: tenantID, Store: st, Now: time.Now, Log: log, AlCambiar: app.cambio}
	if cal != nil {
		calID := tc.GoogleCalendarID
		svc.Externos = func(ctx context.Context, desde, hasta time.Time) ([]booking.Interval, error) {
			return cal.Busy(ctx, calID, desde, hasta)
		}
		app.Syncer = &calendar.Syncer{Calendar: cal, Store: st, Log: log.With("tenant", tc.Slug)}
		app.OnCambio(app.Syncer.Kick)
		go app.Syncer.Run(ctx)
		log.Info("google calendar activo", "calendar", calID)
	}
	e := &engine.Engine{
		LLM:      provider,
		Store:    st,
		Tenant:   tc,
		TenantID: tenantID,
		Tools:    &tools.Toolset{Booking: svc},
		Log:      log.With("tenant", tc.Slug),
	}
	e.Init()
	app.Engine = e
	log.Info("negocio cargado", "tenant", tc.Slug, "llm", provider.Name())
	return app, nil
}
