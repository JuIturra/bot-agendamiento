// Package telegram conecta el bot con Telegram por long polling (getUpdates), sin
// exponer puertos. Sirve para desarrollo local; en producción se usará webhook.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const pollTimeout = 30 // segundos que Telegram mantiene abierta cada getUpdates

type Message struct {
	ChatID  int64
	UserID  int64
	Nombre  string
	Texto   string
	Enviado time.Time
}

// Handler procesa un mensaje y devuelve la respuesta ("" para no responder).
type Handler func(ctx context.Context, m Message) (string, error)

type Bot struct {
	token  string
	http   *http.Client
	log    *slog.Logger
	offset int64
	// ultimoPoll es la hora (unix nano) del último getUpdates exitoso: indica si el
	// bot sigue conectado a Telegram.
	ultimoPoll atomic.Int64
}

// UltimoPoll devuelve la hora del último getUpdates exitoso (cero si no hubo).
func (b *Bot) UltimoPoll() time.Time {
	n := b.ultimoPoll.Load()
	if n == 0 {
		return time.Time{}
	}
	return time.Unix(0, n)
}

func New(token string, log *slog.Logger) *Bot {
	return &Bot{
		token: token,
		http:  &http.Client{Timeout: (pollTimeout + 10) * time.Second},
		log:   log,
	}
}

// Me devuelve el username del bot; sirve para validar el token al arrancar.
func (b *Bot) Me(ctx context.Context) (string, error) {
	var me struct {
		Username string `json:"username"`
	}
	err := b.call(ctx, "getMe", nil, &me)
	return me.Username, err
}

// Run recibe mensajes hasta que ctx se cancela. Los mensajes de un mismo chat se
// procesan en orden, uno a la vez; chats distintos se procesan en paralelo.
func (b *Bot) Run(ctx context.Context, h Handler) error {
	var mu sync.Mutex
	colas := map[int64]chan Message{}
	var wg sync.WaitGroup
	defer wg.Wait()

	encolar := func(m Message) {
		mu.Lock()
		defer mu.Unlock()
		q, ok := colas[m.ChatID]
		if !ok {
			q = make(chan Message, 32)
			colas[m.ChatID] = q
			wg.Add(1)
			go func() {
				defer wg.Done()
				for {
					select {
					case <-ctx.Done():
						return
					case m := <-q:
						b.procesar(ctx, h, m)
					}
				}
			}()
		}
		select {
		case q <- m:
		default:
			b.log.Warn("cola llena, se descarta mensaje", "chat", m.ChatID)
		}
	}

	for ctx.Err() == nil {
		updates, err := b.getUpdates(ctx)
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			if strings.Contains(err.Error(), "Conflict") {
				b.log.Error("getUpdates: otro proceso está usando este bot (¿otro `make run` abierto, o un webhook configurado?). Usa un bot distinto para desarrollo", "err", err)
			} else {
				b.log.Error("getUpdates", "err", err)
			}
			select {
			case <-time.After(3 * time.Second):
			case <-ctx.Done():
			}
			continue
		}
		b.ultimoPoll.Store(time.Now().UnixNano())
		for _, u := range updates {
			// El offset confirma el update a Telegram: no se vuelve a recibir.
			b.offset = u.UpdateID + 1
			m, directa, ok := interpretar(u)
			switch {
			case directa != "":
				if err := b.Send(ctx, m.ChatID, directa); err != nil {
					b.log.Error("sendMessage", "chat", m.ChatID, "err", err)
				}
			case ok:
				encolar(m)
			}
		}
	}
	return ctx.Err()
}

func (b *Bot) procesar(ctx context.Context, h Handler, m Message) {
	stop := b.typing(ctx, m.ChatID)
	resp, err := h(ctx, m)
	stop()
	if err != nil {
		b.log.Error("handler", "chat", m.ChatID, "err", err)
		resp = "Disculpa, tuve un problema. ¿Me escribes de nuevo en un momento?"
	}
	if resp == "" {
		return
	}
	if err := b.Send(ctx, m.ChatID, resp); err != nil {
		b.log.Error("sendMessage", "chat", m.ChatID, "err", err)
	}
}

// typing muestra "escribiendo..." mientras se procesa. Telegram lo apaga a los 5 s,
// así que se repite.
func (b *Bot) typing(ctx context.Context, chatID int64) (stop func()) {
	ctx, cancel := context.WithCancel(ctx)
	go func() {
		t := time.NewTicker(4 * time.Second)
		defer t.Stop()
		for {
			_ = b.call(ctx, "sendChatAction", map[string]any{"chat_id": chatID, "action": "typing"}, nil)
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
	return cancel
}

// Send envía texto plano. Si supera el máximo de Telegram, lo divide en varios
// mensajes.
func (b *Bot) Send(ctx context.Context, chatID int64, text string) error {
	for _, parte := range dividir(text, maxMensaje) {
		if err := b.call(ctx, "sendMessage", map[string]any{"chat_id": chatID, "text": parte}, nil); err != nil {
			return err
		}
	}
	return nil
}

// Telegram acepta hasta 4096 caracteres por mensaje; se deja margen.
const maxMensaje = 4000

// Respuestas que no pasan por el modelo.
const msgSoloTexto = "Por ahora solo puedo leer mensajes de texto. ¿Me lo escribes, por favor?"

// interpretar convierte un update en un mensaje para el motor. Devuelve directa si
// hay que responder algo fijo sin llamar al motor, y ok=false si el update se ignora.
func interpretar(u update) (m Message, directa string, ok bool) {
	tm := u.Message
	// Solo chats privados: en grupos el bot no debe participar.
	if tm == nil || tm.Chat.Type != "private" {
		return Message{}, "", false
	}
	nombre := strings.TrimSpace(tm.From.FirstName + " " + tm.From.LastName)
	m = Message{ChatID: tm.Chat.ID, UserID: tm.From.ID, Nombre: nombre, Enviado: time.Unix(tm.Date, 0)}

	texto := strings.TrimSpace(tm.Text)
	if texto == "" {
		// Foto o documento con texto al pie: se usa el texto.
		texto = strings.TrimSpace(tm.Caption)
	}
	if texto == "" {
		// Audio, sticker, ubicación, foto sin texto...
		return m, msgSoloTexto, false
	}
	// Comandos de Telegram. /start es lo primero que se envía al abrir el bot; puede
	// traer un parámetro de deep link ("/start promo"), que se ignora.
	if strings.HasPrefix(texto, "/") {
		comando, _, _ := strings.Cut(strings.TrimPrefix(texto, "/"), " ")
		comando, _, _ = strings.Cut(comando, "@") // "/start@MiBot" en algunos clientes
		switch strings.ToLower(comando) {
		case "start":
			texto = "Hola"
		case "help", "ayuda":
			texto = "¿En qué me puedes ayudar?"
		}
	}
	m.Texto = texto
	return m, "", true
}

// dividir parte text en trozos de hasta max caracteres, cortando en saltos de línea
// o espacios cuando se puede.
func dividir(text string, max int) []string {
	runes := []rune(text)
	var out []string
	for len(runes) > max {
		corte := max
		for i := max; i > max/2; i-- {
			if runes[i] == '\n' || runes[i] == ' ' {
				corte = i
				break
			}
		}
		out = append(out, strings.TrimSpace(string(runes[:corte])))
		runes = runes[corte:]
	}
	if rest := strings.TrimSpace(string(runes)); rest != "" || len(out) == 0 {
		out = append(out, rest)
	}
	return out
}

type update struct {
	UpdateID int64 `json:"update_id"`
	Message  *struct {
		Date    int64  `json:"date"`
		Text    string `json:"text"`
		Caption string `json:"caption"`
		Chat    struct {
			ID   int64  `json:"id"`
			Type string `json:"type"`
		} `json:"chat"`
		From struct {
			ID        int64  `json:"id"`
			FirstName string `json:"first_name"`
			LastName  string `json:"last_name"`
		} `json:"from"`
	} `json:"message"`
}

func (b *Bot) getUpdates(ctx context.Context) ([]update, error) {
	var out []update
	err := b.call(ctx, "getUpdates", map[string]any{
		"offset": b.offset, "timeout": pollTimeout, "allowed_updates": []string{"message"},
	}, &out)
	return out, err
}

func (b *Bot) call(ctx context.Context, method string, body any, result any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	endpoint := "https://api.telegram.org/bot" + b.token + "/" + method
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := b.http.Do(req)
	if err != nil {
		// El error de net/http incluye la URL, y la URL incluye el token: se loguea
		// solo la causa.
		var uerr *url.Error
		if errors.As(err, &uerr) {
			return fmt.Errorf("%s: %w", method, uerr.Err)
		}
		return fmt.Errorf("%s: error de red", method)
	}
	defer resp.Body.Close()
	var env struct {
		OK          bool            `json:"ok"`
		Result      json.RawMessage `json:"result"`
		Description string          `json:"description"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return fmt.Errorf("%s: respuesta inválida (HTTP %s)", method, strconv.Itoa(resp.StatusCode))
	}
	if !env.OK {
		return fmt.Errorf("%s: %s", method, env.Description)
	}
	if result != nil {
		return json.Unmarshal(env.Result, result)
	}
	return nil
}
