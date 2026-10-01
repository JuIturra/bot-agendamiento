package booking

import (
	"errors"
	"testing"
	"time"

	"agendamiento/internal/tenant"
)

const cfgYAML = `
slug: test
nombre: Test
zona_horaria: America/Santiago
horario:
  martes: ["10:00-14:00", "15:00-20:00"]
  sabado: ["10:00-13:00"]
intervalo_minutos: 30
buffer_minutos: 10
anticipacion_minima_minutos: 60
dias_maximos_adelanto: 21
feriados: ["2026-10-13"]
servicios:
  - {nombre: Corte, duracion_minutos: 30}
  - {nombre: Largo, duracion_minutos: 60}
`

func testCfg(t *testing.T) *tenant.Config {
	t.Helper()
	c, err := tenant.Parse([]byte(cfgYAML))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func at(t *testing.T, c *tenant.Config, s string) time.Time {
	t.Helper()
	v, err := time.ParseInLocation("2006-01-02 15:04", s, c.Location())
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func horas(ts []time.Time) []string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = t.Format("01-02 15:04")
	}
	return out
}

func TestSlots(t *testing.T) {
	c := testCfg(t)
	srv := func(n string) tenant.Servicio { s, _ := c.Servicio(n); return s }
	// Martes 2026-10-06; "ahora" es el lunes anterior, sin efecto de anticipación.
	lunes := at(t, c, "2026-10-05 09:00")

	casos := []struct {
		nombre          string
		q               Query
		cantidad        int
		primero, ultimo string // formato "01-02 15:04"; vacío = no se revisa
	}{
		{
			nombre:   "día completo, servicio de 30",
			q:        Query{Servicio: srv("Corte"), Desde: at(t, c, "2026-10-06 00:00"), Hasta: at(t, c, "2026-10-06 00:00"), Now: lunes},
			cantidad: 8 + 10, primero: "10-06 10:00", ultimo: "10-06 19:30",
		},
		{
			nombre: "servicio de 60 no cruza la pausa de almuerzo",
			q:      Query{Servicio: srv("Largo"), Desde: at(t, c, "2026-10-06 00:00"), Hasta: at(t, c, "2026-10-06 00:00"), Now: lunes},
			// mañana 10:00-13:00 (7) + tarde 15:00-19:00 (9); 13:30 no cabe
			cantidad: 16, primero: "10-06 10:00", ultimo: "10-06 19:00",
		},
		{
			nombre: "franja tarde",
			q:      Query{Servicio: srv("Corte"), Desde: at(t, c, "2026-10-06 00:00"), Hasta: at(t, c, "2026-10-06 00:00"), Franja: FranjaTarde, Now: lunes},
			// la tarde empieza a las 13:00, aunque se atienda hasta las 14:00
			cantidad: 12, primero: "10-06 13:00",
		},
		{
			nombre:   "día cerrado",
			q:        Query{Servicio: srv("Corte"), Desde: at(t, c, "2026-10-05 00:00"), Hasta: at(t, c, "2026-10-05 00:00"), Now: lunes},
			cantidad: 0,
		},
		{
			nombre:   "feriado",
			q:        Query{Servicio: srv("Corte"), Desde: at(t, c, "2026-10-13 00:00"), Hasta: at(t, c, "2026-10-13 00:00"), Now: lunes},
			cantidad: 0,
		},
		{
			nombre: "anticipación mínima",
			q:      Query{Servicio: srv("Corte"), Desde: at(t, c, "2026-10-06 00:00"), Hasta: at(t, c, "2026-10-06 00:00"), Franja: FranjaManana, Now: at(t, c, "2026-10-06 10:15")},
			// 10:15 + 60 min = 11:15, el siguiente slot es 11:30
			cantidad: 3, primero: "10-06 11:30",
		},
		{
			nombre:   "más allá del máximo de días",
			q:        Query{Servicio: srv("Corte"), Desde: at(t, c, "2026-11-03 00:00"), Hasta: at(t, c, "2026-11-03 00:00"), Now: lunes},
			cantidad: 0,
		},
		{
			nombre: "rango de varios días: martes y sábado",
			q:      Query{Servicio: srv("Corte"), Desde: at(t, c, "2026-10-06 00:00"), Hasta: at(t, c, "2026-10-10 00:00"), Now: lunes},
			// 18 del martes + 6 del sábado
			cantidad: 24, primero: "10-06 10:00", ultimo: "10-10 12:30",
		},
	}
	for _, tc := range casos {
		t.Run(tc.nombre, func(t *testing.T) {
			got := horas(Slots(c, tc.q))
			if len(got) != tc.cantidad {
				t.Fatalf("cantidad = %d, want %d: %v", len(got), tc.cantidad, got)
			}
			if tc.primero != "" && got[0] != tc.primero {
				t.Errorf("primero = %s, want %s", got[0], tc.primero)
			}
			if tc.ultimo != "" && got[len(got)-1] != tc.ultimo {
				t.Errorf("último = %s, want %s", got[len(got)-1], tc.ultimo)
			}
		})
	}
}

func TestSlotsRespetaOcupadoYBuffer(t *testing.T) {
	c := testCfg(t)
	corte, _ := c.Servicio("Corte")
	ocupado := []Interval{{Start: at(t, c, "2026-10-06 11:00"), End: at(t, c, "2026-10-06 11:30")}}
	got := horas(Slots(c, Query{
		Servicio: corte, Desde: at(t, c, "2026-10-06 00:00"), Hasta: at(t, c, "2026-10-06 00:00"),
		Franja: FranjaManana, Now: at(t, c, "2026-10-05 09:00"), Ocupado: ocupado,
	}))
	// 10:30 termina a las 11:00 y necesita 10 min de buffer: choca.
	// 11:30 empieza justo al terminar la reserva, sin buffer: choca.
	want := []string{"10-06 10:00", "10-06 12:00", "10-06 12:30"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

// En Chile el horario de verano empieza a medianoche: el domingo 2026-09-06 la
// medianoche local no existe (00:00 pasa a 01:00). Una iteración por medianoches se
// desfasa ese día y corta el rango antes de tiempo.
func TestSlotsCambioDeHorario(t *testing.T) {
	c := testCfg(t)
	corte, _ := c.Servicio("Corte")
	now := at(t, c, "2026-09-01 09:00")
	got := Slots(c, Query{
		Servicio: corte, Desde: at(t, c, "2026-09-05 00:00"), Hasta: at(t, c, "2026-09-08 00:00"), Now: now,
	})
	// Sábado 5: 10:00-13:00 = 6 slots. Martes 8: 18 slots.
	if len(got) != 24 {
		t.Fatalf("cantidad = %d, want 24: %v", len(got), horas(got))
	}
	primeroMartes := got[6]
	if h := primeroMartes.Format("2006-01-02 15:04 -0700"); h != "2026-09-08 10:00 -0300" {
		t.Errorf("primer slot del martes = %s, want 2026-09-08 10:00 -0300 (horario de verano)", h)
	}
	if h := got[0].Format("-0700"); h != "-0400" {
		t.Errorf("el sábado 5 todavía es horario de invierno, offset = %s", h)
	}
}

func TestValidar(t *testing.T) {
	c := testCfg(t)
	corte, _ := c.Servicio("Corte")
	now := at(t, c, "2026-10-05 09:00")
	ocupado := []Interval{{Start: at(t, c, "2026-10-06 11:00"), End: at(t, c, "2026-10-06 11:30")}}
	casos := map[string]struct {
		inicio string
		want   error
	}{
		"válido":           {"2026-10-06 10:00", nil},
		"día cerrado":      {"2026-10-07 10:00", ErrFueraDeHorario},
		"hora desalineada": {"2026-10-06 10:15", ErrFueraDeHorario},
		"cruza almuerzo":   {"2026-10-06 13:45", ErrFueraDeHorario},
		"ocupado":          {"2026-10-06 11:00", ErrOcupado},
		"buffer":           {"2026-10-06 11:30", ErrOcupado},
		"muy pronto":       {"2026-10-05 09:30", ErrMuyPronto},
		"muy lejos":        {"2026-11-03 10:00", ErrMuyLejos},
	}
	for nombre, tc := range casos {
		err := Validar(c, corte, at(t, c, tc.inicio), now, ocupado)
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", nombre, err, tc.want)
		}
	}
}
