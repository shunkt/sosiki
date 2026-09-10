# kaigi

Vite + React (TypeScript) frontend with a Go backend.

## Layout

```
frontend/            Vite + React + TypeScript
  src/api/client.ts  typed fetch wrappers for the backend
  vite.config.ts     dev server, proxies /api -> :8080
backend/
  cmd/server/        main: config, HTTP server, graceful shutdown
  internal/api/      routes, handlers, CORS + request logging
  internal/config/   environment-based settings
```

## Requirements

- Node 20+ (built with 26) and npm
- Go 1.22+ (built with 1.26) — the router uses `net/http` method patterns

## Getting started

```sh
make setup   # npm install + go mod download
make dev     # Go API on :8080, Vite on :5173
```

Open http://localhost:5173. The page shows backend health and can call `/api/hello`.

Run the two sides separately with `make dev-backend` / `make dev-frontend`.

## API

| Method | Path          | Response                        |
| ------ | ------------- | ------------------------------- |
| GET    | `/api/health` | `{"status":"ok"}`               |
| GET    | `/api/hello`  | `{"message":"hello, <name>"}` — optional `?name=` |

## Other commands

```sh
make test    # go test ./...
make lint    # oxlint + go vet
make build   # frontend/dist + backend/bin/server
make clean
```

## Configuration

Copy `.env.example` to `.env` for reference; values are read from the
environment, not from the file.

| Variable                | Default                  | Used by  |
| ----------------------- | ------------------------ | -------- |
| `ADDR`                  | `:8080`                  | backend  |
| `ALLOWED_ORIGINS`       | `http://localhost:5173`  | backend  |
| `VITE_API_PROXY_TARGET` | `http://localhost:8080`  | frontend |

In development the Vite proxy means the browser only sees one origin, so CORS
is not exercised; `ALLOWED_ORIGINS` matters when the frontend is served from a
different host in production.
