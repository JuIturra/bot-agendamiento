// Package limit envuelve un llm.Provider para respetar límites de cuota: requests
// por minuto (RPM), tokens de entrada por minuto (TPM) y requests por día (RPD).
// Pensado para capas gratuitas como la de Gemini, donde pasarse significa errores
// 429 en medio de una conversación.
package limit

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"agendamiento/internal/llm"
)

// Limits en cero significa sin límite en esa dimensión.
type Limits struct {
	RPM int
	TPM int
	RPD int
	// ResetZone es la zona horaria en la que se reinicia el contador diario. Gemini
	// lo reinicia a medianoche hora del Pacífico.
	ResetZone *time.Location
	// MaxWait es lo máximo que se espera por cupo de RPM/TPM antes de rendirse.
	MaxWait time.Duration
}

func (l Limits) Enabled() bool { return l.RPM > 0 || l.TPM > 0 || l.RPD > 0 }

// Uso es el consumo registrado dentro de las ventanas de los límites.
type Uso struct {
	Minuto    []Request // requests del último minuto, del más antiguo al más nuevo
	DiaActual int       // requests desde el último reinicio diario
}

type Request struct {
	At       time.Time
	TokensIn int64
}

// Store guarda los requests. Reserve debe ser atómico entre procesos: lee el uso y,
// si decide registrar, lo hace sin que otro proceso se cuele en medio.
type Store interface {
	// Reserve llama a decide con el uso actual. Si decide devuelve true, registra
	// un request nuevo y devuelve su id.
	Reserve(ctx context.Context, clave string, desdeDia time.Time, now time.Time, decide func(Uso) bool) (id int64, ok bool, err error)
	Record(ctx context.Context, id int64, tokensIn int64) error
}

// ErrCuota indica que no hay cupo y no conviene esperar.
type ErrCuota struct {
	Motivo     string
	Reintentar time.Time
}

func (e *ErrCuota) Error() string {
	return fmt.Sprintf("cuota de LLM agotada (%s); reintentar después de %s", e.Motivo, e.Reintentar.Format(time.RFC3339))
}

// IsCuota indica si err es un ErrCuota.
func IsCuota(err error) bool {
	var e *ErrCuota
	return errors.As(err, &e)
}

type Provider struct {
	inner llm.Provider
	lim   Limits
	store Store
	log   *slog.Logger
	now   func() time.Time
	sleep func(context.Context, time.Duration) error

	mu     sync.Mutex
	lastIn int64 // input del último request: estimación para el TPM del siguiente
}

func Wrap(p llm.Provider, lim Limits, st Store, log *slog.Logger) *Provider {
	if lim.ResetZone == nil {
		lim.ResetZone = time.UTC
	}
	if lim.MaxWait == 0 {
		lim.MaxWait = 90 * time.Second
	}
	return &Provider{inner: p, lim: lim, store: st, log: log, now: time.Now, sleep: sleepCtx}
}

func (p *Provider) Name() string { return p.inner.Name() }

// reintentos ante errores temporales (5xx). El proveedor envuelto no debe reintentar
// por su cuenta: cada intento tiene que pasar por la cuota.
const reintentos = 1

func (p *Provider) Chat(ctx context.Context, req llm.Request) (llm.Response, error) {
	for intento := 0; ; intento++ {
		resp, err := p.chatUnaVez(ctx, req)
		if !llm.IsTemporary(err) || intento == reintentos {
			return resp, err
		}
		p.log.Warn("error temporal del proveedor, reintentando", "llm", p.inner.Name(), "err", err)
		if err := p.sleep(ctx, 2*time.Second); err != nil {
			return llm.Response{}, err
		}
	}
}

func (p *Provider) chatUnaVez(ctx context.Context, req llm.Request) (llm.Response, error) {
	id, err := p.acquire(ctx)
	if err != nil {
		return llm.Response{}, err
	}
	resp, err := p.inner.Chat(ctx, req)
	// Un request fallido también cuenta: el proveedor lo recibió.
	if err == nil {
		p.mu.Lock()
		p.lastIn = resp.Usage.In
		p.mu.Unlock()
		if rerr := p.store.Record(ctx, id, resp.Usage.In); rerr != nil {
			p.log.Warn("registrando tokens de cuota", "err", rerr)
		}
	}
	return resp, err
}

// acquire espera hasta que haya cupo y registra el request.
func (p *Provider) acquire(ctx context.Context) (int64, error) {
	p.mu.Lock()
	estimado := p.lastIn
	p.mu.Unlock()

	var esperado time.Duration
	for {
		now := p.now()
		var espera time.Duration
		var cuotaDia *ErrCuota
		var usadosDia int
		id, ok, err := p.store.Reserve(ctx, p.inner.Name(), inicioDelDia(now, p.lim.ResetZone), now, func(u Uso) bool {
			usadosDia = u.DiaActual
			if p.lim.RPD > 0 && u.DiaActual >= p.lim.RPD {
				cuotaDia = &ErrCuota{Motivo: fmt.Sprintf("%d requests por día", p.lim.RPD),
					Reintentar: inicioDelDia(now, p.lim.ResetZone).AddDate(0, 0, 1)}
				return false
			}
			espera = Espera(u.Minuto, now, p.lim.RPM, p.lim.TPM, estimado)
			return espera == 0
		})
		if err != nil {
			return 0, err
		}
		if cuotaDia != nil {
			return 0, cuotaDia
		}
		if ok {
			if p.lim.RPD > 0 {
				p.log.Info("cuota LLM", "llm", p.inner.Name(), "usados_hoy", usadosDia+1, "limite_dia", p.lim.RPD)
			}
			return id, nil
		}
		if esperado+espera > p.lim.MaxWait {
			return 0, &ErrCuota{Motivo: "límite por minuto", Reintentar: now.Add(espera)}
		}
		p.log.Info("esperando cupo de LLM", "llm", p.inner.Name(), "espera", espera.Round(100*time.Millisecond).String())
		if err := p.sleep(ctx, espera); err != nil {
			return 0, err
		}
		esperado += espera
	}
}

// Espera calcula cuánto falta para que un request nuevo quepa en la ventana de un
// minuto. recientes debe estar ordenado del más antiguo al más nuevo y contener
// solo requests del último minuto. estimado es el input esperado del request nuevo.
func Espera(recientes []Request, now time.Time, rpm, tpm int, estimado int64) time.Duration {
	// Cuántos de los más antiguos tienen que salir de la ventana.
	salir := 0
	if rpm > 0 && len(recientes) >= rpm {
		salir = len(recientes) - rpm + 1
	}
	if tpm > 0 {
		if estimado > int64(tpm) {
			// Un request más grande que el límite solo entra con la ventana vacía.
			estimado = int64(tpm)
		}
		var suma int64
		for _, r := range recientes {
			suma += r.TokensIn
		}
		for k := 0; suma+estimado > int64(tpm) && k < len(recientes); k++ {
			suma -= recientes[k].TokensIn
			if k+1 > salir {
				salir = k + 1
			}
		}
	}
	if salir == 0 {
		return 0
	}
	espera := recientes[salir-1].At.Add(time.Minute).Sub(now)
	if espera < 0 {
		return 0
	}
	// Margen para no llegar justo al borde de la ventana del proveedor.
	return espera + 500*time.Millisecond
}

func inicioDelDia(t time.Time, loc *time.Location) time.Time {
	y, m, d := t.In(loc).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, loc)
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
