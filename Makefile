.PHONY: help setup up down logs seed dev dev-discovery dev-persona-critic dev-persona-pragmatist dev-moderator dev-frontend \
        docker-build docker-up docker-down docker-seed docker-logs \
        kind-up kind-down kind-load k8s-up k8s-seed k8s-logs k8s-down \
        build test lint clean

help: ## Show available targets
	@grep -hE '^[a-zA-Z-]+:.*?## ' $(MAKEFILE_LIST) | awk -F':.*?## ' '{printf "  %-22s %s\n", $$1, $$2}'

setup: ## Install all dependencies
	cd frontend && npm install
	cd backend && go mod download

up: ## Start postgres/MinIO only (the dependencies `make dev` needs) and wait for health
	# Services are named explicitly rather than starting everything: a bare
	# `docker compose up -d` would also start the backend containers, which
	# bind the same host ports `make dev` then tries to listen on.
	docker compose up -d postgres minio minio-init
	@echo "waiting for dependencies to become healthy..."
	@until [ "$$(docker compose ps --format '{{.Health}}' postgres minio 2>/dev/null | grep -cv healthy)" = "0" ]; do sleep 5; done
	@echo "all dependencies healthy"

down: ## Stop docker compose services
	docker compose down

logs: ## Follow docker compose logs
	docker compose logs -f

seed: ## Load sample documents and personas into the running stack (host Go)
	cd backend && \
	POSTGRES_BASE_URL="postgres://postgres:kaigi@localhost:5432?sslmode=disable" \
	go run ./cmd/seed

docker-build: ## Build every service's image
	docker compose build
	docker compose build seed # profile-gated services are skipped by a bare `build` — see docker-compose.yml's seed comment

docker-up: ## Run the whole stack in containers (frontend on :5173, moderator on :8080)
	docker compose up -d --build
	@echo "waiting for dependencies to become healthy..."
	@until [ "$$(docker compose ps --format '{{.Health}}' postgres minio 2>/dev/null | grep -cv healthy)" = "0" ]; do sleep 5; done
	@echo "stack up — open http://localhost:5173 (run 'make docker-seed' on first start)"

docker-seed: ## Load the sample fixtures using the seeder image
	docker compose build seed
	docker compose run --rm seed

docker-logs: ## Follow every backend/frontend container's logs
	docker compose logs -f discovery persona-critic persona-pragmatist moderator frontend

docker-down: ## Stop the whole containerised stack
	docker compose down

dev: up ## Run discovery/persona/moderator/frontend together (Ctrl-C stops all); starts docker compose first
	@trap 'kill 0' EXIT INT TERM; \
	$(MAKE) dev-discovery & \
	sleep 2; \
	$(MAKE) dev-persona-critic & \
	$(MAKE) dev-persona-pragmatist & \
	$(MAKE) dev-moderator & \
	$(MAKE) dev-frontend & \
	wait

# All dev-* backend targets share the same POSTGRES_BASE_URL/REGISTRY_URL —
# only ADDR (and PERSONA_SLUG/A2A_PUBLIC_URL for personas) differs per
# process. Ports: moderator 8080, discovery 8081, persona-critic 8082,
# persona-pragmatist 8083, Vite 5173.
dev-discovery: ## Run the discovery pod's Go binary on :8081
	cd backend && \
	POSTGRES_BASE_URL="postgres://postgres:kaigi@localhost:5432?sslmode=disable" \
	ADDR=":8081" \
	go run ./cmd/discovery

dev-persona-critic: ## Run the critic persona's Go binary on :8082
	cd backend && \
	POSTGRES_BASE_URL="postgres://postgres:kaigi@localhost:5432?sslmode=disable" \
	ADDR=":8082" PERSONA_SLUG="critic" A2A_PUBLIC_URL="http://localhost:8082" \
	go run ./cmd/persona

dev-persona-pragmatist: ## Run the pragmatist persona's Go binary on :8083
	cd backend && \
	POSTGRES_BASE_URL="postgres://postgres:kaigi@localhost:5432?sslmode=disable" \
	ADDR=":8083" PERSONA_SLUG="pragmatist" A2A_PUBLIC_URL="http://localhost:8083" \
	go run ./cmd/persona

dev-moderator: ## Run the moderator's Go binary on :8080
	cd backend && \
	POSTGRES_BASE_URL="postgres://postgres:kaigi@localhost:5432?sslmode=disable" \
	ADDR=":8080" \
	go run ./cmd/moderator

dev-frontend: ## Run the Vite dev server on :5173
	cd frontend && npm run dev

build: ## Build the frontend bundle and every backend binary
	cd frontend && npm run build
	cd backend && \
	go build -o bin/moderator ./cmd/moderator && \
	go build -o bin/discovery ./cmd/discovery && \
	go build -o bin/persona ./cmd/persona && \
	go build -o bin/seed ./cmd/seed

test: ## Run all tests
	cd backend && go test ./...

lint: ## Lint and vet
	cd frontend && npm run lint
	cd backend && go vet ./...

clean: ## Remove build output
	rm -rf frontend/dist backend/bin

# --- Kubernetes (kind) ---
#
# `kind` must be installed first: https://kind.sigs.k8s.io/docs/user/quick-start/#installation
# Requires k8s/overlays/local/secret-patch.yaml — copy secret-patch.yaml.example
# and fill in a real OPENAI_API_KEY first (see that file's own comment).

kind-up: ## Create the kind cluster and install ingress-nginx
	kind create cluster --config k8s/kind-cluster.yaml --name kaigi
	kubectl apply -f https://raw.githubusercontent.com/kubernetes/ingress-nginx/main/deploy/static/provider/kind/deploy.yaml
	kubectl wait --namespace ingress-nginx --for=condition=ready pod \
		--selector=app.kubernetes.io/component=controller --timeout=180s

kind-load: ## Build backend/frontend images and load them into the kind cluster
	docker build -t kaigi-backend:local ./backend
	docker build -t kaigi-frontend:local ./frontend
	kind load docker-image kaigi-backend:local --name kaigi
	kind load docker-image kaigi-frontend:local --name kaigi

k8s-up: ## Apply the local overlay and wait for pods to be ready
	@test -f k8s/overlays/local/secret-patch.yaml || \
		(echo "missing k8s/overlays/local/secret-patch.yaml — copy secret-patch.yaml.example and set OPENAI_API_KEY" && exit 1)
	kubectl apply -k k8s/overlays/local
	@echo "waiting for postgres/minio..."
	kubectl -n kaigi wait --for=condition=ready pod -l app=postgres --timeout=120s
	kubectl -n kaigi wait --for=condition=ready pod -l app=minio --timeout=120s
	@echo "stack applied — open http://kaigi.localtest.me (run 'make k8s-seed' on first start)"

k8s-seed: ## Run the seed Job against the kind cluster (deletes any previous run first — Jobs are immutable)
	kubectl -n kaigi delete job seed --ignore-not-found
	sed -e 's|image: kaigi-backend|image: kaigi-backend:local\n          imagePullPolicy: Never|' \
		k8s/base/seed-job.yaml | kubectl apply -n kaigi -f -
	kubectl -n kaigi wait --for=condition=complete job/seed --timeout=120s
	kubectl -n kaigi logs job/seed

k8s-logs: ## Follow discovery/persona/moderator/frontend pod logs
	kubectl -n kaigi logs -f -l 'app in (discovery,persona-critic,persona-pragmatist,frontend)' --all-containers --prefix

k8s-down: ## Delete the kaigi namespace (keeps the kind cluster itself)
	kubectl delete namespace kaigi --ignore-not-found

kind-down: ## Delete the kind cluster entirely
	kind delete cluster --name kaigi
