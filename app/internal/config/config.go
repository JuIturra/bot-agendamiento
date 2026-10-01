// Package config lee la configuración del proceso desde variables de entorno.
package config

import (
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	DatabaseURL      string
	TelegramBotToken string
	TenantFile       string
	// LLMModel, si no está vacío, reemplaza el modelo_llm del negocio. Formato
	// "proveedor/modelo".
	LLMModel string
	// Credenciales de los proveedores de LLM. Solo se exige la del proveedor elegido.
	AnthropicAPIKey string
	OpenAIAPIKey    string
	GeminiAPIKey    string
	LLMBaseURL      string
	LLMAPIKey       string
	// Credenciales de la cuenta de servicio de Google, como ruta al JSON
	// (GOOGLE_APPLICATION_CREDENTIALS, cómodo en local) o como el JSON mismo
	// (GOOGLE_CREDENTIALS_JSON, para plataformas que solo tienen variables de entorno,
	// como Railway). Solo se exigen si el negocio tiene google_calendar_id.
	GoogleCredentialsFile string
	GoogleCredentialsJSON string
	// GoogleCalendarID, si no está vacío, reemplaza el google_calendar_id del negocio
	// (así un calendario personal no queda escrito en el YAML).
	GoogleCalendarID string
	// CodigoDueno es el secreto con que el dueño se registra para recibir avisos
	// ("/dueno <código>" al bot). Vacío = registro desactivado.
	CodigoDueno string
	// Límites de cuota del proveedor (0 = sin límite). Ver internal/llm/limit.
	LLMLimits LLMLimits
	LogLevel  slog.Level
}

type LLMLimits struct {
	RPM, TPM, RPD int
	// Zona en que se reinicia el contador diario (Gemini: America/Los_Angeles).
	ResetZone string
	MaxWait   time.Duration
}

// Load lee el entorno, completado con .env si existe en el directorio actual. Los
// campos listados en required deben tener valor.
func Load(required ...string) (Config, error) {
	if err := loadDotEnv(".env"); err != nil {
		return Config{}, err
	}
	c := Config{
		DatabaseURL:      os.Getenv("DATABASE_URL"),
		TelegramBotToken: os.Getenv("TELEGRAM_BOT_TOKEN"),
		TenantFile:       os.Getenv("TENANT_FILE"),
		LLMModel:         os.Getenv("LLM_MODEL"),
		AnthropicAPIKey:  os.Getenv("ANTHROPIC_API_KEY"),
		OpenAIAPIKey:     os.Getenv("OPENAI_API_KEY"),
		GeminiAPIKey:     os.Getenv("GEMINI_API_KEY"),
		LLMBaseURL:       os.Getenv("LLM_BASE_URL"),
		LLMAPIKey:        os.Getenv("LLM_API_KEY"),
		// Nombre estándar de Google: también lo leen sus herramientas (gcloud, SDKs).
		GoogleCredentialsFile: os.Getenv("GOOGLE_APPLICATION_CREDENTIALS"),
		GoogleCredentialsJSON: os.Getenv("GOOGLE_CREDENTIALS_JSON"),
		GoogleCalendarID:      os.Getenv("GOOGLE_CALENDAR_ID"),
		CodigoDueno:           os.Getenv("CODIGO_DUENO"),
		LLMLimits: LLMLimits{
			ResetZone: os.Getenv("LLM_LIMITE_ZONA_RESET"),
		},
	}
	var err error
	if c.LLMLimits.RPM, err = intEnv("LLM_LIMITE_RPM"); err != nil {
		return c, err
	}
	if c.LLMLimits.TPM, err = intEnv("LLM_LIMITE_TPM"); err != nil {
		return c, err
	}
	if c.LLMLimits.RPD, err = intEnv("LLM_LIMITE_RPD"); err != nil {
		return c, err
	}
	if v := os.Getenv("LLM_LIMITE_ESPERA_MAX"); v != "" {
		if c.LLMLimits.MaxWait, err = time.ParseDuration(v); err != nil {
			return c, fmt.Errorf("LLM_LIMITE_ESPERA_MAX: usar una duración como 90s: %w", err)
		}
	}
	if c.LLMLimits.ResetZone == "" {
		c.LLMLimits.ResetZone = "America/Los_Angeles"
	}
	if c.TenantFile == "" {
		c.TenantFile = "tenants/barberia-demo.yaml"
	}
	if err := c.LogLevel.UnmarshalText([]byte(strings.ToUpper(os.Getenv("LOG_LEVEL")))); err != nil || os.Getenv("LOG_LEVEL") == "" {
		c.LogLevel = slog.LevelInfo
	}
	var missing []string
	for _, k := range required {
		if os.Getenv(k) == "" {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		return c, fmt.Errorf("faltan variables de entorno: %s (ver .env.example)", strings.Join(missing, ", "))
	}
	return c, nil
}

func intEnv(k string) (int, error) {
	v := os.Getenv(k)
	if v == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("%s: se esperaba un entero >= 0, llegó %q", k, v)
	}
	return n, nil
}

// GoogleCredentials devuelve el JSON de la cuenta de servicio, o nil si no hay. Si
// están las dos variables, gana GOOGLE_CREDENTIALS_JSON.
func (c Config) GoogleCredentials() ([]byte, error) {
	if c.GoogleCredentialsJSON != "" {
		return []byte(c.GoogleCredentialsJSON), nil
	}
	if c.GoogleCredentialsFile == "" {
		return nil, nil
	}
	raw, err := os.ReadFile(c.GoogleCredentialsFile)
	if err != nil {
		return nil, fmt.Errorf("GOOGLE_APPLICATION_CREDENTIALS: %w", err)
	}
	return raw, nil
}
