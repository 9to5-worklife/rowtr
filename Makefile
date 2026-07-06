# Rowtr build & packaging.
#
#   make build      → both binaries into ./bin (host OS)
#   make dist       → macOS zip (rowtr + Rowtr.app) in ./dist
#   make dist-cli   → cgo-free CLI zips for windows/linux amd64+arm64
#   make release    → dist + dist-cli + SHA-256 checksums
#
# The tray (rowtr-tray) uses cgo, so it's built for the HOST platform only.
# The `rowtr` CLI is cgo-free and cross-compiles freely.

VERSION ?= 0.1.0
# -s -w strips symbol tables; -trimpath removes local filesystem paths — smaller
# binaries that don't leak source-tree structure.
LDFLAGS := -s -w -X main.version=$(VERSION)
GOFLAGS := -trimpath
OS      := $(shell go env GOOS)
ARCH    := $(shell go env GOARCH)
NAME    := rowtr-$(VERSION)-$(OS)-$(ARCH)
DIST    := dist/$(NAME)
APP     := $(DIST)/Rowtr.app

CLI_TARGETS := windows/amd64 linux/amd64 linux/arm64

.PHONY: build app dist dist-cli release clean

build:
	go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o bin/rowtr ./cmd/rowtr
	go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o bin/rowtr-tray ./cmd/rowtr-tray

# Wrap the tray binary in a minimal macOS .app so it's double-clickable.
app: build
	rm -rf "$(APP)"
	mkdir -p "$(APP)/Contents/MacOS"
	cp bin/rowtr-tray "$(APP)/Contents/MacOS/rowtr-tray"
	cp packaging/Info.plist "$(APP)/Contents/Info.plist"

dist: app
	cp bin/rowtr "$(DIST)/rowtr"
	cp packaging/QUICKSTART.md "$(DIST)/QUICKSTART.md"
	cp packaging/LICENSE.txt "$(DIST)/LICENSE.txt"
	cd dist && zip -r -q "$(NAME).zip" "$(NAME)"
	@echo "→ dist/$(NAME).zip"

# Cross-compiled, cgo-free CLI packages (no tray) for non-mac testers.
dist-cli:
	@for t in $(CLI_TARGETS); do \
		os=$${t%/*}; arch=$${t#*/}; ext=""; \
		[ "$$os" = "windows" ] && ext=".exe"; \
		out="dist/rowtr-$(VERSION)-$$os-$$arch"; \
		mkdir -p "$$out"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch \
			go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o "$$out/rowtr$$ext" ./cmd/rowtr || exit 1; \
		cp packaging/QUICKSTART-cli.md "$$out/QUICKSTART.md"; \
		cp packaging/LICENSE.txt "$$out/LICENSE.txt"; \
		(cd dist && zip -r -q "rowtr-$(VERSION)-$$os-$$arch.zip" "rowtr-$(VERSION)-$$os-$$arch"); \
		echo "→ dist/rowtr-$(VERSION)-$$os-$$arch.zip"; \
	done

release: dist dist-cli
	cd dist && shasum -a 256 *.zip > SHA256SUMS.txt
	@echo "→ dist/SHA256SUMS.txt"

clean:
	rm -rf bin dist
