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

PKG_STAGE    := dist/pkg

.PHONY: all build build-local build-linux container install smf-install deploy pkg clean fmt vet test

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

## pkg: build pkgsrc binary package for pkg_add installation on SmartOS
pkg: build
	@echo "==> Staging pkgsrc package $(BINARY)-$(VERSION)"
	@rm -rf $(PKG_STAGE)
	@mkdir -p \
		$(PKG_STAGE)/opt/local/bin \
		$(PKG_STAGE)/opt/local/lib/svc/method \
		$(PKG_STAGE)/opt/local/lib/svc/manifest/network
	@install -m 0755 $(BINARY)   $(PKG_STAGE)/opt/local/bin/$(BINARY)
	@install -m 0755 smf/method  $(PKG_STAGE)/opt/local/lib/svc/method/$(BINARY)
	@install -m 0444 $(SMF_SRC)  $(PKG_STAGE)/opt/local/lib/svc/manifest/network/$(BINARY).xml
	@printf '@comment PKG_FORMAT_REVISION:1.0\n@name %s-%s\n' \
		$(BINARY) $(VERSION) > $(PKG_STAGE)/+CONTENTS
	@printf 'bin/%s\n'                          $(BINARY) >> $(PKG_STAGE)/+CONTENTS
	@printf 'lib/svc/method/%s\n'               $(BINARY) >> $(PKG_STAGE)/+CONTENTS
	@printf 'lib/svc/manifest/network/%s.xml\n' $(BINARY) >> $(PKG_STAGE)/+CONTENTS
	@printf '@exec svccfg import %%D/lib/svc/manifest/network/%s.xml 2>/dev/null || true\n' \
		$(BINARY) >> $(PKG_STAGE)/+CONTENTS
	@printf '@unexec svccfg delete svc:/network/%s:default 2>/dev/null || true\n' \
		$(BINARY) >> $(PKG_STAGE)/+CONTENTS
	@printf 'Pluto TV for Channels DVR\n' > $(PKG_STAGE)/+COMMENT
	@printf 'Bridges Pluto TV free streaming to Channels DVR via authenticated\n' \
		> $(PKG_STAGE)/+DESC
	@printf 'M3U playlists and XMLTV EPG served over HTTP.\n' >> $(PKG_STAGE)/+DESC
	@printf 'OPSYS=SunOS\nOS_VERSION=5.11\nMACHINE_ARCH=x86_64\n' > $(PKG_STAGE)/+BUILD_INFO
	@mkdir -p dist
	@cd $(PKG_STAGE) && tar czf ../$(BINARY)-$(VERSION).tgz \
		+CONTENTS +COMMENT +DESC +BUILD_INFO opt
	@echo "==> Package: dist/$(BINARY)-$(VERSION).tgz"

## clean: remove build artifacts
clean:
	rm -f $(BINARY)
	rm -rf dist/

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
