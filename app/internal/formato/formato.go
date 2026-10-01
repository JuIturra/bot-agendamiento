// Package formato convierte fechas y montos a texto en español de Chile.
package formato

import (
	"fmt"
	"strconv"
	"time"
)

var dias = [...]string{"domingo", "lunes", "martes", "miércoles", "jueves", "viernes", "sábado"}

// DiaSemana devuelve el nombre del día en minúsculas.
func DiaSemana(t time.Time) string { return dias[t.Weekday()] }

// Fecha devuelve "lunes 29-09-2026".
func Fecha(t time.Time) string {
	return fmt.Sprintf("%s %s", DiaSemana(t), t.Format("02-01-2006"))
}

// FechaHora devuelve "lunes 29-09-2026 a las 14:30".
func FechaHora(t time.Time) string {
	return fmt.Sprintf("%s a las %s", Fecha(t), t.Format("15:04"))
}

// Precio devuelve "$12.000".
func Precio(clp int) string {
	s := strconv.Itoa(clp)
	neg := clp < 0
	if neg {
		s = s[1:]
	}
	var out []byte
	for i, c := range []byte(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, '.')
		}
		out = append(out, c)
	}
	if neg {
		return "-$" + string(out)
	}
	return "$" + string(out)
}
