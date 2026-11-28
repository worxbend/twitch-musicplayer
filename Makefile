# Runs the CI gates locally (.github/workflows/ci.yml).
# `make check` covers the toolchains, lint, backend and frontend jobs.
# `make ui`, `make ui-prod` (Playwright) and `make docker-smoke` (image builds)
# are opt-in: they are heavy on a shared host. `make site` runs the Pages build and check
# (.github/workflows/pages.yml) and needs cwebp.

.DEFAULT_GOAL := check

SMOKE_BE_IMAGE ?= lofi-backend:smoke-local
SMOKE_FE_IMAGE ?= lofi-frontend:smoke-local
SMOKE_BE_PORT ?= 18081
SMOKE_FE_PORT ?= 13001

# $(call need,TOOL,HINT): fail with HINT instead of a cryptic shell error when
# TOOL is not on PATH.
need = @command -v $(1) >/dev/null 2>&1 || { echo "make: $(1) not found on PATH; $(2)" >&2; exit 127; }
NPM_HINT := install Node per frontend/.nvmrc, e.g. nvm use

check: toolchains lint backend frontend

toolchains:
	bash scripts/check-toolchains.sh

lint:
	bash scripts/lint.sh

backend:
	$(call need,go,install Go (see backend/go.mod toolchain))
	$(MAKE) -C backend check

# Same steps as the CI frontend job.
frontend:
	$(call need,npm,$(NPM_HINT))
	cd frontend && npm ci && npm audit --omit=dev --audit-level=high && npm run typecheck && npm test && npm run build && test -f dist/config.js

ui:
	$(call need,npm,$(NPM_HINT))
	cd frontend && npm run test:ui

# The @smoke subset against the production bundle (vite build + vite preview).
ui-prod:
	$(call need,npm,$(NPM_HINT))
	cd frontend && npm run test:ui:prod

# Builds both images, smokes them and the compose hardening on spare ports
# and removes the tags even when a step fails.
docker-smoke:
	$(call need,docker,install Docker with BuildKit)
	$(call need,jq,install jq)
	status=0; \
	docker build -t $(SMOKE_BE_IMAGE) backend \
	  && docker build -t $(SMOKE_FE_IMAGE) frontend \
	  && SMOKE_BE_PORT=$(SMOKE_BE_PORT) SMOKE_FE_PORT=$(SMOKE_FE_PORT) \
	     bash scripts/docker-smoke.sh $(SMOKE_BE_IMAGE) $(SMOKE_FE_IMAGE) \
	  && SMOKE_BE_PORT=$(SMOKE_BE_PORT) SMOKE_FE_PORT=$(SMOKE_FE_PORT) \
	     bash scripts/compose-smoke.sh $(SMOKE_BE_IMAGE) $(SMOKE_FE_IMAGE) \
	  || status=$$?; \
	docker rmi $(SMOKE_BE_IMAGE) $(SMOKE_FE_IMAGE) >/dev/null 2>&1 || true; \
	exit $$status

# Builds the product site into a temp dir like pages.yml, checks it and removes
# the dir even when a step fails.
site:
	$(call need,cwebp,install the webp package)
	$(call need,node,$(NPM_HINT))
	out=$$(mktemp -d); status=0; \
	bash scripts/site.sh build "$$out" && bash scripts/site.sh check "$$out" || status=$$?; \
	rm -rf "$$out"; \
	exit $$status

.PHONY: check toolchains lint backend frontend ui ui-prod docker-smoke site
