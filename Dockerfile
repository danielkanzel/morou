# syntax=docker/dockerfile:1

# --- build stage ---
FROM golang:1.23-alpine AS build
WORKDIR /src

# Cache dependencies first.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/router ./cmd/router

# --- runtime stage ---
FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app

COPY --from=build /out/router /app/router

EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/app/router"]
CMD ["--config", "/app/config.yaml"]
