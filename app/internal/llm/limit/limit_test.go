package limit

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"agendamiento/internal/llm"
)

var t0 = time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC)

func TestEspera(t *testing.T) {
	reqs := func(segs ...int) []Request {
		var out []Request
		for _, s := range segs {
			out = append(out, Request{At: t0.Add(time.Duration(s) * time.Second), TokensIn: 1000})
		}
		return out
	}
	now := t0.Add(30 * time.Second)
	casos := []struct {
		nombre    string
		recientes []Request
		rpm, tpm  int
		estimado  int64
		want      time.Duration // sin el margen de 500 ms
	}{
		{"sin límites", reqs(0, 1, 2), 0, 0, 0, 0},
		{"bajo el RPM", reqs(0, 10), 5, 0, 0, 0},
		// 5 en la ventana con RPM 5: hay que esperar a que salga el más antiguo (t0).
		{"RPM lleno", reqs(0, 10, 20, 25, 28), 5, 0, 0, 30 * time.Second},
		// 3000 usados + 2500 estimados = 5500 > 5000: basta con que salga el más
		// antiguo (quedan 4500).
		{"TPM lleno", reqs(0, 10, 20), 0, 5000, 2500, 30 * time.Second},
		// Con 3500 estimados tienen que salir dos (quedan 1000 + 3500).
		{"TPM lleno, salen dos", reqs(0, 10, 20), 0, 5000, 3500, 40 * time.Second},
		{"TPM con cupo", reqs(0, 10), 0, 5000, 2500, 0},
		// Un request estimado más grande que el TPM solo entra con la ventana vacía.
		{"estimado gigante", reqs(0, 10), 0, 5000, 9999, 40 * time.Second},
	}
	for _, c := range casos {
		got := Espera(c.recientes, now, c.rpm, c.tpm, c.estimado)
		want := c.want
		if want > 0 {
			want += 500 * time.Millisecond
		}
		if got != want {
			t.Errorf("%s: espera = %v, want %v", c.nombre, got, want)
		}
	}
}

// memStore es un Store en memoria para tests.
type memStore struct {
	mu   sync.Mutex
	reqs []Request
}

func (m *memStore) Reserve(_ context.Context, _ string, desdeDia, now time.Time, decide func(Uso) bool) (int64, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var u Uso
	for _, r := range m.reqs {
		if !r.At.Before(desdeDia) {
			u.DiaActual++
		}
		if r.At.After(now.Add(-time.Minute)) {
			u.Minuto = append(u.Minuto, r)
		}
	}
	if !decide(u) {
		return 0, false, nil
	}
	m.reqs = append(m.reqs, Request{At: now})
	return int64(len(m.reqs)), true, nil
}

func (m *memStore) Record(_ context.Context, id int64, tokensIn int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reqs[id-1].TokensIn = tokensIn
	return nil
}

type fake struct {
	llamadas int
	fallas   int // cuántas de las primeras llamadas devuelven un error temporal
}

func (f *fake) Name() string { return "fake/modelo" }
func (f *fake) Chat(context.Context, llm.Request) (llm.Response, error) {
	f.llamadas++
	if f.llamadas <= f.fallas {
		return llm.Response{}, &llm.TemporaryError{Err: errors.New("503")}
	}
	return llm.Response{Stop: llm.StopEnd, Usage: llm.Usage{In: 100}}, nil
}

// reloj simulado: sleep avanza el tiempo en vez de esperar.
func wrapConReloj(lim Limits, st Store) (*Provider, *fake, *time.Time, *[]time.Duration) {
	f := &fake{}
	p := Wrap(f, lim, st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	now := t0
	var esperas []time.Duration
	p.now = func() time.Time { return now }
	p.sleep = func(_ context.Context, d time.Duration) error {
		esperas = append(esperas, d)
		now = now.Add(d)
		return nil
	}
	return p, f, &now, &esperas
}

func TestRPMEspera(t *testing.T) {
	p, f, _, esperas := wrapConReloj(Limits{RPM: 2}, &memStore{})
	for i := 0; i < 3; i++ {
		if _, err := p.Chat(context.Background(), llm.Request{}); err != nil {
			t.Fatal(err)
		}
	}
	if f.llamadas != 3 || len(*esperas) != 1 {
		t.Fatalf("llamadas = %d, esperas = %v", f.llamadas, *esperas)
	}
	if (*esperas)[0] < time.Minute {
		t.Errorf("el tercer request debía esperar ~1 min, esperó %v", (*esperas)[0])
	}
}

func TestRPMEsperaMaxima(t *testing.T) {
	p, f, _, _ := wrapConReloj(Limits{RPM: 1, MaxWait: 10 * time.Second}, &memStore{})
	p.Chat(context.Background(), llm.Request{})
	_, err := p.Chat(context.Background(), llm.Request{})
	if !IsCuota(err) || f.llamadas != 1 {
		t.Fatalf("se esperaba ErrCuota sin llamar al proveedor: err = %v, llamadas = %d", err, f.llamadas)
	}
}

func TestRPDSeReiniciaEnSuZona(t *testing.T) {
	pacifico, _ := time.LoadLocation("America/Los_Angeles")
	p, f, now, _ := wrapConReloj(Limits{RPD: 2, ResetZone: pacifico}, &memStore{})
	// 10:00 UTC = 03:00 en el Pacífico.
	p.Chat(context.Background(), llm.Request{})
	p.Chat(context.Background(), llm.Request{})
	_, err := p.Chat(context.Background(), llm.Request{})
	var ec *ErrCuota
	if !asCuota(err, &ec) || f.llamadas != 2 {
		t.Fatalf("tercer request del día: err = %v, llamadas = %d", err, f.llamadas)
	}
	// Reintentar a medianoche del Pacífico = 07:00 UTC del día siguiente.
	if want := time.Date(2026, 9, 30, 7, 0, 0, 0, time.UTC); !ec.Reintentar.Equal(want) {
		t.Errorf("Reintentar = %v, want %v", ec.Reintentar.UTC(), want)
	}
	*now = time.Date(2026, 9, 30, 7, 0, 1, 0, time.UTC)
	if _, err := p.Chat(context.Background(), llm.Request{}); err != nil {
		t.Fatalf("después del reinicio diario debería haber cupo: %v", err)
	}
}

func asCuota(err error, target **ErrCuota) bool {
	e, ok := err.(*ErrCuota)
	*target = e
	return ok
}

// Cada reintento pasa por la cuota: un 503 seguido de un éxito son 2 requests.
func TestReintentoCuentaEnLaCuota(t *testing.T) {
	st := &memStore{}
	p, f, _, _ := wrapConReloj(Limits{RPD: 20}, st)
	f.fallas = 1
	if _, err := p.Chat(context.Background(), llm.Request{}); err != nil {
		t.Fatal(err)
	}
	if f.llamadas != 2 || len(st.reqs) != 2 {
		t.Fatalf("llamadas = %d, registrados = %d; se esperaban 2 y 2", f.llamadas, len(st.reqs))
	}
	// Dos errores temporales seguidos: se rinde después del reintento.
	f.llamadas, f.fallas = 0, 5
	if _, err := p.Chat(context.Background(), llm.Request{}); !llm.IsTemporary(err) || f.llamadas != 2 {
		t.Fatalf("err = %v, llamadas = %d", err, f.llamadas)
	}
}
