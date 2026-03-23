GO      = /usr/local/go/bin/go
BINARY  = sacli
VERSION = $(shell grep 'const version' main.go | grep -oP '".*?"' | tr -d '"')
LDFLAGS = -ldflags="-s -w"

.PHONY: build build-linux-amd64 build-linux-386 build-linux-arm64 build-linux-arm build-windows-amd64 build-mac build-all clean

build:
	$(GO) build $(LDFLAGS) -o $(BINARY) .

build-linux-amd64:
	GOOS=linux GOARCH=amd64 $(GO) build $(LDFLAGS) -o $(BINARY)-linux-amd64 .

build-linux-386:
	GOOS=linux GOARCH=386 $(GO) build $(LDFLAGS) -o $(BINARY)-linux-386 .

build-linux-arm64:
	GOOS=linux GOARCH=arm64 $(GO) build $(LDFLAGS) -o $(BINARY)-linux-arm64 .

build-linux-arm:
	GOOS=linux GOARCH=arm $(GO) build $(LDFLAGS) -o $(BINARY)-linux-arm .

build-windows-amd64:
	GOOS=windows GOARCH=amd64 $(GO) build $(LDFLAGS) -o $(BINARY)-windows.exe .

build-mac:
	GOOS=darwin GOARCH=arm64 $(GO) build $(LDFLAGS) -o $(BINARY)-mac .

build-all: build-linux-amd64 build-linux-386 build-linux-arm64 build-linux-arm build-windows-amd64 build-mac

clean:
	rm -f $(BINARY) $(BINARY)-* $(BINARY)-windows.exe
