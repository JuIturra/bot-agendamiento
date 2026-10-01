package telegram

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

func upd(t *testing.T, js string) update {
	t.Helper()
	var u update
	if err := json.Unmarshal([]byte(js), &u); err != nil {
		t.Fatal(err)
	}
	return u
}

const base = `"date": 1790000000, "chat": {"id": 7, "type": "%s"}, "from": {"id": 9, "first_name": "Ana", "last_name": "Pérez"}`

func msg(tipo, extra string) string {
	return `{"update_id": 1, "message": {` + strings.Replace(base, "%s", tipo, 1) + extra + `}}`
}

func TestInterpretar(t *testing.T) {
	casos := []struct {
		nombre, js     string
		ok             bool
		texto, directa string
	}{
		{"texto normal", msg("private", `, "text": "hola, ¿tienen hora?"`), true, "hola, ¿tienen hora?", ""},
		{"/start", msg("private", `, "text": "/start"`), true, "Hola", ""},
		{"/start con deep link", msg("private", `, "text": "/start promo-octubre"`), true, "Hola", ""},
		{"/start@bot", msg("private", `, "text": "/start@BarberiaBot"`), true, "Hola", ""},
		{"/ayuda", msg("private", `, "text": "/ayuda"`), true, "¿En qué me puedes ayudar?", ""},
		{"foto con texto", msg("private", `, "caption": "¿cuánto sale este corte?", "photo": [{}]`), true, "¿cuánto sale este corte?", ""},
		{"audio", msg("private", `, "voice": {"duration": 3}`), false, "", msgSoloTexto},
		{"sticker", msg("private", `, "sticker": {}`), false, "", msgSoloTexto},
		{"grupo", msg("group", `, "text": "hola"`), false, "", ""},
		{"sin message", `{"update_id": 1}`, false, "", ""},
	}
	for _, c := range casos {
		m, directa, ok := interpretar(upd(t, c.js))
		if ok != c.ok || m.Texto != c.texto || directa != c.directa {
			t.Errorf("%s: ok=%v texto=%q directa=%q", c.nombre, ok, m.Texto, directa)
		}
		if ok && (m.Nombre != "Ana Pérez" || m.ChatID != 7 || m.UserID != 9) {
			t.Errorf("%s: datos del remitente = %+v", c.nombre, m)
		}
	}
}

func TestDividir(t *testing.T) {
	if got := dividir("corto", 4000); len(got) != 1 || got[0] != "corto" {
		t.Errorf("corto: %q", got)
	}
	largo := strings.Repeat("palabra ", 1200) // 9600 caracteres
	partes := dividir(largo, 4000)
	if len(partes) != 3 {
		t.Fatalf("partes = %d", len(partes))
	}
	for _, p := range partes {
		if utf8.RuneCountInString(p) > 4000 || strings.HasPrefix(p, "labra") {
			t.Errorf("parte mal cortada (%d runas): %q...", utf8.RuneCountInString(p), p[:20])
		}
	}
	// Caracteres multibyte: el límite es en caracteres, no en bytes.
	if got := dividir(strings.Repeat("ñ", 4001), 4000); len(got) != 2 {
		t.Errorf("ñ: partes = %d", len(got))
	}
}
