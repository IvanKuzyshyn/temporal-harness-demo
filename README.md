# Temporal Agent Harness

A local Go + Bubble Tea + Ollama teaching harness for Temporal's long-running activity, child workflow, parallel execution, and approval patterns.

## Stack status

This stack position includes the scaffold, contracts and environment doctor. Commands are discoverable with `--help`; `doctor [--fix]` checks or starts dependencies, while other runtime commands deliberately return errors until their later PRs. **The demo is not implemented at this stack position.** No draft in this stack should be merged independently without review.

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

The completed stack will support `mise trust && mise run setup && ./bin/harness demo`. Setup builds the binary, starts dependencies and pulls the model. Standalone `doctor --fix` deliberately leaves services it starts running; later demo invocations reuse them without taking ownership. Demo-owned services will be stopped on exit. `mise setup` is also a supported task shorthand; documentation uses `mise run setup` explicitly.

Pins: Go 1.27.3, Temporal CLI 1.10.0, Ollama 0.40.15. Ollama's macOS arm64 mise installation was verified during scaffolding; CI validates Linux amd64. Model downloads require several GB. No cloud model API keys are needed. Runtime files will live in gitignored `.data/`.

If mise cannot install Ollama, the intended doctor fallback is `brew install ollama` on macOS or Ollama's official Linux installation instructions at https://ollama.com/download/linux.
