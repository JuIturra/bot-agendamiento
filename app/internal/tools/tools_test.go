package tools

import (
	"encoding/json"
	"slices"
	"testing"

	"agendamiento/internal/booking"
	"agendamiento/internal/tenant"
)

func TestDefinitionsEstrictas(t *testing.T) {
	cfg, err := tenant.Load("../../tenants/barberia-demo.yaml")
	if err != nil {
		t.Fatal(err)
	}
	ts := &Toolset{Booking: &booking.Service{Cfg: cfg}}
	a, _ := json.Marshal(ts.Definitions())
	b, _ := json.Marshal(ts.Definitions())
	if string(a) != string(b) {
		t.Fatal("las definiciones deben ser idénticas entre llamadas (cache de prompts)")
	}
	for _, d := range ts.Definitions() {
		if !d.Strict {
			t.Errorf("%s: falta Strict", d.Name)
		}
		if d.Schema["type"] != "object" || d.Schema["additionalProperties"] != false {
			t.Errorf("%s: schema no estricto: %v", d.Name, d.Schema)
		}
		props := d.Schema["properties"].(map[string]any)
		if req := d.Schema["required"].([]string); len(req) != len(props) {
			t.Errorf("%s: todas las propiedades deben ser obligatorias", d.Name)
		}
		if d.Name == "agendar" {
			enum := props["servicio"].(map[string]any)["enum"].([]string)
			if !slices.Contains(enum, "Corte de pelo") {
				t.Error("el enum de servicios debería salir del negocio")
			}
		}
	}
}
