# djgyrofix — dependency-free Go build.
#
# There are no module requirements, so every target works offline.

GO      ?= go
VERSION ?= $(shell sed -n 's/^const Version = "\(.*\)"$$/\1/p' cmd/djgyrofix/main.go)
DIST    ?= dist

# Platforms from the plan's CI matrix.
PLATFORMS = linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64

.PHONY: all build test race cover vet fmt lint fuzz parity fixtures dist clean

all: fmt vet test build

build:
	$(GO) build -o djgyrofix ./cmd/djgyrofix

test:
	$(GO) test ./...

race:
	$(GO) test -race ./...

cover:
	$(GO) test -coverprofile=coverage.out ./...
	$(GO) tool cover -func=coverage.out | tail -1

vet:
	$(GO) vet ./...

fmt:
	@unformatted=$$(gofmt -l cmd internal tools); \
	if [ -n "$$unformatted" ]; then echo "not gofmt'd:"; echo "$$unformatted"; exit 1; fi

lint: fmt vet

# The parsers walk attacker-controllable length fields, so both get fuzzed.
FUZZTIME ?= 60s
fuzz:
	$(GO) test -run xxx -fuzz FuzzParseTracks -fuzztime $(FUZZTIME) ./internal/mp4/
	$(GO) test -run xxx -fuzz FuzzQuaternions -fuzztime $(FUZZTIME) ./internal/djiproto/

# Byte-for-byte comparison against the Python reference (plan §9.1).
# Clone it first: git clone --depth 1 https://github.com/kim2160/DJIGyroFix.git
REFERENCE ?= ../DJIGyroFix
parity:
	./testdata/golden/parity.sh $(REFERENCE)

# Regenerate the smoothing fixtures the Go parity test reads. Only needed if
# the reference implementation changes.
fixtures:
	cd $(REFERENCE) && PYTHONPATH=. python3 $(CURDIR)/testdata/golden/gen_smoothing.py \
		$(CURDIR)/testdata/golden/smoothing.json

dist:
	@rm -rf $(DIST) && mkdir -p $(DIST)
	@for platform in $(PLATFORMS); do \
		os=$${platform%/*}; arch=$${platform#*/}; \
		out=$(DIST)/djgyrofix-$(VERSION)-$$os-$$arch; \
		if [ "$$os" = "windows" ]; then out=$$out.exe; fi; \
		echo "building $$out"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch $(GO) build -trimpath \
			-ldflags="-s -w" -o $$out ./cmd/djgyrofix || exit 1; \
	done
	@ls -l $(DIST)

clean:
	rm -rf djgyrofix $(DIST) coverage.out
