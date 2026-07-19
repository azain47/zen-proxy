BINARY=zen-proxy
CMD=./cmd/zen-proxy
PREFIX ?= /usr/local
BINDIR ?= $(PREFIX)/bin
VERSION ?= dev
LDFLAGS ?= -s -w -X main.version=$(VERSION)
DIST_DIR ?= dist
PLATFORMS ?= darwin/amd64 darwin/arm64 linux/amd64 linux/arm64 windows/amd64 windows/arm64

build:
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) $(CMD)

run: build
	./$(BINARY)

test:
	go test ./...

vet:
	go vet ./...

clean:
	rm -f $(BINARY)
	rm -rf $(DIST_DIR)

dist: clean
	mkdir -p "$(DIST_DIR)"
	@set -eu; \
	for platform in $(PLATFORMS); do \
		os="$${platform%/*}"; \
		arch="$${platform#*/}"; \
		name="$(BINARY)_$${os}_$${arch}"; \
		out="$(DIST_DIR)/$${name}/$(BINARY)"; \
		if [ "$$os" = "windows" ]; then out="$${out}.exe"; fi; \
		printf 'Building %s/%s\n' "$$os" "$$arch"; \
		mkdir -p "$(DIST_DIR)/$${name}"; \
		GOOS="$$os" GOARCH="$$arch" CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o "$$out" $(CMD); \
		cp LICENSE THIRD_PARTY_NOTICES.md "$(DIST_DIR)/$${name}/"; \
		mkdir -p "$(DIST_DIR)/$${name}/LICENSES"; \
		cp LICENSES/Apache-2.0.txt "$(DIST_DIR)/$${name}/LICENSES/"; \
		if [ "$$os" = "windows" ]; then \
			(cd "$(DIST_DIR)/$${name}" && zip -qr "../$${name}.zip" .); \
		else \
			tar -C "$(DIST_DIR)/$${name}" -czf "$(DIST_DIR)/$${name}.tar.gz" .; \
		fi; \
		rm -rf "$(DIST_DIR)/$${name}"; \
	done
	@if command -v sha256sum >/dev/null 2>&1; then \
		(cd "$(DIST_DIR)" && sha256sum * > checksums.txt); \
	else \
		(cd "$(DIST_DIR)" && shasum -a 256 * > checksums.txt); \
	fi

release: test vet dist

install: build
	install -d "$(DESTDIR)$(BINDIR)"
	install -m 0755 "$(BINARY)" "$(DESTDIR)$(BINDIR)/$(BINARY)"

uninstall:
	rm -f "$(DESTDIR)$(BINDIR)/$(BINARY)"

.PHONY: build run test vet clean dist release install uninstall
