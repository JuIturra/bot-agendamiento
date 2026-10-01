// panel sirve solo el panel del operador, sin el bot: sirve para revisar una base
// (local o la de producción) sin competir con el bot de Telegram que esté corriendo.
// Escucha solo en localhost.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	_ "time/tzdata"

	"agendamiento/internal/config"
	"agendamiento/internal/llm/providers"
	"agendamiento/internal/panel"
	"agendamiento/internal/store"
	"agendamiento/internal/tenant"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load("DATABASE_URL", "PANEL_PASSWORD")
	if err != nil {
		return err
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: cfg.LogLevel}))
	tc, err := tenant.Load(cfg.TenantFile)
	if err != nil {
		return err
	}
	ctx := context.Background()
	st, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer st.Close()
	tenantID, err := st.TenantID(ctx, tc.Slug)
	if err != nil {
		return fmt.Errorf("negocio %q: %w (¿ya arrancó el bot alguna vez con esta base?)", tc.Slug, err)
	}
	zona, err := time.LoadLocation(cfg.LLMLimits.ResetZone)
	if err != nil {
		return err
	}
	// La cuota se cuenta con el nombre del proveedor ("proveedor/modelo"), igual que
	// en el servidor: se normaliza para que "claude-x" cuente como "anthropic/claude-x".
	spec := cfg.LLMModel
	if spec == "" {
		spec = tc.ModeloLLM
	}
	clave := spec
	if prov, modelo, err := providers.Parse(spec); err == nil {
		clave = prov + "/" + modelo
	}
	p, err := panel.New(panel.Config{
		Datos: st, TenantID: tenantID, Negocio: tc.Nombre, Loc: tc.Location(),
		ClaveLLM: clave, LimiteRPD: cfg.LLMLimits.RPD, ZonaReset: zona,
		Usuario: cfg.PanelUsuario, Password: cfg.PanelPassword, Log: log,
	})
	if err != nil {
		return err
	}
	addr := "127.0.0.1:" + cfg.Port
	fmt.Printf("Panel en http://%s (usuario: %s)\n", addr, usuario(cfg.PanelUsuario))
	srv := &http.Server{Addr: addr, Handler: p.Handler(), ReadHeaderTimeout: 10 * time.Second}
	return srv.ListenAndServe()
}

func usuario(u string) string {
	if u == "" {
		return "admin"
	}
	return u
}
