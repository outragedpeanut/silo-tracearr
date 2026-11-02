VERSION ?= 0.2.0
BIN     := tracearr-connector_$(VERSION)

.PHONY: test build clean
test:
	go test ./...

# CGO must be off so the binary runs in Silo's container image.
build:
	mkdir -p dist
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-s -w" -o dist/$(BIN)_linux_amd64 .
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags "-s -w" -o dist/$(BIN)_linux_arm64 .

clean:
	rm -rf dist
