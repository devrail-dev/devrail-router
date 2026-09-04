# DevRail Router

> Local-first LLM routing and control plane for private AI infrastructure.

[![DevRail compliant](https://devrail.dev/images/badge.svg)](https://devrail.dev)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

DevRail Router presents one OpenAI-compatible endpoint to local agents and
developer tools, then routes requests to private inference backends such as LM
Studio, Ollama, vLLM, SGLang, or approved cloud fallbacks.

The initial target user is an operator running mixed self-hosted inference
hardware who wants a private subscription-style backend for tools such as
Hermes, OpenClaw, opencode, and other local agents.

## Status

This repository is in early foundation work. The current service supports:

- a small Go HTTP service
- `/healthz`
- `/v1/models`
- OpenAI-compatible `/v1/*` request proxying
- model alias rewriting
- YAML configuration
- Linux/systemd packaging notes

Routing policy, auth, telemetry, LM Studio lifecycle integration, and Omarchy
integration are planned next.

## Quick Start

Build and test locally:

```sh
go test ./...
go build ./cmd/devrail-router
```

Run against the example config:

```sh
go run ./cmd/devrail-router serve -config configs/router.example.yaml
```

List exposed model aliases:

```sh
curl http://127.0.0.1:8080/v1/models
```

Send a chat completion through the router:

```sh
curl http://127.0.0.1:8080/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "local-coder",
    "messages": [{"role": "user", "content": "Reply with ok."}],
    "max_tokens": 32
  }'
```

## Configuration

See `configs/router.example.yaml`.

```yaml
server:
  address: 127.0.0.1:8080

models:
  - id: local-coder
    name: Local Coder
    backend: lmstudio
    target_model: qwen3-coder-30b-a3b-instruct
    context_window: 65536
    max_output_tokens: 4096
    tool_calls: true

backends:
  - id: lmstudio
    type: openai-compatible
    base_url: http://127.0.0.1:1234/v1
```

## Packaging Direction

Linux is the first-class target:

- Binary: `/usr/local/bin/devrail-router`
- Config: `/etc/devrail/router.yaml`
- State: `/var/lib/devrail-router`
- Service user: `devrail-router`
- Service manager: systemd

See `docs/packaging.md` and `packaging/systemd/devrail-router.service`.

Omarchy support is planned as a separate integration profile. See
`integrations/omarchy/README.md`.

## Development

This project follows [DevRail](https://devrail.dev) development standards.

```sh
make check
```

All DevRail checks run through `ghcr.io/devrail-dev/dev-toolchain:v1`.

## License

MIT. See [LICENSE](LICENSE).
