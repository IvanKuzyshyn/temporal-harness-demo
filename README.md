# Temporal Agent Harness

A local Go + Bubble Tea + Ollama teaching harness for Temporal's long-running activity, child workflow, parallel execution, and approval patterns.

## Stack status

This is the scaffold PR. Commands are discoverable with `--help` but deliberately return an error until implemented in later draft PRs. **The demo is not implemented at this stack position.** No draft in this stack should be merged independently without review.

## Development

Supported: macOS arm64 and Linux amd64. Install [mise](https://mise.jdx.dev), then:

```sh
mise trust
mise install
mise exec -- go build -o bin/harness ./cmd/harness
./bin/harness --help
mise run test
mise run lint
```

The completed stack will support `mise trust && mise run setup && ./bin/harness demo`. The setup task currently stops at the unimplemented doctor command. `mise setup` is also a supported task shorthand; documentation uses `mise run setup` explicitly.

Pins: Go 1.27.3, Temporal CLI 1.10.0, Ollama 0.40.15. Ollama's macOS arm64 mise installation was verified during scaffolding; CI validates Linux amd64. Model downloads require several GB. No cloud model API keys are needed. Runtime files will live in gitignored `.data/`.

If mise cannot install Ollama, the intended doctor fallback is `brew install ollama` on macOS or Ollama's official Linux installation instructions at https://ollama.com/download/linux.
