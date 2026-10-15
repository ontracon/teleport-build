.DEFAULT_GOAL := help

DOCKER ?= docker
GO ?= go
NPM ?= npm
BUILD_PLATFORM ?= linux/amd64
BUILD_JOBS ?= 2
TELEPORT_VERSION ?= 18.10.0
export BUILD_PLATFORM BUILD_JOBS TELEPORT_VERSION
CONTAINER_BUILD = $(DOCKER) buildx bake --file build/docker-bake.hcl
MOCK_COMPOSE = $(DOCKER) compose -f dev/compose.yaml
REAL_COMPOSE = $(DOCKER) compose -f dev/compose.real.yaml

.PHONY: help test test-patches prepare-source build build-test artifacts lab-build lab-image lab lab-down real-lab real-lab-down test-deps test-browser test-connectors test-cli test-real

help:
	@printf '%s\n' \
	  'make test             Test and vet the independent OIDC adapter' \
	  'make test-patches SOURCE_DIR=...  Check source preparation in a disposable fixture' \
	  'make prepare-source SOURCE_DIR=...  Apply integration to a fresh checkout' \
	  'make build            Build the tested Teleport + Zitadel runtime image' \
	  'make build-test       Build the verified compiler/test stage' \
	  'make artifacts        Export all six binaries, manifest, and reports' \
	  'make lab-build        Build runtime and lab images together' \
	  'make lab              Start the mock lab; build its image if missing' \
	  'make lab-down         Stop the mock lab and retain its data' \
	  'make real-lab         Start the real lab; build its image if missing' \
	  'make real-lab-down    Stop the real lab and retain its data' \
	  'make test-deps        Install browser-test dependencies' \
	  'make test-browser     Test browser login against the running mock lab' \
	  'make test-connectors  Test connector management against the mock lab' \
	  'make test-cli         Test tsh login and SSH against the mock lab' \
	  'make test-real        Check the real Zitadel login handoff'

test:
	cd oidc-adapter && $(GO) test -race -count=1 ./... && $(GO) vet ./...

test-patches:
	bash tests/build/test-prepare-source.sh "$(SOURCE_DIR)"

prepare-source:
	./build/prepare-teleport-source.sh --source "$(SOURCE_DIR)"

build:
	$(CONTAINER_BUILD) --load runtime

build-test:
	$(CONTAINER_BUILD) --load verified

artifacts:
	$(CONTAINER_BUILD) artifacts

lab-build:
	$(CONTAINER_BUILD) --load runtime lab

lab-image:
	@if [ "$$($(DOCKER) image inspect --format '{{.Os}}/{{.Architecture}}' teleport-zitadel-lab:$(TELEPORT_VERSION) 2>/dev/null)" != "$(BUILD_PLATFORM)" ]; then $(MAKE) lab-build; fi

lab: lab-image
	$(MOCK_COMPOSE) up -d --no-build --wait

lab-down:
	$(MOCK_COMPOSE) stop teleport mock-idp

real-lab: lab-image
	$(REAL_COMPOSE) up -d --wait

real-lab-down:
	$(REAL_COMPOSE) stop teleport

test-deps:
	$(NPM) ci --prefix tests/integration

test-browser:
	$(NPM) test --prefix tests/integration

test-connectors:
	$(NPM) run test:connectors --prefix tests/integration

test-cli:
	$(MOCK_COMPOSE) exec -T teleport python3 /lab/cli-smoke.py

test-real:
	$(NPM) run test:real --prefix tests/integration
