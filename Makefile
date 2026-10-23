# KubeAssist developer tasks. `make help` lists them.
SHELL := /bin/bash
KIND_CLUSTER ?= kubeassist-dev
API ?= http://localhost:8080
# Account `make agent-kind` registers clusters under (dev sign-in).
KUBEASSIST_USER ?= dev
COMPOSE_KIND := docker compose -f docker-compose.yml -f docker-compose.kind.yml

.PHONY: help
help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'

# ---- tests ---------------------------------------------------------------

.PHONY: test test-go test-ai test-web
test: test-go test-ai test-web ## Run every test suite

test-go: ## Go unit + contract tests (set TEST_DATABASE_URL for Postgres tests)
	cd backend-go && go vet ./... && go test -race ./...

test-ai: ai-service/.venv ## Python tests
	cd ai-service && .venv/bin/python -m pytest -q

test-web: frontend/node_modules ## Frontend typecheck + tests
	cd frontend && npm run typecheck && npm test

ai-service/.venv:
	cd ai-service && python3 -m venv .venv && .venv/bin/pip install -q -r requirements-dev.txt

frontend/node_modules:
	cd frontend && npm ci

# ---- run everything in containers --------------------------------------

.PHONY: up up-kind down logs
up: ## Full stack in Docker (relay mode; UI on :3000)
	docker compose up --build -d
	@echo "UI: http://localhost:3000   API: $(API)"

up-kind: .kube/kind-internal.yaml ## Full stack + direct access to your Kind cluster
	$(COMPOSE_KIND) up --build -d
	@echo "UI: http://localhost:3000   API: $(API)"

down: ## Stop the stack
	$(COMPOSE_KIND) down 2>/dev/null || docker compose down

logs: ## Tail backend + AI logs
	docker compose logs -f backend ai-service

.PHONY: .kube/kind-internal.yaml
.kube/kind-internal.yaml:
	@mkdir -p .kube
	kind get kubeconfig --internal --name $(KIND_CLUSTER) > $@

# ---- run services on the host (hot reload) ----------------------------

.PHONY: dev-backend dev-ai dev-web
dev-backend: ## Go backend on :8080 against your current kubeconfig (needs postgres+ai from `docker compose up -d postgres ai-service`)
	cd backend-go && KUBECONFIG=$${KUBECONFIG:-$$HOME/.kube/config} KUBE_CONTEXT=$${KUBE_CONTEXT:-kind-$(KIND_CLUSTER)} \
	  ENABLE_RELAY=true AUTH_DEV_LOGIN=true PUBLIC_URL=http://localhost:5173 AI_BASE_URL=http://localhost:8000 AI_AUTH_TOKEN=$${AI_SHARED_TOKEN:-dev-shared-token-change-me} \
	  DATABASE_URL=$${DATABASE_URL:-postgres://kubeassist:kubeassist_dev_password@localhost:5432/kubeassist?sslmode=disable} \
	  go run ./cmd/server

dev-ai: ai-service/.venv ## AI service on :8000 with reload
	cd ai-service && .venv/bin/uvicorn app.main:app --reload --port 8000

dev-web: frontend/node_modules ## Vite dev server on :5173 (proxies /api to :8080)
	cd frontend && npm run dev

# ---- Kind lab ------------------------------------------------------------

.PHONY: kind lab lab-rollout lab-clean agent-kind
kind: ## Create the Kind cluster if it doesn't exist
	@kind get clusters | grep -qx $(KIND_CLUSTER) || kind create cluster --name $(KIND_CLUSTER)

lab: ## Deploy the broken lab workloads (namespace kubeassist-lab)
	kubectl --context kind-$(KIND_CLUSTER) apply -f deploy/lab/

lab-rollout: ## Bad-rollout scenario: deploy a working revision, then a release that breaks it
	kubectl --context kind-$(KIND_CLUSTER) apply -f deploy/lab/00-namespace.yaml
	kubectl --context kind-$(KIND_CLUSTER) apply -f deploy/lab-rollout/v1.yaml
	kubectl --context kind-$(KIND_CLUSTER) -n kubeassist-lab rollout status deployment/bad-rollout --timeout=120s
	kubectl --context kind-$(KIND_CLUSTER) apply -f deploy/lab-rollout/v2.yaml

lab-clean: ## Remove the lab workloads
	kubectl --context kind-$(KIND_CLUSTER) delete namespace kubeassist-lab --ignore-not-found

agent-kind: ## Build the agent, load it into Kind, register "kind-dev" (as $(KUBEASSIST_USER)) and deploy it
	docker build -f backend-go/Dockerfile.agent -t kubeassist-agent:dev backend-go
	kind load docker-image kubeassist-agent:dev --name $(KIND_CLUSTER)
	@jar=$$(mktemp); trap 'rm -f "$$jar"' EXIT; \
	curl -fsS -c "$$jar" -X POST $(API)/api/auth/dev-login -H 'Content-Type: application/json' -d '{"login":"$(KUBEASSIST_USER)"}' >/dev/null || { \
	  echo "dev sign-in failed (the backend needs AUTH_DEV_LOGIN=true, as in docker-compose.yml)"; exit 1; }; \
	resp=$$(curl -fsS -b "$$jar" -X POST $(API)/api/clusters -H 'Content-Type: application/json' -d '{"clusterId":"kind-dev"}') || { \
	  echo "registration failed (already registered? revoke it in the UI as $(KUBEASSIST_USER), or DELETE /api/clusters/kind-dev)"; exit 1; }; \
	token=$$(echo "$$resp" | python3 -c 'import json,sys; print(json.load(sys.stdin)["token"])'); \
	kubectl --context kind-$(KIND_CLUSTER) apply -f deploy/agent/kind-dev.yaml; \
	kubectl --context kind-$(KIND_CLUSTER) -n kubeassist create secret generic kubeassist-agent-token \
	  --from-literal=AGENT_TOKEN=$$token --dry-run=client -o yaml | kubectl --context kind-$(KIND_CLUSTER) apply -f -; \
	kubectl --context kind-$(KIND_CLUSTER) -n kubeassist rollout restart deployment/kubeassist-agent; \
	echo; echo "Cluster kind-dev registered to account \"$(KUBEASSIST_USER)\"."; \
	echo "Open http://localhost:3000/connect and sign in as \"$(KUBEASSIST_USER)\" to use it."
