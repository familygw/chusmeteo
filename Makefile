BIN  := chusmeteo
PKG  := ./cmd/chusmeteo
V    ?= mapa
SIZE ?= 80x24

.PHONY: run test race vet build build-all dump clean

run:
	go run $(PKG)

test:
	go test ./...

# Detector de carreras: importante con goroutines (descargas del radar).
race:
	go test -race ./...

vet:
	go vet ./...

build:
	go build -o bin/$(BIN) $(PKG)

# Cross-compilación: Go compila para otro SO/arquitectura con sólo dos variables.
build-all:
	GOOS=darwin  GOARCH=arm64 go build -o bin/$(BIN)-darwin-arm64  $(PKG)
	GOOS=linux   GOARCH=amd64 go build -o bin/$(BIN)-linux-amd64   $(PKG)
	GOOS=windows GOARCH=amd64 go build -o bin/$(BIN)-windows-amd64.exe $(PKG)

# make dump V=clima|mapa|radar|rx SIZE=120x40
dump:
	CHUSMETEO_DUMP=$(V) CHUSMETEO_SIZE=$(SIZE) go test ./internal/ui -run Dump -v -count=1

clean:
	rm -rf bin
