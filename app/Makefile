# Los programas leen .env por su cuenta (internal/config/dotenv.go), sin pisar
# variables ya definidas en el entorno. Por eso aquí no se incluye.

# Los tests de integración usan su propia base: si compartieran la de desarrollo, el
# bot real procesaría sus datos (p. ej. sincronizar con Calendar un negocio de test).
TEST_DATABASE_URL ?= postgres://agenda:agenda@localhost:5433/agenda_test?sslmode=disable

.PHONY: db-up db-down run chat test test-integration build gemini-modelos calendar-check docker-build docker-up docker-logs panel

db-up:
	docker compose up -d --wait db

db-down:
	docker compose down

run: ## bot de Telegram (long polling)
	go run ./cmd/server

chat: ## conversar con el bot desde la terminal, sin Telegram
	go run ./cmd/chat

test:
	go test ./...

test-integration: db-up
	@docker exec agendamiento_db psql -U agenda -d agenda -tAc "SELECT 1 FROM pg_database WHERE datname = 'agenda_test'" | grep -q 1 \
		|| docker exec agendamiento_db createdb -U agenda agenda_test
	TEST_DATABASE_URL=$(TEST_DATABASE_URL) go test -count=1 ./...

build:
	go build -o bin/server ./cmd/server

gemini-modelos: ## lista los modelos de Gemini disponibles para GEMINI_API_KEY
	@if [ -z "$$GEMINI_API_KEY" ] && [ -f .env ]; then set -a; . ./.env; set +a; fi; \
	test -n "$$GEMINI_API_KEY" || { echo "falta GEMINI_API_KEY en .env"; exit 1; }; \
	curl -sS -H "x-goog-api-key: $$GEMINI_API_KEY" \
		"https://generativelanguage.googleapis.com/v1beta/models?pageSize=100" \
		| grep -o '"name": "models/[^"]*"' | sed 's/.*models\///; s/"$$//' | sort

calendar-check: ## verifica la cuenta de servicio y el acceso al calendario
	go run ./cmd/calcheck

docker-build: ## construye la imagen que usa Railway
	docker build -t agendamiento .

docker-up: ## levanta base + bot en contenedores (como en Railway)
	docker compose --profile bot up -d --build

docker-logs:
	docker compose --profile bot logs -f bot

panel: ## solo el panel del operador, sin bot (http://127.0.0.1:8080)
	go run ./cmd/panel
