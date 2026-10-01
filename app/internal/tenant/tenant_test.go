package tenant

import "testing"

func TestLoadDemo(t *testing.T) {
	c, err := Load("../../tenants/barberia-demo.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := c.Servicio("corte DE pelo"); !ok {
		t.Error("Servicio debería ignorar mayúsculas")
	}
}

func TestParseErrores(t *testing.T) {
	casos := map[string]string{
		"sin servicios":   "slug: x\nnombre: X\n",
		"rango invertido": "slug: x\nnombre: X\nhorario: {lunes: ['14:00-10:00']}\nservicios: [{nombre: A, duracion_minutos: 30}]\n",
		"feriado mal":     "slug: x\nnombre: X\nferiados: ['25-12-2026']\nservicios: [{nombre: A, duracion_minutos: 30}]\n",
		"repetido":        "slug: x\nnombre: X\nservicios: [{nombre: A, duracion_minutos: 30}, {nombre: a, duracion_minutos: 30}]\n",
		"zona inválida":   "slug: x\nnombre: X\nzona_horaria: Marte/Olympus\nservicios: [{nombre: A, duracion_minutos: 30}]\n",
	}
	for nombre, yml := range casos {
		if _, err := Parse([]byte(yml)); err == nil {
			t.Errorf("%s: se esperaba error", nombre)
		}
	}
}
