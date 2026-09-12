.PHONY: help setup up down logs seed dev dev-backend dev-frontend \
        docker-up docker-down docker-seed docker-logs docker-build \
        build test lint clean

help: ## Show available targets
	@grep -hE '^[a-z-]+:.*?## ' $(MAKEFILE_LIST) | awk -F':.*?## ' '{printf "  %-14s %s\n", $$1, $$2}'

setup: ## Install all dependencies
	cd frontend && npm install
	cd backend && go mod download

up: ## Start postgres/MinIO only (the dependencies `make dev` needs) and wait for health
	# Services are named explicitly rather than starting everything: a bare
	# `docker compose up -d` would also start the backend container, which binds
	# the same host port 8080 that `make dev` then tries to listen on.
	docker compose up -d postgres minio minio-init
	@echo "waiting for dependencies to become healthy..."
	@until [ "$$(docker compose ps --format '{{.Health}}' postgres minio 2>/dev/null | grep -cv healthy)" = "0" ]; do sleep 5; done
	@echo "all dependencies healthy"

down: ## Stop docker compose services
	docker compose down

logs: ## Follow docker compose logs
	docker compose logs -f

seed: ## Load sample documents and personas into the running stack (host Go)
	cd backend && go run ./cmd/seed

docker-build: ## Build the backend and frontend images
	docker compose build

docker-up: ## Run the whole stack in containers (frontend on :5173, API on :8080)
	docker compose up -d --build
	@echo "waiting for dependencies to become healthy..."
	@until [ "$$(docker compose ps --format '{{.Health}}' postgres minio 2>/dev/null | grep -cv healthy)" = "0" ]; do sleep 5; done
	@echo "stack up — open http://localhost:5173 (run 'make docker-seed' on first start)"

docker-seed: ## Load the sample fixtures using the seeder image
	docker compose run --rm seed

docker-logs: ## Follow backend and frontend container logs
	docker compose logs -f backend frontend

docker-down: ## Stop the whole containerised stack
	docker compose down

dev: up ## Run backend and frontend together (Ctrl-C stops both); starts docker compose first
	@trap 'kill 0' EXIT INT TERM; \
	$(MAKE) dev-backend & \
	$(MAKE) dev-frontend & \
	wait

dev-backend: ## Run the Go API on :8080
	cd backend && go run ./cmd/server

dev-frontend: ## Run the Vite dev server on :5173
	cd frontend && npm run dev

build: ## Build both the frontend bundle and the server binary
	cd frontend && npm run build
	cd backend && go build -o bin/server ./cmd/server

test: ## Run all tests
	cd backend && go test ./...

lint: ## Lint and vet
	cd frontend && npm run lint
	cd backend && go vet ./...

clean: ## Remove build output
	rm -rf frontend/dist backend/bin
