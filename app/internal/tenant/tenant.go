// Package tenant define la configuración de un negocio y la carga desde YAML.
package tenant

import (
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config es todo lo que el bot sabe de un negocio. Se escribe en YAML y se guarda
// como JSONB en tenants.config.
type Config struct {
	Slug        string `yaml:"slug" json:"slug"`
	Nombre      string `yaml:"nombre" json:"nombre"`
	ZonaHoraria string `yaml:"zona_horaria" json:"zona_horaria"`
	ModeloLLM   string `yaml:"modelo_llm" json:"modelo_llm"`
	// GoogleCalendarID es el calendario del dueño, compartido con la cuenta de
	// servicio. Vacío = sin integración con Calendar.
	GoogleCalendarID string `yaml:"google_calendar_id" json:"google_calendar_id"`

	Descripcion string   `yaml:"descripcion" json:"descripcion"`
	Direccion   string   `yaml:"direccion" json:"direccion"`
	Telefono    string   `yaml:"telefono" json:"telefono"`
	MediosPago  []string `yaml:"medios_pago" json:"medios_pago"`

	Horario                   Horario  `yaml:"horario" json:"horario"`
	IntervaloMinutos          int      `yaml:"intervalo_minutos" json:"intervalo_minutos"`
	BufferMinutos             int      `yaml:"buffer_minutos" json:"buffer_minutos"`
	AnticipacionMinimaMinutos int      `yaml:"anticipacion_minima_minutos" json:"anticipacion_minima_minutos"`
	DiasMaximosAdelanto       int      `yaml:"dias_maximos_adelanto" json:"dias_maximos_adelanto"`
	Feriados                  []string `yaml:"feriados" json:"feriados"`

	Servicios           []Servicio `yaml:"servicios" json:"servicios"`
	PreguntasFrecuentes []FAQ      `yaml:"preguntas_frecuentes" json:"preguntas_frecuentes"`
	InstruccionesExtra  string     `yaml:"instrucciones_extra" json:"instrucciones_extra"`

	loc      *time.Location
	feriados map[string]bool
	horario  [7][]Rango // indexado por time.Weekday
}

// Horario lista los rangos de atención por día, en formato "HH:MM-HH:MM".
// Las claves van sin tilde: miercoles, sabado.
type Horario struct {
	Lunes     []string `yaml:"lunes" json:"lunes"`
	Martes    []string `yaml:"martes" json:"martes"`
	Miercoles []string `yaml:"miercoles" json:"miercoles"`
	Jueves    []string `yaml:"jueves" json:"jueves"`
	Viernes   []string `yaml:"viernes" json:"viernes"`
	Sabado    []string `yaml:"sabado" json:"sabado"`
	Domingo   []string `yaml:"domingo" json:"domingo"`
}

type Servicio struct {
	Nombre          string `yaml:"nombre" json:"nombre"`
	DuracionMinutos int    `yaml:"duracion_minutos" json:"duracion_minutos"`
	Precio          int    `yaml:"precio" json:"precio"`
	Descripcion     string `yaml:"descripcion" json:"descripcion"`
}

type FAQ struct {
	Pregunta  string `yaml:"pregunta" json:"pregunta"`
	Respuesta string `yaml:"respuesta" json:"respuesta"`
}

// Rango es un tramo de atención dentro de un día, en minutos desde medianoche.
type Rango struct {
	Desde, Hasta int
}

// Load lee un archivo YAML de negocio.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	c, err := Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

// Parse interpreta un YAML de negocio, aplica valores por defecto y valida.
func Parse(raw []byte) (*Config, error) {
	var c Config
	if err := yaml.Unmarshal(raw, &c); err != nil {
		return nil, err
	}
	if err := c.init(); err != nil {
		return nil, err
	}
	return &c, nil
}

func (c *Config) init() error {
	if c.Slug == "" || c.Nombre == "" {
		return fmt.Errorf("slug y nombre son obligatorios")
	}
	if c.ZonaHoraria == "" {
		c.ZonaHoraria = "America/Santiago"
	}
	if c.IntervaloMinutos <= 0 {
		c.IntervaloMinutos = 30
	}
	if c.DiasMaximosAdelanto <= 0 {
		c.DiasMaximosAdelanto = 30
	}
	loc, err := time.LoadLocation(c.ZonaHoraria)
	if err != nil {
		return fmt.Errorf("zona_horaria: %w", err)
	}
	c.loc = loc

	c.feriados = map[string]bool{}
	for _, f := range c.Feriados {
		if _, err := time.Parse(time.DateOnly, f); err != nil {
			return fmt.Errorf("feriado %q: usar AAAA-MM-DD", f)
		}
		c.feriados[f] = true
	}

	dias := map[time.Weekday][]string{
		time.Monday: c.Horario.Lunes, time.Tuesday: c.Horario.Martes,
		time.Wednesday: c.Horario.Miercoles, time.Thursday: c.Horario.Jueves,
		time.Friday: c.Horario.Viernes, time.Saturday: c.Horario.Sabado,
		time.Sunday: c.Horario.Domingo,
	}
	for wd, rangos := range dias {
		for _, s := range rangos {
			r, err := parseRango(s)
			if err != nil {
				return fmt.Errorf("horario %s: %w", wd, err)
			}
			c.horario[wd] = append(c.horario[wd], r)
		}
	}

	if len(c.Servicios) == 0 {
		return fmt.Errorf("hay que definir al menos un servicio")
	}
	vistos := map[string]bool{}
	for _, s := range c.Servicios {
		if s.Nombre == "" || s.DuracionMinutos <= 0 {
			return fmt.Errorf("servicio %q: nombre y duracion_minutos son obligatorios", s.Nombre)
		}
		k := strings.ToLower(s.Nombre)
		if vistos[k] {
			return fmt.Errorf("servicio %q repetido", s.Nombre)
		}
		vistos[k] = true
	}
	return nil
}

func parseRango(s string) (Rango, error) {
	desde, hasta, ok := strings.Cut(strings.ReplaceAll(s, " ", ""), "-")
	if !ok {
		return Rango{}, fmt.Errorf("%q: usar HH:MM-HH:MM", s)
	}
	d, err1 := parseHora(desde)
	h, err2 := parseHora(hasta)
	if err1 != nil || err2 != nil || d >= h {
		return Rango{}, fmt.Errorf("%q: usar HH:MM-HH:MM con inicio < fin", s)
	}
	return Rango{Desde: d, Hasta: h}, nil
}

func parseHora(s string) (int, error) {
	t, err := time.Parse("15:04", s)
	if err != nil {
		return 0, err
	}
	return t.Hour()*60 + t.Minute(), nil
}

// Location devuelve la zona horaria del negocio.
func (c *Config) Location() *time.Location { return c.loc }

// RangosDelDia devuelve los tramos de atención de una fecha, o nil si no se atiende
// (día sin horario o feriado).
func (c *Config) RangosDelDia(fecha time.Time) []Rango {
	if c.feriados[fecha.Format(time.DateOnly)] {
		return nil
	}
	return c.horario[fecha.Weekday()]
}

// Servicio busca un servicio por nombre, sin distinguir mayúsculas.
func (c *Config) Servicio(nombre string) (Servicio, bool) {
	for _, s := range c.Servicios {
		if strings.EqualFold(s.Nombre, strings.TrimSpace(nombre)) {
			return s, true
		}
	}
	return Servicio{}, false
}

// NombresServicios devuelve los nombres en el orden del YAML (estable, para el cache
// de prompts).
func (c *Config) NombresServicios() []string {
	out := make([]string, len(c.Servicios))
	for i, s := range c.Servicios {
		out[i] = s.Nombre
	}
	return out
}
