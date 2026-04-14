.PHONY: build test lint clean docker run

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

build:
	go build -ldflags="$(LDFLAGS)" -o goclaw ./cmd/

test:
	go test -race ./...

lint:
	golangci-lint run

clean:
	rm -f goclaw goclaw.exe

docker:
	docker build --build-arg VERSION=$(VERSION) -t goclaw:$(VERSION) .

run:
	go run ./cmd/ cli
