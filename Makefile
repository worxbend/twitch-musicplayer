# Runs the CI gates locally (.github/workflows/ci.yml).
# `make check` covers the toolchains, lint, backend and frontend jobs.
# `make ui` (Playwright) and `make docker-smoke` (image builds) are opt-in:
# both are heavy on a shared host.

.DEFAULT_GOAL := check

SMOKE_BE_IMAGE ?= lofi-backend:smoke-local
SMOKE_FE_IMAGE ?= lofi-frontend:smoke-local
SMOKE_BE_PORT ?= 18081
SMOKE_FE_PORT ?= 13001

check: toolchains lint backend frontend

toolchains:
	bash scripts/check-toolchains.sh

lint:
	bash scripts/lint.sh

backend:
	$(MAKE) -C backend check

# Same steps as the CI frontend job.
frontend:
	cd frontend && npm ci && npm audit --omit=dev --audit-level=high && npm run typecheck && npm test && npm run build && test -f dist/config.js

ui:
	cd frontend && npm run test:ui

# Builds both images, smokes them on spare ports and removes the tags even
# when a step fails.
docker-smoke:
	status=0; \
	docker build -t $(SMOKE_BE_IMAGE) backend \
	  && docker build -t $(SMOKE_FE_IMAGE) frontend \
	  && SMOKE_BE_PORT=$(SMOKE_BE_PORT) SMOKE_FE_PORT=$(SMOKE_FE_PORT) \
	     bash scripts/docker-smoke.sh $(SMOKE_BE_IMAGE) $(SMOKE_FE_IMAGE) \
	  || status=$$?; \
	docker rmi $(SMOKE_BE_IMAGE) $(SMOKE_FE_IMAGE) >/dev/null 2>&1 || true; \
	exit $$status

.PHONY: check toolchains lint backend frontend ui docker-smoke
