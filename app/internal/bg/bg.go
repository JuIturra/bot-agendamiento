// Package bg tiene el ciclo común a los procesos en segundo plano que llevan cambios
// de la base a un servicio externo (Calendar, avisos): barren al recibir un aviso
// (Kick) y, por si acaso, cada cierto intervalo.
package bg

import (
	"context"
	"log/slog"
	"time"
)

type Loop struct {
	// Intervalo entre barridos cuando nadie llama a Kick. Default 1 minuto.
	Intervalo time.Duration
	kick      chan struct{}
}

func (l *Loop) init() {
	if l.kick == nil {
		l.kick = make(chan struct{}, 1)
	}
	if l.Intervalo == 0 {
		l.Intervalo = time.Minute
	}
}

// Kick pide un barrido inmediato. No bloquea: si ya hay uno pedido, no hace nada.
func (l *Loop) Kick() {
	l.init()
	select {
	case l.kick <- struct{}{}:
	default:
	}
}

// Run llama a barrer al arrancar, en cada Kick y cada Intervalo, hasta que ctx se
// cancela. Los errores se registran y el ciclo sigue.
func (l *Loop) Run(ctx context.Context, nombre string, log *slog.Logger, barrer func(context.Context) error) {
	l.init()
	t := time.NewTicker(l.Intervalo)
	defer t.Stop()
	for {
		if err := barrer(ctx); err != nil && ctx.Err() == nil {
			log.Error(nombre, "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-l.kick:
		}
	}
}
