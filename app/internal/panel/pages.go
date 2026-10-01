package panel

import (
	"bytes"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// diasResumen es la ventana de las métricas del resumen.
const diasResumen = 7

type pagina struct {
	Titulo  string
	Seccion string
	Negocio string
	Ahora   time.Time
	Datos   any
}

func (p *Panel) render(w http.ResponseWriter, nombre, titulo string, datos any) error {
	var buf bytes.Buffer
	if err := p.tmpl[nombre].Execute(&buf, pagina{
		Titulo: titulo, Seccion: nombre, Negocio: p.cfg.Negocio, Ahora: p.cfg.Now(), Datos: datos,
	}); err != nil {
		return err
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, err := buf.WriteTo(w)
	return err
}

type vistaResumen struct {
	Resumen
	Dias       int
	LimiteRPD  int
	UltimoPoll time.Time
	PollViejo  bool
	// ConTelegram es false cuando el panel corre sin el bot (cmd/panel).
	ConTelegram bool
	Conversion  string
}

func (p *Panel) resumen(w http.ResponseWriter, r *http.Request) error {
	now := p.cfg.Now()
	desde := now.AddDate(0, 0, -diasResumen)
	res, err := p.cfg.Datos.Resumen(r.Context(), p.cfg.TenantID, desde, inicioDelDia(now, p.cfg.ZonaReset), p.cfg.ClaveLLM)
	if err != nil {
		return err
	}
	v := vistaResumen{Resumen: res, Dias: diasResumen, LimiteRPD: p.cfg.LimiteRPD,
		Conversion: pct(res.ConversacionesConReserva, res.Conversaciones)}
	if p.cfg.UltimoPoll != nil {
		v.ConTelegram = true
		v.UltimoPoll = p.cfg.UltimoPoll()
		// El poll dura hasta 30 s: más de 2 minutos sin uno exitoso es un problema.
		v.PollViejo = v.UltimoPoll.IsZero() || now.Sub(v.UltimoPoll) > 2*time.Minute
	}
	return p.render(w, "resumen", "Resumen", v)
}

func (p *Panel) conversaciones(w http.ResponseWriter, r *http.Request) error {
	cs, err := p.cfg.Datos.Conversaciones(r.Context(), p.cfg.TenantID, 100)
	if err != nil {
		return err
	}
	return p.render(w, "conversaciones", "Conversaciones", cs)
}

func (p *Panel) conversacion(w http.ResponseWriter, r *http.Request) error {
	c, err := p.cfg.Datos.Conversacion(r.Context(), p.cfg.TenantID, r.PathValue("id"))
	if err != nil {
		return err
	}
	return p.render(w, "conversacion", "Conversación con "+c.Cliente, c)
}

func (p *Panel) reservas(w http.ResponseWriter, r *http.Request) error {
	rs, err := p.cfg.Datos.Reservas(r.Context(), p.cfg.TenantID, p.cfg.Now().AddDate(0, 0, -diasResumen), 200)
	if err != nil {
		return err
	}
	return p.render(w, "reservas", "Reservas", rs)
}

// --- formato ---

var dias = [...]string{"dom", "lun", "mar", "mié", "jue", "vie", "sáb"}

func fechaCorta(t time.Time, loc *time.Location) string {
	if t.IsZero() {
		return "—"
	}
	t = t.In(loc)
	return fmt.Sprintf("%s %s", dias[t.Weekday()], t.Format("02-01 15:04"))
}

func hace(t, now time.Time) string {
	if t.IsZero() {
		return "nunca"
	}
	d := now.Sub(t)
	switch {
	case d < 0:
		return "en el futuro"
	case d < time.Minute:
		return "hace segundos"
	case d < time.Hour:
		return fmt.Sprintf("hace %d min", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("hace %d h", int(d.Hours()))
	default:
		return fmt.Sprintf("hace %d días", int(d.Hours()/24))
	}
}

func miles(n int64) string {
	s := strconv.FormatInt(n, 10)
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte('.')
		}
		b.WriteRune(c)
	}
	return b.String()
}

func pct(parte, total int) string {
	if total == 0 {
		return "—"
	}
	return fmt.Sprintf("%d%%", parte*100/total)
}

func inicioDelDia(t time.Time, loc *time.Location) time.Time {
	y, m, d := t.In(loc).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, loc)
}
