.PHONY: build test lint clean

build:
	go build ./...

test:
	go test ./...

lint:
	golangci-lint run

clean:
	rm -f bin/scuttlebot bin/scuttlectl

bin/scuttlebot:
	go build -o bin/scuttlebot ./cmd/scuttlebot

bin/scuttlectl:
	go build -o bin/scuttlectl ./cmd/scuttlectl
