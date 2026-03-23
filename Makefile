BINARY     := pluto
CMD        := ./cmd/pluto
VERSION    := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS    := -s -w -X main.version=$(VERSION)

# SmartOS/illumos cross-compilation target.
GOOS_TARGET   := illumos
GOARCH_TARGET := amd64

INSTALL_BIN  := /opt/local/bin/$(BINARY)
SMF_SRC      := smf/pluto.xml
SMF_DEST     := /opt/local/lib/svc/manifest/network/pluto.xml

.PHONY: all build build-local build-linux container install smf-install deploy clean fmt vet test

all: build

## build: cross-compile for SmartOS (illumos/amd64)
build:
	CGO_ENABLED=0 GOOS=$(GOOS_TARGET) GOARCH=$(GOARCH_TARGET) go build \
		-ldflags "$(LDFLAGS)" \
		-o $(BINARY) $(CMD)

## build-local: compile for the current OS (for local testing)
build-local:
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) $(CMD)

## build-linux: cross-compile for Linux/amd64 (e.g. for a Linux VM)
build-linux:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
		-ldflags "$(LDFLAGS)" \
		-o $(BINARY)-linux-amd64 $(CMD)

## container: build OCI container image using the Containerfile
container:
	container build --build-arg VERSION=$(VERSION) -t pluto .

## install: cross-compile and install binary on target host (run as root)
install: build
	install -m 0755 $(BINARY) $(INSTALL_BIN)

## smf-install: install and import the SMF manifest (run as root on SmartOS)
smf-install: $(SMF_SRC)
	install -m 0444 $(SMF_SRC) $(SMF_DEST)
	svccfg import $(SMF_DEST)

## deploy: install binary + SMF manifest (run as root on SmartOS)
deploy: install smf-install

## clean: remove build artifacts
clean:
	rm -f $(BINARY)

## fmt: format all Go source files
fmt:
	gofmt -w .

## vet: run go vet
vet:
	go vet ./...

## test: run all tests (of which there are none currently)
test:
	go test ./...

# Print help
help:
	@grep -E '^## ' Makefile | sed 's/^## /  /'
