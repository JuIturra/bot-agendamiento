package formato

import (
	"testing"
	"time"
)

func TestPrecio(t *testing.T) {
	for in, want := range map[int]string{0: "$0", 500: "$500", 12000: "$12.000", 1250000: "$1.250.000"} {
		if got := Precio(in); got != want {
			t.Errorf("Precio(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestFechaHora(t *testing.T) {
	d := time.Date(2026, 9, 30, 9, 5, 0, 0, time.UTC)
	if got, want := FechaHora(d), "miércoles 30-09-2026 a las 09:05"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
