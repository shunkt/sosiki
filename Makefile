.PHONY: help setup dev dev-backend dev-frontend build test lint clean

help: ## Show available targets
	@grep -hE '^[a-z-]+:.*?## ' $(MAKEFILE_LIST) | awk -F':.*?## ' '{printf "  %-14s %s\n", $$1, $$2}'

setup: ## Install all dependencies
	cd frontend && npm install
	cd backend && go mod download

dev: ## Run backend and frontend together (Ctrl-C stops both)
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
