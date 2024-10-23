PKG := github.com/lightninglabs/aperture
ESCPKG := github.com\/lightninglabs\/aperture
TOOLS_DIR := tools

GOVERALLS_PKG := github.com/mattn/goveralls
GOACC_PKG := github.com/ory/go-acc

GO_BIN := ${GOPATH}/bin
GOVERALLS_BIN := $(GO_BIN)/goveralls
GOACC_BIN := $(GO_BIN)/go-acc
GOACC_COMMIT := ddc355013f90fea78d83d3a6c71f1d37ac07ecd5

DEPGET := cd /tmp && GO111MODULE=on go get -v
GOBUILD := go build -v
GOINSTALL := go install -v
GOTEST := go test -v

GOFILES_NOVENDOR = $(shell find . -type f -name '*.go' -not -path "./vendor/*")
GOLIST := go list -deps $(PKG)/... | grep '$(PKG)'| grep -v '/vendor/'
GOLISTCOVER := $(shell go list -deps -f '{{.ImportPath}}' ./... | grep '$(PKG)' | sed -e 's/^$(ESCPKG)/./')

MIGRATE_BIN := $(GO_BIN)/migrate

RM := rm -f
CP := cp
MAKE := make
XARGS := xargs -L 1

include make/testing_flags.mk

# DOCKER_TOOLS is a docker run command which executes the
# aperture tools (e.g. linting) docker image.
DOCKER_TOOLS = docker run -v $$(pwd):/build aperture-tools

default: build

all: build check install

# ============
# DEPENDENCIES
# ============

$(GOVERALLS_BIN):
	@$(call print, "Fetching goveralls.")
	go get -u $(GOVERALLS_PKG)

$(GOACC_BIN):
	@$(call print, "Fetching go-acc")
	$(DEPGET) $(GOACC_PKG)@$(GOACC_COMMIT)

# ============
# INSTALLATION
# ============

build:
	@$(call print, "Building aperture.")
	$(GOBUILD) $(PKG)/cmd/aperture

install:
	@$(call print, "Installing aperture.")
	$(GOINSTALL) -tags="${tags}" $(PKG)/cmd/aperture

docker-tools:
	@$(call print, "Building tools docker image.")
	docker build -q -t aperture-tools $(TOOLS_DIR)

# ===================
# DATABASE MIGRATIONS
# ===================

migrate-up: $(MIGRATE_BIN)
	migrate -path aperturedb/sqlc/migrations -database $(APERTURE_DB_CONNECTIONSTRING) -verbose up

migrate-down: $(MIGRATE_BIN)
	migrate -path aperturedb/sqlc/migrations -database $(APERTURE_DB_CONNECTIONSTRING) -verbose down 1

migrate-create: $(MIGRATE_BIN)
	migrate create -dir aperturedb/sqlc/migrations -seq -ext sql $(patchname)

# =======
# CODEGEN
# =======
sqlc:
	@$(call print, "Generating sql models and queries in Go")
	./scripts/gen_sqlc_docker.sh

sqlc-check: sqlc
	@$(call print, "Verifying sql code generation.")
	if test -n "$$(git status --porcelain '*.go')"; then echo "SQL models not properly generated!"; git status --porcelain '*.go'; exit 1; fi

# =======
# TESTING
# =======

check: unit

unit:
	@$(call print, "Running unit tests.")
	$(UNIT)

unit-cover: $(GOACC_BIN)
	@$(call print, "Running unit coverage tests.")
	$(GOACC_BIN) $(COVER_PKG)

unit-race:
	@$(call print, "Running unit race tests.")
	env CGO_ENABLED=1 GORACE="history_size=7 halt_on_errors=1" $(UNIT_RACE)

goveralls: $(GOVERALLS_BIN)
	@$(call print, "Sending coverage report.")
	$(GOVERALLS_BIN) -coverprofile=coverage.txt -service=travis-ci

travis-race: lint unit-race

# =============
# FLAKE HUNTING
# =============
flake-unit:
	@$(call print, "Flake hunting unit tests.")
	while [ $$? -eq 0 ]; do GOTRACEBACK=all $(UNIT) -count=1; done

# =========
# UTILITIES
# =========
fmt:
	@$(call print, "Formatting source.")
	gofmt -l -w -s $(GOFILES_NOVENDOR)

lint: docker-tools
	@$(call print, "Linting source.")
	$(DOCKER_TOOLS) golangci-lint run -v

list:
	@$(call print, "Listing commands.")
	@$(MAKE) -qp | \
		awk -F':' '/^[a-zA-Z0-9][^$$#\/\t=]*:([^=]|$$)/ {split($$1,A,/ /);for(i in A)print A[i]}' | \
		grep -v Makefile | \
		sort

rpc:
	@$(call print, "Compiling protos.")
	cd ./pricesrpc; ./gen_protos_docker.sh

rpc-format:
	@$(call print, "Formatting protos.")
	cd ./pricesrpc; find . -name "*.proto" | xargs clang-format --style=file -i

rpc-check: rpc
	@$(call print, "Verifying protos.")
	cd ./pricesrpc; ../pricesrpc/check-rest-annotations.sh
	if test -n "$$(git status --porcelain)"; then echo "Protos not properly formatted or not compiled with correct version"; git status; git diff; exit 1; fi

clean:
	@$(call print, "Cleaning source.$(NC)")
	$(RM) ./aperture
	$(RM) coverage.txt
