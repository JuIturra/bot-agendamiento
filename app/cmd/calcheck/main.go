// calcheck verifica la configuración de Google Calendar: muestra con qué email hay
// que compartir el calendario y prueba leer los ocupados de la próxima semana.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	_ "time/tzdata"

	"agendamiento/internal/calendar"
	"agendamiento/internal/config"
	"agendamiento/internal/tenant"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "✗", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	raw, err := cfg.GoogleCredentials()
	if err != nil {
		return err
	}
	if raw == nil {
		return fmt.Errorf("faltan GOOGLE_APPLICATION_CREDENTIALS (ruta al JSON) o GOOGLE_CREDENTIALS_JSON (el JSON)")
	}
	var sa struct {
		Type        string `json:"type"`
		ClientEmail string `json:"client_email"`
	}
	if err := json.Unmarshal(raw, &sa); err != nil || sa.Type != "service_account" {
		return fmt.Errorf("las credenciales no parecen el JSON de una cuenta de servicio (type = %q)", sa.Type)
	}
	fmt.Println("✓ cuenta de servicio:", sa.ClientEmail)
	fmt.Println("  El calendario tiene que estar compartido con ese email, con permiso")
	fmt.Println("  \"Hacer cambios en eventos\".")

	calID := cfg.GoogleCalendarID
	tc, err := tenant.Load(cfg.TenantFile)
	if err != nil {
		return err
	}
	if calID == "" {
		calID = tc.GoogleCalendarID
	}
	if calID == "" {
		return fmt.Errorf("falta el calendario: GOOGLE_CALENDAR_ID o google_calendar_id en %s", cfg.TenantFile)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cal, err := calendar.New(ctx, raw)
	if err != nil {
		return err
	}
	now := time.Now()
	busy, err := cal.Busy(ctx, calID, now, now.AddDate(0, 0, 7))
	if err != nil {
		return err
	}
	fmt.Printf("✓ calendario %s accesible: %d tramos ocupados en los próximos 7 días\n", calID, len(busy))
	for _, b := range busy {
		fmt.Printf("  - %s → %s\n", b.Start.In(tc.Location()).Format("Mon 02-01 15:04"), b.End.In(tc.Location()).Format("15:04"))
	}
	return nil
}
