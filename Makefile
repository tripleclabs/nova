BINARY  := nova
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

# Suppress duplicate -lobjc warnings from Code-Hex/vz cgo directives (macOS only).
ifeq ($(shell uname),Darwin)
export CGO_LDFLAGS += -Wl,-no_warn_duplicate_libraries
endif

.PHONY: build test integration coverage proto clean

build:
	go build -ldflags "-X main.version=$(VERSION)" -o $(BINARY) ./cmd/nova/
ifeq ($(shell uname),Darwin)
	codesign --entitlements entitlements.plist --force -s - $(BINARY)
endif

test:
	go test ./...

integration: build
	go test -tags integration -timeout 300s -v -count=1 ./...

coverage:
	go test ./... -coverprofile=coverage.out
	go tool cover -func=coverage.out | tail -1
	@echo "---"
	@go tool cover -func=coverage.out | grep -v "100.0%" | grep -v "0.0%" | sort -t'%' -k2 -n | head -20

proto:
	protoc \
		--proto_path=proto \
		--proto_path=$$(brew --prefix protobuf)/include \
		--go_out=pkg/novapb --go_opt=paths=source_relative \
		--go-grpc_out=pkg/novapb --go-grpc_opt=paths=source_relative \
		nova/v1/nova.proto

clean:
	rm -f $(BINARY) coverage.out
