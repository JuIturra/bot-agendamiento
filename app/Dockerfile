# Imagen del bot. Se construye con el contexto en app/:
#   docker build -t agendamiento .
FROM golang:1.26 AS build
WORKDIR /src
# Dependencias primero, para aprovechar la cache de capas.
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server

# distroless/static trae certificados CA y nada más (ni shell). Las zonas horarias
# van dentro del binario (import _ "time/tzdata" en cmd/server).
FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=build /out/server /app/server
COPY tenants /app/tenants
USER nonroot:nonroot
ENTRYPOINT ["/app/server"]
