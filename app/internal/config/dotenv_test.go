package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDotEnvNoPisaElEntorno(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	os.WriteFile(path, []byte("# comentario\nDOTENV_A=desde_archivo\nDOTENV_B=\"con comillas\"\nexport DOTENV_C=exportada\nDOTENV_YA=archivo\n"), 0o600)
	t.Setenv("DOTENV_YA", "entorno")
	for _, k := range []string{"DOTENV_A", "DOTENV_B", "DOTENV_C"} {
		os.Unsetenv(k)
		t.Cleanup(func() { os.Unsetenv(k) })
	}
	if err := loadDotEnv(path); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"DOTENV_A": "desde_archivo", "DOTENV_B": "con comillas", "DOTENV_C": "exportada", "DOTENV_YA": "entorno"}
	for k, v := range want {
		if got := os.Getenv(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
	if err := loadDotEnv(filepath.Join(t.TempDir(), "no-existe")); err != nil {
		t.Errorf("un .env inexistente no es error: %v", err)
	}
}

func TestGoogleCredentials(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sa.json")
	os.WriteFile(path, []byte(`{"desde":"archivo"}`), 0o600)

	c := Config{GoogleCredentialsFile: path}
	if raw, err := c.GoogleCredentials(); err != nil || string(raw) != `{"desde":"archivo"}` {
		t.Errorf("archivo: %s, %v", raw, err)
	}
	c.GoogleCredentialsJSON = `{"desde":"variable"}`
	if raw, _ := c.GoogleCredentials(); string(raw) != `{"desde":"variable"}` {
		t.Errorf("GOOGLE_CREDENTIALS_JSON debe tener prioridad: %s", raw)
	}
	if raw, err := (Config{}).GoogleCredentials(); raw != nil || err != nil {
		t.Errorf("sin credenciales: %s, %v", raw, err)
	}
	if _, err := (Config{GoogleCredentialsFile: "/no/existe.json"}).GoogleCredentials(); err == nil {
		t.Error("un archivo inexistente debe ser error")
	}
}
