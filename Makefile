.PHONY: build test lint clean install-codex-relay install-gemini-relay install-claude-relay test-smoke

build:
	go build ./...

test:
	go test ./...

test-smoke:
	bash tests/smoke/test-installers.sh

lint:
	golangci-lint run

clean:
	rm -f bin/scuttlebot bin/scuttlectl bin/claude-agent bin/codex-agent bin/gemini-agent bin/codex-relay bin/gemini-relay bin/claude-relay bin/fleet-cmd

install-codex-relay:
	bash skills/openai-relay/scripts/install-codex-relay.sh

install-gemini-relay:
	bash skills/gemini-relay/scripts/install-gemini-relay.sh

install-claude-relay:
	bash skills/scuttlebot-relay/scripts/install-claude-relay.sh

bin/scuttlebot:
	go build -o bin/scuttlebot ./cmd/scuttlebot

bin/scuttlectl:
	go build -o bin/scuttlectl ./cmd/scuttlectl

bin/claude-agent:
	go build -o bin/claude-agent ./cmd/claude-agent

bin/codex-agent:
	go build -o bin/codex-agent ./cmd/codex-agent

bin/gemini-agent:
	go build -o bin/gemini-agent ./cmd/gemini-agent

bin/codex-relay:
	go build -o bin/codex-relay ./cmd/codex-relay

bin/gemini-relay:
	go build -o bin/gemini-relay ./cmd/gemini-relay

bin/claude-relay:
	go build -o bin/claude-relay ./cmd/claude-relay

bin/fleet-cmd:
	go build -o bin/fleet-cmd ./cmd/fleet-cmd
