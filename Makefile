# Maître De Serveur developer entry points. See docs/v1-build-order.md.

GO      ?= go
CARGO   ?= cargo
PNPM    ?= pnpm
BUF     ?= buf
STATE   ?= $(HOME)/.maitre
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

.PHONY: help gen gen-check build test lint dev dev-trust info m0-check m1-check image image-boot clean

help:
	@grep -E '^[a-z0-9-]+:.*## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-12s %s\n", $$1, $$2}'

gen: ## Regenerate Go and TypeScript from api/
	cd api && $(BUF) lint && $(BUF) generate

gen-check: gen ## Fail if generated code is stale
	git diff --exit-code -- server/gen desktop/src/gen

build: server/bin/maitre ## Build the agent binary
server/bin/maitre: $(shell find server -name '*.go') server/go.mod
	cd server && $(GO) build -ldflags "-X main.version=$(VERSION)" -o bin/maitre ./cmd/maitre

test: ## Run Go, Rust, and TypeScript checks
	cd server && $(GO) vet ./... && $(GO) test ./...
	cd desktop && $(PNPM) exec tsc --noEmit && $(PNPM) test
	cd desktop/src-tauri && $(CARGO) test --lib

lint: ## gofmt and buf lint
	@test -z "$$(cd server && gofmt -l .)" || (cd server && gofmt -l . && exit 1)
	cd api && $(BUF) lint

dev: build ## Run a dev Node on 127.0.0.1:7443 using $(STATE)
	MAITRE_STATE=$(STATE) ./server/bin/maitre dev

dev-trust: ## Trust Maître Desktop's device key (DEVICE_PUB=path/to/identity.pub)
	@test -n "$(DEVICE_PUB)" || (echo "usage: make dev-trust DEVICE_PUB=~/.local/share/dev.maitre.desktop/device/identity.pub"; exit 1)
	mkdir -p $(STATE)/trusted && cp "$(DEVICE_PUB)" $(STATE)/trusted/desktop.pub
	@echo "trusted. Restart 'make dev'. Node fingerprint:"; ./server/bin/maitre keygen --out $(STATE)/node | awk '/^identity/{print $$2}'

info: build ## Call GetNodeInfo as the CLI device in $(STATE)/device
	MAITRE_STATE=$(STATE) ./server/bin/maitre info

m0-check: build ## End-to-end: Go dev Node <-> Rust client over mTLS, both directions of refusal
	./scripts/m0-check.sh ./server/bin/maitre

m1-check: build ## End-to-end: pair and provision a (fake-install) Installer, then start the agent from its config
	./scripts/m1-check.sh ./server/bin/maitre

image: build ## Build the Maître Server bootc image (needs podman)
	podman build -t localhost/maitre-server:$(VERSION) -f image/Containerfile .

image-boot: ## Boot the image in QEMU and wait for the agent (needs podman, bootc-image-builder, qemu)
	./image/boot-test.sh localhost/maitre-server:$(VERSION)

clean:
	rm -rf server/bin desktop/dist
