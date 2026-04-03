.PHONY: all build fmt vet lint test test-smoke clean install \
        install-claude-relay install-codex-relay install-gemini-relay \
        chatbots

BINS := bin/scuttlebot bin/scuttlectl bin/claude-relay bin/codex-relay \
        bin/gemini-relay bin/fleet-cmd

CHATBOT_BINS := bin/claude-agent bin/codex-agent bin/gemini-agent

all: $(BINS)

# chatbots builds the optional IRC chatbot agents (claude-agent, codex-agent,
# gemini-agent). These are not part of the default build — see docs/chatbot-agents.md.
chatbots: $(CHATBOT_BINS)

build:
	go build ./...

fmt:
	gofmt -w ./

vet:
	go vet ./...

lint:
	golangci-lint run

test:
	go test ./...

test-smoke:
	bash tests/smoke/test-installers.sh

# Install daemon + CLI to $(GOPATH)/bin (or ~/go/bin).
install:
	go install ./cmd/scuttlebot ./cmd/scuttlectl

clean:
	rm -f $(BINS) $(CHATBOT_BINS)

# --- relay install helpers ---

install-claude-relay:
	bash skills/scuttlebot-relay/scripts/install-claude-relay.sh

install-codex-relay:
	bash skills/openai-relay/scripts/install-codex-relay.sh

install-gemini-relay:
	bash skills/gemini-relay/scripts/install-gemini-relay.sh

# --- individual bin targets ---

bin/scuttlebot:
	go build -o $@ ./cmd/scuttlebot

bin/scuttlectl:
	go build -o $@ ./cmd/scuttlectl

bin/claude-relay:
	go build -o $@ ./cmd/claude-relay

bin/codex-relay:
	go build -o $@ ./cmd/codex-relay

bin/gemini-relay:
	go build -o $@ ./cmd/gemini-relay

bin/claude-agent:
	go build -o $@ ./cmd/claude-agent

bin/codex-agent:
	go build -o $@ ./cmd/codex-agent

bin/gemini-agent:
	go build -o $@ ./cmd/gemini-agent

bin/fleet-cmd:
	go build -o $@ ./cmd/fleet-cmd
