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
	$(GOINSTALL) $(PKG)/cmd/aperture

docker-tools:
	@$(call print, "Building tools docker image.")
	docker build -q -t aperture-tools $(TOOLS_DIR)

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

clean:
	@$(call print, "Cleaning source.$(NC)")
	$(RM) ./aperture
	$(RM) coverage.txt
