package calendar

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"google.golang.org/api/option"
)

// fakeGoogle imita las rutas de la API de Calendar que se usan.
func fakeGoogle(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := newClient(context.Background(), option.WithEndpoint(srv.URL+"/"), option.WithoutAuthentication())
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestBusy(t *testing.T) {
	c := fakeGoogle(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/freeBusy") {
			t.Errorf("path = %s", r.URL.Path)
		}
		io.WriteString(w, `{"calendars": {
		  "dueño@gmail.com": {"busy": [{"start": "2026-10-01T15:00:00-03:00", "end": "2026-10-01T16:00:00-03:00"}]},
		  "no-compartido@gmail.com": {"errors": [{"domain": "global", "reason": "notFound"}]}
		}}`)
	})
	ctx := context.Background()
	d := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

	busy, err := c.Busy(ctx, "dueño@gmail.com", d, d.Add(24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(busy) != 1 || busy[0].Start.UTC().Hour() != 18 || busy[0].End.Sub(busy[0].Start) != time.Hour {
		t.Errorf("busy = %+v", busy)
	}
	// Un calendario que no se compartió no da error HTTP: hay que detectarlo.
	if _, err := c.Busy(ctx, "no-compartido@gmail.com", d, d.Add(time.Hour)); err == nil || !strings.Contains(err.Error(), "compartido") {
		t.Errorf("calendario no compartido: err = %v", err)
	}
}

func TestCreateEventIdempotente(t *testing.T) {
	var body map[string]any
	llamadas := 0
	c := fakeGoogle(t, func(w http.ResponseWriter, r *http.Request) {
		llamadas++
		raw, _ := io.ReadAll(r.Body)
		json.Unmarshal(raw, &body)
		if llamadas == 2 {
			// Segundo intento con el mismo id: Google responde 409.
			w.WriteHeader(http.StatusConflict)
			io.WriteString(w, `{"error": {"code": 409, "message": "The requested identifier already exists."}}`)
			return
		}
		io.WriteString(w, `{"id": "x"}`)
	})
	loc, _ := time.LoadLocation("America/Santiago")
	e := Event{
		ID: EventID("a42c99fe-73b8-48b7-93c3-27304a3098da"), Summary: "Corte — Ana",
		Start: time.Date(2026, 10, 1, 10, 0, 0, 0, loc), End: time.Date(2026, 10, 1, 10, 30, 0, 0, loc),
		TimeZone: "America/Santiago", AppointmentID: "a42c99fe-73b8-48b7-93c3-27304a3098da",
	}
	for i := 0; i < 2; i++ {
		if err := c.CreateEvent(context.Background(), "cal", e); err != nil {
			t.Fatalf("intento %d: %v", i+1, err)
		}
	}
	if body["id"] != "agendaa42c99fe73b848b793c327304a3098da" {
		t.Errorf("id = %v", body["id"])
	}
	start := body["start"].(map[string]any)
	if start["dateTime"] != "2026-10-01T10:00:00-03:00" || start["timeZone"] != "America/Santiago" {
		t.Errorf("start = %v", start)
	}
	priv := body["extendedProperties"].(map[string]any)["private"].(map[string]any)
	if priv["appointment_id"] != e.AppointmentID {
		t.Errorf("extendedProperties = %v", priv)
	}
}

func TestEventIDValido(t *testing.T) {
	id := EventID("A42C99FE-73B8-48B7-93C3-27304A3098DA")
	for _, r := range id {
		// base32hex: 0-9 y a-v
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'v') {
			t.Fatalf("carácter inválido %q en %s", r, id)
		}
	}
}

func TestDeleteEvent(t *testing.T) {
	codigo := http.StatusGone
	c := fakeGoogle(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("método = %s", r.Method)
		}
		w.WriteHeader(codigo)
		io.WriteString(w, `{"error": {"message": "x"}}`)
	})
	// Ya borrado (410) o inexistente (404): éxito.
	for _, codigo = range []int{http.StatusGone, http.StatusNotFound} {
		if err := c.DeleteEvent(context.Background(), "cal", "ev"); err != nil {
			t.Errorf("%d: %v", codigo, err)
		}
	}
	codigo = http.StatusForbidden
	if err := c.DeleteEvent(context.Background(), "cal", "ev"); err == nil {
		t.Error("403 debería ser error")
	}
}
