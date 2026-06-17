# model-router

An OpenAI-compatible reverse proxy written in Go. It groups several identical
sglang/vllm backends under a single logical model name and enforces per-consumer
concurrency limits.

## Features

- OpenAI endpoints: `/v1/chat/completions`, `/v1/completions`, `/v1/embeddings`,
  `/v1/models` (lists only models with at least one healthy backend).
- Streaming (SSE) passthrough without buffering.
- Per-`(client, model)` concurrency control with queueing and `429` on timeout.
- Load balancing: `random`, `roundRobin`, `lessQueue`.
- Engine auto-detection (vllm / sglang) per backend.
- Health checks and queue-depth polling, with automatic backend rotation.
- mTLS client identification by certificate CN.
- Prometheus metrics at `/metrics`, OpenAPI docs at `/docs`.
- Graceful shutdown on SIGTERM/SIGINT.

## Quick start

```sh
make build
./bin/router --config ./config.yaml --port 8080 --log-level info
```

## Flags

| Flag          | Purpose                          | Default          |
|---------------|----------------------------------|------------------|
| `--port`      | TCP port to listen on            | `8080`           |
| `--host`      | Listen address                   | `0.0.0.0`        |
| `--log-level` | `debug`/`info`/`warn`/`error`    | `info`           |
| `--config`    | Path to the YAML config          | `./config.yaml`  |

See `config.yaml` for a complete configuration example.

## Layout

```
cmd/router/        entry point, flags, TLS, graceful shutdown
internal/config/   YAML loading and validation
internal/handler/  HTTP layer, OpenAI endpoints, /metrics, /docs
internal/service/  routing and concurrency control
internal/balancer/ random / roundRobin / lessQueue
internal/health/   engine detection, health and queue polling
internal/proxy/    streaming reverse proxy
internal/metrics/  Prometheus metrics
internal/auth/     client identification by CN
```

## Development

```sh
make test        # unit tests
make test-race   # race detector
make vet         # go vet
make lint        # golangci-lint
make docker      # build container image
```
