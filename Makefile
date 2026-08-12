BINDIR   := bin
VERSION  ?= v0.0.0-dev
COMMIT   ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
LDFLAGS  := -s -w \
  -X github.com/zippo1908/agentcell/internal/version.Version=$(VERSION) \
  -X github.com/zippo1908/agentcell/internal/version.Commit=$(COMMIT)

BINARIES := celld git-broker cell-runtime cellctl

.PHONY: all build $(BINARIES) test vet fmt-check lint clean web web-install

all: build

build: $(BINARIES)

$(BINARIES):
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o $(BINDIR)/$@ ./cmd/$@

# cell-runtime is bind-mounted read-only into arbitrary container images,
# so it must always be a static linux/amd64 build regardless of the host.
.PHONY: build-runtime-static
build-runtime-static:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags '$(LDFLAGS)' \
	  -o $(BINDIR)/cell-runtime-linux-amd64 ./cmd/cell-runtime

# The UI is embedded into celld; build it before the Go binaries when it
# changed. dist/ is committed so `go build` works without Node.
web-install:
	cd web && pnpm install --frozen-lockfile

web:
	cd web && pnpm build

test:
	go test ./...

vet:
	go vet ./...

fmt-check:
	@out="$$(gofmt -l .)"; if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

lint: fmt-check vet

clean:
	rm -rf $(BINDIR)
