// Package notify avisa al dueño del negocio de cada reserva y cancelación, por
// Telegram. El dueño se registra escribiéndole al mismo bot "/dueno <código>".
package notify

import (
	"context"
	"crypto/subtle"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"agendamiento/internal/bg"
	"agendamiento/internal/formato"
)

// Pendiente es una reserva con un aviso por enviar.
type Pendiente struct {
	AppointmentID string
	ChatID        int64 // chat de Telegram del dueño
	TimeZone      string
	Servicio      string
	NombreCliente string
	Canal         string
	Inicio        time.Time
	Confirmada    bool
	// AvisadaCreacion indica si el dueño ya supo de la reserva.
	AvisadaCreacion bool
}

type Store interface {
	// AvisosPendientes devuelve reservas con aviso por enviar de negocios con dueño
	// registrado, creadas después del registro y cuyo próximo intento ya venció.
	AvisosPendientes(ctx context.Context, limit int) ([]Pendiente, error)
	// MarcarAviso registra qué avisos quedan resueltos (enviados u omitidos).
	MarcarAviso(ctx context.Context, appointmentID string, creacion, cancelacion bool) error
	// AvisoError programa el próximo intento con backoff.
	AvisoError(ctx context.Context, appointmentID, msg string) error
	// RegistrarDueno guarda el chat del dueño del negocio.
	RegistrarDueno(ctx context.Context, tenantID string, chatID int64) error
}

type Sender interface {
	Send(ctx context.Context, chatID int64, text string) error
}

// Notifier envía los avisos pendientes. Corre en segundo plano; Kick pide un barrido
// inmediato después de una reserva o cancelación.
type Notifier struct {
	Store  Store
	Sender Sender
	Log    *slog.Logger
	bg.Loop
}

func (n *Notifier) Run(ctx context.Context) {
	n.Loop.Run(ctx, "avisos al dueño", n.Log, func(ctx context.Context) error {
		_, err := n.EnviarPendientes(ctx)
		return err
	})
}

// EnviarPendientes envía los avisos pendientes y devuelve cuántos mensajes salieron.
// Un fallo de Telegram en un aviso no corta el barrido.
func (n *Notifier) EnviarPendientes(ctx context.Context) (int, error) {
	ps, err := n.Store.AvisosPendientes(ctx, 50)
	if err != nil {
		return 0, err
	}
	enviados := 0
	for _, p := range ps {
		texto, creacion, cancelacion := Mensaje(p)
		if texto != "" {
			if err := n.Sender.Send(ctx, p.ChatID, texto); err != nil {
				n.Log.Warn("aviso al dueño: se reintentará", "appointment", p.AppointmentID, "err", err)
				if err := n.Store.AvisoError(ctx, p.AppointmentID, err.Error()); err != nil {
					return enviados, err
				}
				continue
			}
			enviados++
			n.Log.Info("aviso al dueño enviado", "appointment", p.AppointmentID, "confirmada", p.Confirmada)
		}
		if err := n.Store.MarcarAviso(ctx, p.AppointmentID, creacion, cancelacion); err != nil {
			return enviados, err
		}
	}
	return enviados, nil
}

// Mensaje decide qué decirle al dueño y qué avisos quedan resueltos con eso. Una
// reserva cancelada de la que el dueño nunca supo no se avisa: se resuelven los dos
// avisos sin mensaje.
func Mensaje(p Pendiente) (texto string, creacion, cancelacion bool) {
	loc, err := time.LoadLocation(p.TimeZone)
	if err != nil {
		loc = time.UTC
	}
	cuando := formato.FechaHora(p.Inicio.In(loc))
	cliente := fmt.Sprintf("Cliente: %s (%s)", p.NombreCliente, canal(p.Canal))
	switch {
	case p.Confirmada:
		return fmt.Sprintf("📅 Nueva reserva\n%s, %s\n%s", p.Servicio, cuando, cliente), true, false
	case p.AvisadaCreacion:
		return fmt.Sprintf("❌ Reserva cancelada\n%s, %s\n%s\nEl horario quedó libre.", p.Servicio, cuando, cliente), true, true
	default:
		return "", true, true
	}
}

func canal(c string) string {
	switch c {
	case "telegram":
		return "Telegram"
	case "whatsapp":
		return "WhatsApp"
	case "consola":
		return "prueba por consola"
	default:
		return c
	}
}

// Comando de registro del dueño. Sin tilde: Telegram solo acepta [a-z0-9_].
const Comando = "/dueno"

// Registro atiende "/dueno <código>". Devuelve handled=false si el texto no es ese
// comando, para que siga al motor de conversación.
type Registro struct {
	Store    Store
	TenantID string
	Negocio  string
	// Codigo es el secreto que el dueño debe enviar. Vacío = registro desactivado.
	Codigo string
	Log    *slog.Logger
}

func (r *Registro) Handle(ctx context.Context, chatID int64, texto string) (respuesta string, handled bool, err error) {
	cmd, codigo, _ := strings.Cut(strings.TrimSpace(texto), " ")
	if !strings.EqualFold(cmd, Comando) {
		return "", false, nil
	}
	codigo = strings.TrimSpace(codigo)
	// Comparación en tiempo constante: el código es un secreto.
	if r.Codigo == "" || subtle.ConstantTimeCompare([]byte(codigo), []byte(r.Codigo)) != 1 {
		r.Log.Warn("intento de registro de dueño con código inválido", "chat", chatID)
		return "Código inválido.", true, nil
	}
	if err := r.Store.RegistrarDueno(ctx, r.TenantID, chatID); err != nil {
		return "", true, err
	}
	r.Log.Info("dueño registrado", "chat", chatID)
	return fmt.Sprintf("Listo. Desde ahora te aviso por aquí cada reserva y cancelación de %s.", r.Negocio), true, nil
}
