BINARY  ?= ghpm
VERSION ?= 0.4.0-dev
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null)
LDFLAGS  = -s -w -X ghpm/internal/version.Version=$(VERSION)
ifneq ($(COMMIT),)
LDFLAGS += -X ghpm/internal/version.Commit=$(COMMIT)
endif
PREFIX  ?= /usr/local
GO      ?= $(shell command -v go 2>/dev/null || echo /usr/local/go/bin/go)

.PHONY: build test race vet fmt install clean docs

build:
	$(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/ghpm

docs: build
	./$(BINARY) doc --dir docs

test:
	$(GO) test ./...

race:
	$(GO) test -race ./...

vet:
	$(GO) vet ./...

fmt:
	$(GO) fmt ./...

install: build
	install -Dm755 $(BINARY) $(DESTDIR)$(PREFIX)/bin/$(BINARY)

clean:
	$(GO) clean
