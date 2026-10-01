// Package booking calcula horarios disponibles y gestiona reservas. El cálculo de
// slots es lógica pura: no toca la base de datos ni el reloj del sistema.
package booking

import (
	"errors"
	"time"

	"agendamiento/internal/tenant"
)

// Interval es un tramo ocupado [Start, End).
type Interval struct {
	Start, End time.Time
}

// Franja filtra slots por momento del día.
type Franja string

const (
	FranjaCualquiera Franja = "cualquiera"
	FranjaManana     Franja = "mañana"
	FranjaTarde      Franja = "tarde"
)

// inicioTarde separa mañana de tarde, en minutos desde medianoche.
const inicioTarde = 13 * 60

// Query describe una búsqueda de disponibilidad. Desde y Hasta son fechas
// (se ignora la hora) y ambas se incluyen.
type Query struct {
	Servicio tenant.Servicio
	Desde    time.Time
	Hasta    time.Time
	Franja   Franja
	Now      time.Time
	Ocupado  []Interval
}

// Slots devuelve los inicios posibles para el servicio, en orden cronológico y en la
// zona horaria del negocio. Un slot es válido si el servicio completo cabe en un
// tramo de atención, respeta la anticipación mínima y el máximo de días hacia
// adelante, y no se solapa con nada ocupado (considerando el buffer entre citas).
func Slots(cfg *tenant.Config, q Query) []time.Time {
	loc := cfg.Location()
	now := q.Now.In(loc)
	earliest := now.Add(time.Duration(cfg.AnticipacionMinimaMinutos) * time.Minute)
	ultimoDia := diaSiguiente(mediodia(now, loc), cfg.DiasMaximosAdelanto)
	dur := time.Duration(q.Servicio.DuracionMinutos) * time.Minute
	buffer := time.Duration(cfg.BufferMinutos) * time.Minute

	var out []time.Time
	// Se itera por días usando el mediodía: en Chile el cambio de horario ocurre a
	// medianoche, así que ese día la medianoche local no existe.
	for d := mediodia(q.Desde, loc); !d.After(mediodia(q.Hasta, loc)); d = diaSiguiente(d, 1) {
		if d.After(ultimoDia) {
			break
		}
		for _, r := range cfg.RangosDelDia(d) {
			for m := r.Desde; m+q.Servicio.DuracionMinutos <= r.Hasta; m += cfg.IntervaloMinutos {
				if !enFranja(m, q.Franja) {
					continue
				}
				// time.Date con hora y minuto explícitos resuelve bien los días de
				// cambio de horario; sumar una Duration a la medianoche no.
				start := time.Date(d.Year(), d.Month(), d.Day(), m/60, m%60, 0, 0, loc)
				if start.Before(earliest) {
					continue
				}
				if solapa(start, start.Add(dur+buffer), q.Ocupado, buffer) {
					continue
				}
				out = append(out, start)
			}
		}
	}
	return out
}

var (
	ErrFueraDeHorario = errors.New("ese horario está fuera del horario de atención")
	ErrMuyPronto      = errors.New("ese horario no cumple la anticipación mínima")
	ErrMuyLejos       = errors.New("ese horario supera el máximo de días hacia adelante")
	ErrOcupado        = errors.New("ese horario ya está ocupado")
)

// Validar comprueba que inicio sea un slot válido y, si no lo es, explica por qué.
func Validar(cfg *tenant.Config, srv tenant.Servicio, inicio, now time.Time, ocupado []Interval) error {
	loc := cfg.Location()
	inicio = inicio.In(loc)
	if mediodia(inicio, loc).After(diaSiguiente(mediodia(now, loc), cfg.DiasMaximosAdelanto)) {
		return ErrMuyLejos
	}
	if inicio.Before(now.Add(time.Duration(cfg.AnticipacionMinimaMinutos) * time.Minute)) {
		return ErrMuyPronto
	}
	// Para distinguir "fuera de horario" de "ocupado", primero se calcula el día sin
	// reservas y después con ellas. Los límites de anticipación y días ya se
	// validaron arriba.
	q := Query{Servicio: srv, Desde: inicio, Hasta: inicio, Franja: FranjaCualquiera, Now: inicio}
	if !contiene(slotsSinLimites(cfg, q), inicio) {
		return ErrFueraDeHorario
	}
	q.Ocupado = ocupado
	if !contiene(slotsSinLimites(cfg, q), inicio) {
		return ErrOcupado
	}
	return nil
}

// slotsSinLimites calcula slots ignorando anticipación y máximo de días.
func slotsSinLimites(cfg *tenant.Config, q Query) []time.Time {
	c := *cfg
	c.AnticipacionMinimaMinutos = 0
	c.DiasMaximosAdelanto = 1 << 16
	return Slots(&c, q)
}

func contiene(slots []time.Time, t time.Time) bool {
	for _, s := range slots {
		if s.Equal(t) {
			return true
		}
	}
	return false
}

// solapa indica si [start, end) choca con algún intervalo ocupado, extendido por el
// buffer que ese intervalo necesita después.
func solapa(start, end time.Time, ocupado []Interval, buffer time.Duration) bool {
	for _, o := range ocupado {
		if start.Before(o.End.Add(buffer)) && o.Start.Before(end) {
			return true
		}
	}
	return false
}

func enFranja(minuto int, f Franja) bool {
	switch f {
	case FranjaManana:
		return minuto < inicioTarde
	case FranjaTarde:
		return minuto >= inicioTarde
	default:
		return true
	}
}

// mediodia devuelve las 12:00 locales del día de t.
func mediodia(t time.Time, loc *time.Location) time.Time {
	y, m, d := t.In(loc).Date()
	return time.Date(y, m, d, 12, 0, 0, 0, loc)
}

func diaSiguiente(t time.Time, n int) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day()+n, 12, 0, 0, 0, t.Location())
}
