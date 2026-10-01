package calendar

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
)

type fakeWriter struct {
	creados, borrados []string
	fallar            bool
}

func (f *fakeWriter) CreateEvent(_ context.Context, _ string, e Event) error {
	if f.fallar {
		return errors.New("google caído")
	}
	f.creados = append(f.creados, e.ID)
	return nil
}

func (f *fakeWriter) DeleteEvent(_ context.Context, _, eventID string) error {
	if f.fallar {
		return errors.New("google caído")
	}
	f.borrados = append(f.borrados, eventID)
	return nil
}

type fakeSyncStore struct {
	pendientes    []Pendiente
	sincronizados map[string]string
	errores       map[string]string
}

func (f *fakeSyncStore) PendientesCalendar(context.Context, int) ([]Pendiente, error) {
	return f.pendientes, nil
}

func (f *fakeSyncStore) CalendarSincronizado(_ context.Context, id, eventID string) error {
	f.sincronizados[id] = eventID
	return nil
}

func (f *fakeSyncStore) CalendarError(_ context.Context, id, msg string) error {
	f.errores[id] = msg
	return nil
}

func TestSyncOnce(t *testing.T) {
	st := &fakeSyncStore{
		pendientes: []Pendiente{
			{AppointmentID: "11111111-0000-0000-0000-000000000000", Confirmada: true, Servicio: "Corte", NombreCliente: "Ana"},
			{AppointmentID: "22222222-0000-0000-0000-000000000000", Confirmada: false, EventID: "ev-viejo"},
		},
		sincronizados: map[string]string{}, errores: map[string]string{},
	}
	w := &fakeWriter{}
	s := &Syncer{Calendar: w, Store: st, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	n, err := s.SyncOnce(context.Background())
	if err != nil || n != 2 {
		t.Fatalf("n = %d, err = %v", n, err)
	}
	if len(w.creados) != 1 || len(w.borrados) != 1 || w.borrados[0] != "ev-viejo" {
		t.Errorf("creados = %v, borrados = %v", w.creados, w.borrados)
	}
	if st.sincronizados["11111111-0000-0000-0000-000000000000"] != EventID("11111111-0000-0000-0000-000000000000") {
		t.Errorf("no se guardó el id del evento creado: %v", st.sincronizados)
	}
	if v, ok := st.sincronizados["22222222-0000-0000-0000-000000000000"]; !ok || v != "" {
		t.Errorf("el borrado debe limpiar el id: %q, %v", v, ok)
	}

	// Con Google caído: no corta, registra el error para reintentar.
	w.fallar = true
	st.sincronizados = map[string]string{}
	n, err = s.SyncOnce(context.Background())
	if err != nil || n != 0 || len(st.errores) != 2 {
		t.Fatalf("n = %d, err = %v, errores = %v", n, err, st.errores)
	}
}
