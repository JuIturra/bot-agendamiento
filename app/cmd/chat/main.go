// Chat por terminal contra el mismo motor que usa Telegram. Sirve para probar
// prompts y herramientas sin crear un bot. Cada ejecución guarda la conversación en
// la base como canal "consola".
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	_ "time/tzdata"

	"agendamiento/internal/bootstrap"
	"agendamiento/internal/config"
	"agendamiento/internal/engine"
)

func main() {
	usuario := flag.String("usuario", "local", "id del cliente simulado; cambiarlo empieza de cero")
	nombre := flag.String("nombre", "Cliente de prueba", "nombre de perfil del cliente simulado")
	flag.Parse()

	cfg, err := config.Load("DATABASE_URL")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	// Los logs van a stderr para no mezclarse con la conversación.
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: cfg.LogLevel}))
	ctx := context.Background()
	app, err := bootstrap.Build(ctx, cfg, log)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer app.Close()
	eng := app.Engine

	fmt.Printf("Chateando con %s. Ctrl+D para salir.\n\n", eng.Tenant.Nombre)
	in := bufio.NewScanner(os.Stdin)
	for {
		fmt.Print("tú> ")
		if !in.Scan() {
			fmt.Println()
			return
		}
		texto := strings.TrimSpace(in.Text())
		if texto == "" {
			continue
		}
		resp, err := eng.Handle(ctx, engine.Inbound{
			Canal: "consola", ExternalUserID: *usuario, Nombre: *nombre,
			Texto: texto, Enviado: time.Now(),
		})
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			continue
		}
		fmt.Printf("bot> %s\n\n", resp)
	}
}
