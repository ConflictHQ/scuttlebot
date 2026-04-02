# Contributing to scuttlebot

Thanks for your interest in contributing!

## Development setup

```bash
git clone https://github.com/ConflictHQ/scuttlebot
cd scuttlebot
go mod download
```

See [`bootstrap.md`](../bootstrap.md) for full setup including the Ergo IRC server.

## Running tests

```bash
go test ./...
```

## Code style

We use `gofmt` (enforced) and `golangci-lint`.

```bash
gofmt -w .
golangci-lint run
```

## Pull requests

1. Fork the repo and create a branch from `main`
2. Add tests for new behaviour
3. Ensure CI passes
4. Open a PR with a clear description of the change

## Commit messages

Use the imperative mood: `add X`, `fix Y`, `update Z`. Keep the first line under 72 characters.
