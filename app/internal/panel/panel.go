// Package panel es el panel web del operador: qué conversaciones hubo, qué hizo el
// bot en cada una, reservas, cuota del LLM y salud de las integraciones. Es de solo
// lectura y está protegido con contraseña: muestra datos personales de clientes.
package panel

import (
	"context"
	"crypto/subtle"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"time"
)

var ErrNoEncontrada = errors.New("no encontrada")

type Resumen struct {
	Conversaciones           int
	ConversacionesConReserva int
	MensajesCliente          int
	Reservas                 int
	Cancelaciones            int
	Proximas                 int
	TokensIn, TokensOut      int64
	UltimoMensaje            time.Time

	LLMHoy int // requests al LLM desde el último reinicio diario

	CalendarActivo      bool
	CalendarPendientes  int
	CalendarConError    int
	CalendarUltimoError string

	DuenoRegistrado  bool
	AvisosPendientes int
	AvisosConError   int
	AvisoUltimoError string
}

type ConversacionFila struct {
	ID, Cliente, Canal, Estado string
	Inicio, Ultimo             time.Time
	MensajesCliente            int
	TokensIn, TokensOut        int64
	Agendo                     bool // alguna llamada a agendar respondió OK
	Vista                      string
}

type Mensaje struct {
	Rol      string // user | assistant | tool
	At       time.Time
	Texto    string
	Tool     string
	Input    string
	Result   string
	IsError  bool
	TokensIn int64
	TokensOu int64
	Cache    int64
}

type Conversacion struct {
	ConversacionFila
	Mensajes []Mensaje
}

type ReservaFila struct {
	ID, Servicio, Cliente, Canal, Estado string
	Inicio, Creada                       time.Time
	EnCalendar                           bool
	CalendarError                        string
	Avisada, AvisadaCancel               bool
	AvisoError                           string
}

// Datos es lo que el panel lee de la base.
type Datos interface {
	Resumen(ctx context.Context, tenantID string, desde, inicioDiaLLM time.Time, claveLLM string) (Resumen, error)
	Conversaciones(ctx context.Context, tenantID string, limit int) ([]ConversacionFila, error)
	Conversacion(ctx context.Context, tenantID, id string) (Conversacion, error)
	Reservas(ctx context.Context, tenantID string, desde time.Time, limit int) ([]ReservaFila, error)
}

type Config struct {
	Datos    Datos
	TenantID string
	Negocio  string
	Loc      *time.Location

	// Cuota del LLM: la clave con que el limitador cuenta ("proveedor/modelo"), el
	// límite diario (0 = sin límite) y la zona del reinicio diario.
	ClaveLLM  string
	LimiteRPD int
	ZonaReset *time.Location

	// UltimoPoll, si no es nil, informa el último contacto exitoso con Telegram.
	UltimoPoll func() time.Time

	Usuario, Password string
	Log               *slog.Logger
	Now               func() time.Time
}

//go:embed templates/*.html
var templatesFS embed.FS

type Panel struct {
	cfg  Config
	tmpl map[string]*template.Template
}

func New(cfg Config) (*Panel, error) {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.ZonaReset == nil {
		cfg.ZonaReset = time.UTC
	}
	if cfg.Usuario == "" {
		cfg.Usuario = "admin"
	}
	p := &Panel{cfg: cfg, tmpl: map[string]*template.Template{}}
	funcs := template.FuncMap{
		"fecha":  func(t time.Time) string { return fechaCorta(t, cfg.Loc) },
		"hora":   func(t time.Time) string { return t.In(cfg.Loc).Format("15:04:05") },
		"hace":   func(t time.Time) string { return hace(t, cfg.Now()) },
		"miles":  miles,
		"pct":    pct,
		"nonulo": func(t time.Time) bool { return !t.IsZero() },
	}
	for _, page := range []string{"resumen", "conversaciones", "conversacion", "reservas"} {
		t, err := template.New("layout.html").Funcs(funcs).ParseFS(templatesFS, "templates/layout.html", "templates/"+page+".html")
		if err != nil {
			return nil, fmt.Errorf("plantilla %s: %w", page, err)
		}
		p.tmpl[page] = t
	}
	return p, nil
}

// Handler devuelve las rutas. /healthz no pide contraseña: lo usa Railway para saber
// si el proceso está arriba. Siempre responde 200 si el proceso atiende HTTP: no
// depende de Telegram, porque durante un redeploy la instancia nueva no puede leer
// de Telegram hasta que la vieja se apaga.
func (p *Panel) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintln(w, "ok")
	})
	if p.cfg.Password == "" {
		// Sin contraseña no se publica nada con datos.
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "panel desactivado: falta PANEL_PASSWORD", http.StatusNotFound)
		})
		return mux
	}
	mux.Handle("GET /{$}", p.auth(p.resumen))
	mux.Handle("GET /conversaciones", p.auth(p.conversaciones))
	mux.Handle("GET /conversaciones/{id}", p.auth(p.conversacion))
	mux.Handle("GET /reservas", p.auth(p.reservas))
	return mux
}

func (p *Panel) auth(h func(http.ResponseWriter, *http.Request) error) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, pw, ok := r.BasicAuth()
		// Se comparan los dos campos siempre, en tiempo constante.
		uOK := subtle.ConstantTimeCompare([]byte(u), []byte(p.cfg.Usuario))
		pOK := subtle.ConstantTimeCompare([]byte(pw), []byte(p.cfg.Password))
		if !ok || uOK&pOK != 1 {
			if ok {
				p.cfg.Log.Warn("panel: credenciales inválidas", "ip", r.RemoteAddr)
			}
			w.Header().Set("WWW-Authenticate", `Basic realm="Panel", charset="UTF-8"`)
			http.Error(w, "se requiere usuario y contraseña", http.StatusUnauthorized)
			return
		}
		hdr := w.Header()
		// Datos personales: que no queden en caches ni se puedan embeber en otro sitio.
		hdr.Set("Cache-Control", "no-store")
		hdr.Set("X-Frame-Options", "DENY")
		hdr.Set("X-Content-Type-Options", "nosniff")
		hdr.Set("Referrer-Policy", "no-referrer")
		hdr.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
		if err := h(w, r); err != nil {
			if errors.Is(err, ErrNoEncontrada) {
				http.Error(w, "no encontrada", http.StatusNotFound)
				return
			}
			p.cfg.Log.Error("panel", "path", r.URL.Path, "err", err)
			http.Error(w, "error interno", http.StatusInternalServerError)
		}
	})
}
