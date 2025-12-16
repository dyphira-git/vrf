MODULE := github.com/vexxvakan/vrf

BIN_DIR ?= $(CURDIR)/bin
PROTO_DIR ?= ./proto

GO ?= go
BUF ?= $(GO) tool buf

# Binaries / packages
CHAIN_NAME ?= vrf
CHAIN_BINARY ?= chaind
CHAIN_PKG ?= ./cmd/chaind
SIDECAR_BINARY ?= sidecar
SIDECAR_PKG ?= ./cmd/sidecar

# Inputs (overridable):
# - BUILD_TAGS: additional Go build tags (space-separated)
# - LDFLAGS: additional Go ldflags appended to defaults
# - LEDGER_ENABLED: include Cosmos ledger support (true/false)
# - LINK_STATICALLY: set CGO_ENABLED=0 (true/false)
# - SKIP_TIDY: skip go mod tidy before build/test/install (true/false)
BUILD_TAGS ?=
LDFLAGS ?=
LEDGER_ENABLED ?= true
LINK_STATICALLY ?= false
SKIP_TIDY ?= false

# Host/target platform
HOST_GOOS := $(shell $(GO) env GOOS 2>/dev/null)
HOST_GOARCH := $(shell $(GO) env GOARCH 2>/dev/null)
GOOS ?= $(HOST_GOOS)
GOARCH ?= $(HOST_GOARCH)

EXE_EXT :=
ifeq ($(GOOS),windows)
EXE_EXT := .exe
endif

CHAIN_OUT := $(BIN_DIR)/$(CHAIN_BINARY)$(EXE_EXT)
SIDECAR_OUT := $(BIN_DIR)/$(SIDECAR_BINARY)$(EXE_EXT)

# Git metadata (best-effort; ok to be empty outside git worktrees)
BRANCH := $(shell git rev-parse --abbrev-ref HEAD 2>/dev/null)
COMMIT := $(shell git rev-parse HEAD 2>/dev/null)
VERSION ?= $(shell git describe --tags --always 2>/dev/null)
ifeq ($(strip $(VERSION)),)
  ifneq ($(strip $(BRANCH)),)
    ifneq ($(strip $(COMMIT)),)
      VERSION := $(BRANCH)-$(COMMIT)
    endif
  endif
endif
ifeq ($(strip $(VERSION)),)
  VERSION := dev
endif

# Dependency versions (used in summaries/ldflags)
COSMOS_SDK_VERSION := $(shell $(GO) list -m -f '{{.Version}}' github.com/cosmos/cosmos-sdk 2>/dev/null)
CMT_VERSION := $(shell $(GO) list -m -f '{{.Version}}' github.com/cometbft/cometbft 2>/dev/null)
DRAND_GO_VERSION := $(shell $(GO) list -m -f '{{.Version}}' github.com/drand/drand/v2 2>/dev/null)
DRAND_SEMVER := $(patsubst v%,%,$(DRAND_GO_VERSION))

# Build tags
build_tags := netgo
ifneq ($(strip $(BUILD_TAGS)),)
  build_tags += $(BUILD_TAGS)
endif

ifeq ($(LEDGER_ENABLED),true)
  ifeq ($(LINK_STATICALLY),true)
    $(error LEDGER_ENABLED=true is incompatible with LINK_STATICALLY=true (ledger requires CGO))
  endif

  ifeq ($(HOST_GOOS),windows)
    GCCEXE := $(strip $(shell cmd /C "where gcc.exe 2> NUL"))
    ifeq ($(GCCEXE),)
      $(error gcc.exe not installed for ledger support; install it (e.g. mingw) or set LEDGER_ENABLED=false)
    endif
    build_tags += ledger
  else
    UNAME_S := $(shell uname -s 2>/dev/null)
    ifeq ($(UNAME_S),OpenBSD)
      $(warning OpenBSD detected, disabling ledger support (https://github.com/cosmos/cosmos-sdk/issues/1988))
    else
      GCC := $(shell command -v gcc 2>/dev/null)
      ifeq ($(strip $(GCC)),)
        $(error gcc not installed for ledger support; install it or set LEDGER_ENABLED=false)
      endif
      build_tags += ledger
    endif
  endif
endif

empty :=
space := $(empty) $(empty)
comma := ,
build_tags_comma_sep := $(subst $(space),$(comma),$(build_tags))

# Linker flags
ldflags := \
	-X github.com/cosmos/cosmos-sdk/version.Name=$(CHAIN_NAME) \
	-X github.com/cosmos/cosmos-sdk/version.AppName=$(CHAIN_BINARY) \
	-X github.com/cosmos/cosmos-sdk/version.Version=$(VERSION) \
	-X github.com/cosmos/cosmos-sdk/version.Commit=$(COMMIT) \
	-X "github.com/cosmos/cosmos-sdk/version.BuildTags=$(build_tags_comma_sep)" \
	-X github.com/cometbft/cometbft/version.TMCoreSemVer=$(CMT_VERSION)

ifeq ($(LINK_STATICALLY),true)
  GO_ENV_VARS := CGO_ENABLED=0
  ldflags += -s -w
else
  GO_ENV_VARS :=
endif

ldflags += $(LDFLAGS)
ldflags := $(strip $(ldflags))

BUILD_FLAGS := -trimpath -tags "$(build_tags)" -ldflags '$(ldflags)'

# Sidecar drand binary pinning (PRD §3.1).
sidecar_ldflags := $(ldflags)
ifneq ($(strip $(DRAND_SEMVER)),)
  sidecar_ldflags += -X $(MODULE)/sidecar.expectedDrandSemver=$(DRAND_SEMVER)
endif
sidecar_ldflags := $(strip $(sidecar_ldflags))

SIDECAR_BUILD_FLAGS := -trimpath -tags "$(build_tags)" -ldflags '$(sidecar_ldflags)'
TEST_FLAGS := -tags "$(build_tags)"
GO_ENV_BUILD := $(GO_ENV_VARS) GOOS=$(GOOS) GOARCH=$(GOARCH)
GO_ENV_TEST := $(GO_ENV_VARS) GOOS=$(HOST_GOOS) GOARCH=$(HOST_GOARCH)

ifeq ($(SKIP_TIDY),true)
TIDY_DEPS :=
else
TIDY_DEPS := tidy
endif

GOFUMPT := $(GO) tool gofumpt
GOIMPORTS := $(GO) tool goimports
GOLANGCI_LINT := $(GO) tool golangci-lint

GO_SOURCES_FIND := find . -name '*.go' -type f \
	-not -path './.git/*' \
	-not -name '*.pb.go' \
	-not -name '*.pulsar.go' \
	-not -name '*.gw.go' \
	-print0

.DEFAULT_GOAL := help

help: ## Show this help
	@awk 'BEGIN {FS=":.*##"; printf "Usage: make <target> [VAR=value]\n\nTargets:\n"} /^[a-zA-Z0-9_.-]+:.*##/ {printf "  %-22s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

###############################################################################
### Build
###############################################################################

verify:
	@echo "🔎 Verifying Dependencies ..."
	@$(GO) mod verify > /dev/null 2>&1
	@echo "✅ Verified dependencies successfully!"

go-cache: verify
	@echo "📥 Downloading and caching dependencies..."
	@$(GO) mod download
	@echo "✅ Downloaded and cached dependencies successfully!"

install: $(TIDY_DEPS) go-cache install-chain install-sidecar ## Install chaind + sidecar into GOBIN

install-chain:
	@echo "🔄 Installing $(CHAIN_BINARY)..."
	@$(GO_ENV_TEST) $(GO) install -mod=readonly $(BUILD_FLAGS) $(CHAIN_PKG)

install-sidecar:
	@echo "🔄 Installing $(SIDECAR_BINARY)..."
	@$(GO_ENV_TEST) $(GO) install -mod=readonly $(SIDECAR_BUILD_FLAGS) $(SIDECAR_PKG)

build: $(TIDY_DEPS) go-cache build-chain build-sidecar ## Build chaind + sidecar into ./bin

build-chain:
	@mkdir -p $(BIN_DIR)
	@echo "🔄 Building $(CHAIN_OUT)..."
	@$(GO_ENV_BUILD) $(GO) build -mod=readonly $(BUILD_FLAGS) -o $(CHAIN_OUT) $(CHAIN_PKG)

build-sidecar:
	@mkdir -p $(BIN_DIR)
	@echo "🔄 Building $(SIDECAR_OUT)..."
	@$(GO_ENV_BUILD) $(GO) build -mod=readonly $(SIDECAR_BUILD_FLAGS) -o $(SIDECAR_OUT) $(SIDECAR_PKG)

clean: ## Remove build artifacts
	@rm -rf $(BIN_DIR)

print-build-info: ## Print computed build metadata
	@echo "Binary: $(CHAIN_BINARY)$(EXE_EXT)"
	@echo "Version: $(VERSION)"
	@echo "Commit: $(COMMIT)"
	@echo "Build tags: $(build_tags)"
	@echo "Cosmos SDK: $(COSMOS_SDK_VERSION)"
	@echo "CometBFT: $(CMT_VERSION)"

build-summary:
	@echo ""
	@echo "======= Build Summary ======="
	@echo "$(CHAIN_BINARY): $(VERSION)"
	@echo "Cosmos SDK: $(COSMOS_SDK_VERSION)"
	@echo "Comet: $(CMT_VERSION)"
	@echo "============================="

install-summary:
	@echo ""
	@echo "====== Install Summary ======"
	@echo "$(CHAIN_BINARY): $(VERSION)"
	@echo "Cosmos SDK: $(COSMOS_SDK_VERSION)"
	@echo "Comet: $(CMT_VERSION)"
	@echo "============================="

init: build-chain ## Initialize a local single-node devnet (destructive)
	@BINARY=$(CHAIN_OUT) RESET_CHAIN=true sh scripts/init.sh chain

###############################################################################
### Docker
###############################################################################

DOCKER ?= docker
DOCKER_COMPOSE ?= $(DOCKER) compose
DOCKER_BUILDX ?= $(DOCKER) buildx

DOCKER_BUILDER ?=
ifeq ($(HOST_GOOS),darwin)
  DOCKER_BUILDER ?= desktop-linux
endif

DOCKER_BUILDER_FLAG :=
ifneq ($(strip $(DOCKER_BUILDER)),)
  DOCKER_BUILDER_FLAG := --builder $(DOCKER_BUILDER)
endif

DOCKER_GO_VERSION ?= 1.25.3
DOCKER_ALPINE_VERSION ?= 3.22

IMAGE_BASE ?= vrf
IMAGE_TAG ?= $(VERSION)

CHAIN_IMAGE_REPO ?= $(IMAGE_BASE)-chain
SIDECAR_IMAGE_REPO ?= $(IMAGE_BASE)-sidecar

CHAIN_IMAGE ?= $(CHAIN_IMAGE_REPO):$(IMAGE_TAG)
SIDECAR_IMAGE ?= $(SIDECAR_IMAGE_REPO):$(IMAGE_TAG)

DOCKER_BUILD_TAGS ?= muslc
DOCKER_LEDGER_ENABLED ?= false
DOCKER_LINK_STATICALLY ?= true
DOCKER_SKIP_TIDY ?= true

DOCKER_LOCAL_PLATFORM ?= linux/$(HOST_GOARCH)
DOCKER_PLATFORMS ?= linux/amd64,linux/arm64

DOCKER_BUILD_ARGS := \
	--build-arg GO_VERSION=$(DOCKER_GO_VERSION) \
	--build-arg ALPINE_VERSION=$(DOCKER_ALPINE_VERSION) \
	--build-arg VERSION=$(VERSION) \
	--build-arg COMMIT=$(COMMIT) \
	--build-arg BUILD_TAGS=$(DOCKER_BUILD_TAGS) \
	--build-arg LEDGER_ENABLED=$(DOCKER_LEDGER_ENABLED) \
	--build-arg LINK_STATICALLY=$(DOCKER_LINK_STATICALLY) \
	--build-arg SKIP_TIDY=$(DOCKER_SKIP_TIDY)

docker-build: docker-build-chain docker-build-sidecar ## Build Docker images (local platform)

docker-build-chain: ## Build chain Docker image (local platform)
	@$(DOCKER_BUILDX) build \
		$(DOCKER_BUILDER_FLAG) \
		--load \
		--platform $(DOCKER_LOCAL_PLATFORM) \
		$(DOCKER_BUILD_ARGS) \
		-f contrib/images/chain.Dockerfile \
		-t $(CHAIN_IMAGE) \
		.

docker-build-sidecar: ## Build sidecar Docker image (local platform)
	@$(DOCKER_BUILDX) build \
		$(DOCKER_BUILDER_FLAG) \
		--load \
		--platform $(DOCKER_LOCAL_PLATFORM) \
		$(DOCKER_BUILD_ARGS) \
		-f contrib/images/sidecar.Dockerfile \
		-t $(SIDECAR_IMAGE) \
		.

docker-push: docker-push-chain docker-push-sidecar ## Build+push Docker images (multi-platform)

docker-push-chain: ## Build+push chain Docker image (multi-platform)
	@$(DOCKER_BUILDX) build \
		$(DOCKER_BUILDER_FLAG) \
		--push \
		--platform $(DOCKER_PLATFORMS) \
		$(DOCKER_BUILD_ARGS) \
		-f contrib/images/chain.Dockerfile \
		-t $(CHAIN_IMAGE) \
		.

docker-push-sidecar: ## Build+push sidecar Docker image (multi-platform)
	@$(DOCKER_BUILDX) build \
		$(DOCKER_BUILDER_FLAG) \
		--push \
		--platform $(DOCKER_PLATFORMS) \
		$(DOCKER_BUILD_ARGS) \
		-f contrib/images/sidecar.Dockerfile \
		-t $(SIDECAR_IMAGE) \
		.

docker-up: ## Start Docker Compose stack (chain + sidecar)
	@CHAIN_IMAGE=$(CHAIN_IMAGE) \
	SIDECAR_IMAGE=$(SIDECAR_IMAGE) \
	VERSION=$(VERSION) \
	COMMIT=$(COMMIT) \
	DOCKER_GO_VERSION=$(DOCKER_GO_VERSION) \
	DOCKER_ALPINE_VERSION=$(DOCKER_ALPINE_VERSION) \
	DOCKER_BUILD_TAGS=$(DOCKER_BUILD_TAGS) \
	DOCKER_LEDGER_ENABLED=$(DOCKER_LEDGER_ENABLED) \
	DOCKER_LINK_STATICALLY=$(DOCKER_LINK_STATICALLY) \
	DOCKER_SKIP_TIDY=$(DOCKER_SKIP_TIDY) \
	$(DOCKER_COMPOSE) up -d --build

docker-up-vrf: docker-up ## Alias for docker-up (deprecated)

docker-stop: ## Stop Docker Compose stack (keeps containers + volumes)
	@$(DOCKER_COMPOSE) stop

docker-down: ## Remove Docker Compose stack (containers + network)
	@$(DOCKER_COMPOSE) down

docker-down-clean: ## Remove Docker Compose stack and volumes
	@$(DOCKER_COMPOSE) down -v

###############################################################################
### Protobuf
###############################################################################

proto-all: proto-format proto-lint proto-gen ## Format+lint+generate protobuf
proto-check: proto-format-check proto-lint ## Check protobuf formatting+lint
proto-gen: proto-gogo proto-pulsar proto-openapi ## Generate protobuf code

proto-gogo:
	@echo "==> generating protobuf (gogo)"
	@sh ./scripts/buf/buf-gogo.sh

proto-pulsar:
	@echo "==> generating protobuf (api)"
	@sh ./scripts/buf/buf-pulsar.sh

proto-openapi:
	@echo "==> generating protobuf (openapi)"
	@sh ./scripts/buf/buf-openapi.sh

proto-format:
	@echo "==> formatting protobuf"
	@$(BUF) format $(PROTO_DIR) -w --error-format=json

proto-format-check:
	@echo "==> checking protobuf formatting"
	@$(BUF) format $(PROTO_DIR) -d --exit-code --error-format=json

proto-lint:
	@echo "==> linting protobuf"
	@$(BUF) lint $(PROTO_DIR) --error-format=json

PROTO_BREAKING_AGAINST ?= .git#branch=main
proto-breaking: ## Check protobuf breaking changes against main
	@echo "==> checking protobuf breaking changes against $(PROTO_BREAKING_AGAINST)"
	@$(BUF) breaking $(PROTO_DIR) --against '$(PROTO_BREAKING_AGAINST)'

###############################################################################
### Tests
###############################################################################

test: $(TIDY_DEPS) ## Run unit tests
	@$(GO_ENV_TEST) $(GO) test $(TEST_FLAGS) ./...

###############################################################################
### Tooling
###############################################################################

tidy: ## Run go mod tidy
	@$(GO) mod tidy

lint: ## Run golangci-lint
	@echo "==> running golangci-lint"
	@$(GOLANGCI_LINT) run

lint-fix: ## Run golangci-lint with --fix
	@echo "==> running golangci-lint (fix)"
	@$(GOLANGCI_LINT) run --fix --issues-exit-code=0

format: ## Run gofumpt + goimports
	@$(GO_SOURCES_FIND) | xargs -0 $(GOFUMPT) -w
	@$(GO_SOURCES_FIND) | xargs -0 $(GOIMPORTS) -w -local $(MODULE)

.PHONY: \
	help \
	verify \
	go-cache \
	tidy \
	build \
	build-chain \
	build-sidecar \
	install \
	install-chain \
	install-sidecar \
	clean \
	print-build-info \
	build-summary \
	install-summary \
	init \
	docker-build \
	docker-build-chain \
	docker-build-sidecar \
	docker-push \
	docker-push-chain \
	docker-push-sidecar \
	docker-up \
	docker-up-vrf \
	docker-stop \
	docker-down \
	docker-down-clean \
	proto-all \
	proto-check \
	proto-gen \
	proto-gogo \
	proto-pulsar \
	proto-openapi \
	proto-format \
	proto-format-check \
	proto-lint \
	proto-breaking \
	test \
	lint \
	lint-fix \
	format
