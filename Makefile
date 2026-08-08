BINDIR   := bin
VERSION  ?= v0.0.0-dev
COMMIT   ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
LDFLAGS  := -s -w \
  -X github.com/agentcell/agentcell/internal/version.Version=$(VERSION) \
  -X github.com/agentcell/agentcell/internal/version.Commit=$(COMMIT)

BINARIES := celld cell-runtime cellctl

.PHONY: all build $(BINARIES) test vet fmt-check lint clean

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

test:
	go test ./...

vet:
	go vet ./...

fmt-check:
	@out="$$(gofmt -l .)"; if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

lint: fmt-check vet

clean:
	rm -rf $(BINDIR)
