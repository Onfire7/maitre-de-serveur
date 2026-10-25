# Maître De Serveur developer entry points. See docs/v1-build-order.md.

GO      ?= go
CARGO   ?= cargo
PNPM    ?= pnpm
BUF     ?= buf
STATE   ?= $(HOME)/.maitre
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
TAG     ?= dev

.PHONY: help gen gen-check build sidecar test lint dev dev-trust vm desktop info m0-check m1-check tunnel-check image image-boot publish depot clean

help:
	@grep -E '^[a-z0-9-]+:.*## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-12s %s\n", $$1, $$2}'

gen: ## Regenerate Go and TypeScript from api/, and the Catalog index the Desktop bundles (decision 78)
	cd api && $(BUF) lint && $(BUF) generate
	$(MAKE) build
	./server/bin/maitre catalog-index --out desktop/src/catalog/index.json catalog/entries

gen-check: gen ## Fail if generated code is stale
	git diff --exit-code -- server/gen desktop/src/gen desktop/src/catalog

build: server/bin/maitre ## Build the agent binary
server/bin/maitre: $(shell find server -name '*.go') server/go.mod
	cd server && $(GO) build -ldflags "-X main.version=$(VERSION)" -o bin/maitre ./cmd/maitre

test: sidecar ## Run Go, Rust, and TypeScript checks
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

vm: build ## Boot Maître Server in QEMU from CI's installer media; Ctrl-C stops it (FRESH=1 wipes the VM disk)
	FRESH=$(FRESH) RUN=$(RUN) ./scripts/vm.sh

TRIPLE  := $(shell rustc -vV 2>/dev/null | sed -n 's/^host: //p')

sidecar: build ## Place the maitre binary where the Desktop bundles it as its netboot helper (Tauri needs it to compile at all)
	mkdir -p desktop/src-tauri/binaries && cp server/bin/maitre desktop/src-tauri/binaries/maitre-$(TRIPLE)$(if $(findstring windows,$(TRIPLE)),.exe)

desktop: sidecar ## Run Maître Desktop with live reload
	cd desktop && MAITRE_BIN=$(CURDIR)/server/bin/maitre $(PNPM) tauri dev

info: build ## Call GetNodeInfo as the CLI device in $(STATE)/device
	MAITRE_STATE=$(STATE) ./server/bin/maitre info

tunnel-check: sidecar ## End-to-end: Console's Service tunnel (HTTP + WebSocket) against a dev Node with an echo Service
	./scripts/tunnel-check.sh

m0-check: sidecar ## End-to-end: Go dev Node <-> Rust client over mTLS, both directions of refusal
	./scripts/m0-check.sh ./server/bin/maitre

m1-check: build ## End-to-end: pair and provision a (fake-install) Installer, then start the agent from its config
	./scripts/m1-check.sh ./server/bin/maitre

image: build ## Build the Maître Server bootc image (needs podman)
	podman build --retry 6 --retry-delay 30s --build-arg VERSION=$(VERSION) -t localhost/maitre-server:$(VERSION) -f image/Containerfile .

publish: build ## Publish the local image build into the Depot as maitre-server:$(TAG) (TAG=dev; NETBOOT=1 also builds netboot files)
	./server/bin/maitre depot publish localhost/maitre-server:$(VERSION) --tag $(TAG) $(if $(NETBOOT),--netboot)

depot: build ## Serve the Depot (image registry + netboot files) to Nodes on the LAN (NETBOOT=1 answers PXE, asks for sudo; SERIAL=1 gives netbooted VMs a serial console)
	$(if $(NETBOOT),sudo ./server/bin/maitre depot serve --netboot --dir $(HOME)/.maitre/depot,./server/bin/maitre depot serve) $(if $(SERIAL),--serial)

image-boot: build ## Install from the image in QEMU and reboot into a Node (needs rootful podman, qemu, KVM)
	MAITRE=./server/bin/maitre ./image/boot-test.sh localhost/maitre-server:$(VERSION)

clean:
	rm -rf server/bin desktop/dist
