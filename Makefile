BINARY  := nova
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

# Suppress duplicate -lobjc warnings from Code-Hex/vz cgo directives (macOS only).
ifeq ($(shell uname),Darwin)
export CGO_LDFLAGS += -Wl,-no_warn_duplicate_libraries
endif

.PHONY: build test clean

build:
	go build -ldflags "-X main.version=$(VERSION)" -o $(BINARY) ./cmd/nova/

test:
	go test ./...

clean:
	rm -f $(BINARY)
