package booking

import (
	"context"
	"errors"
	"testing"
	"time"
)

// memStore es un Store mínimo en memoria.
type memStore struct{ ocupado []Interval }

func (m *memStore) Ocupados(context.Context, string, time.Time, time.Time) ([]Interval, error) {
	return m.ocupado, nil
}
func (m *memStore) CrearReserva(_ context.Context, r NuevaReserva) (Reserva, error) {
	return Reserva{ID: "r1", Servicio: r.Servicio, Inicio: r.Inicio, Fin: r.Fin}, nil
}
func (m *memStore) ReservasFuturas(context.Context, string, string, time.Time) ([]Reserva, error) {
	return nil, nil
}
func (m *memStore) CancelarReserva(context.Context, string, string, string) (Reserva, error) {
	return Reserva{}, nil
}

func TestExternosYAlCambiar(t *testing.T) {
	c := testCfg(t)
	now := at(t, c, "2026-10-05 09:00")
	cambios := 0
	s := &Service{
		Cfg: c, Store: &memStore{}, Now: func() time.Time { return now },
		// El dueño bloqueó 10:00-12:00 en su calendario.
		Externos: func(context.Context, time.Time, time.Time) ([]Interval, error) {
			return []Interval{{Start: at(t, c, "2026-10-06 10:00"), End: at(t, c, "2026-10-06 12:00")}}, nil
		},
		AlCambiar: func() { cambios++ },
	}
	martes := at(t, c, "2026-10-06 00:00")
	slots, err := s.Disponibilidad(context.Background(), "Corte", martes, martes, FranjaManana)
	if err != nil {
		t.Fatal(err)
	}
	// 12:00 choca con el buffer de 10 min del bloqueo; quedan 12:30, 13:00 no es mañana.
	if got := horas(slots); len(got) != 1 || got[0] != "10-06 12:30" {
		t.Errorf("slots = %v", got)
	}
	if _, err := s.Agendar(context.Background(), "c1", "Corte", at(t, c, "2026-10-06 10:30"), "Ana"); !errors.Is(err, ErrOcupado) {
		t.Errorf("agendar sobre un bloqueo manual: err = %v", err)
	}
	if _, err := s.Agendar(context.Background(), "c1", "Corte", at(t, c, "2026-10-06 12:30"), "Ana"); err != nil {
		t.Fatal(err)
	}
	if cambios != 1 {
		t.Errorf("AlCambiar se llamó %d veces, se esperaba 1 (solo la reserva exitosa)", cambios)
	}

	// Si Calendar no responde, se sigue calculando con las reservas.
	s.Externos = func(context.Context, time.Time, time.Time) ([]Interval, error) {
		return nil, errors.New("google caído")
	}
	slots, err = s.Disponibilidad(context.Background(), "Corte", martes, martes, FranjaManana)
	if err != nil || len(slots) == 0 {
		t.Errorf("con Calendar caído debería degradar: slots = %d, err = %v", len(slots), err)
	}
}
