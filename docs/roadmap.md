# Roadmap

DevRail Router is the local-first control plane between agent clients and
private inference backends. The near-term goal is not to become a full model
server. It is to make local inference predictable enough that coding agents and
ops assistants can use it without every client learning backend-specific
lifecycle, queueing, telemetry, and safety behavior.

## Principles

- Keep the public client surface OpenAI-compatible.
- Prefer explicit aliases over hidden routing behavior.
- Make backend state visible before adding smarter routing.
- Fail with useful OpenAI-shaped errors instead of hanging clients.
- Keep host-specific lifecycle code replaceable by native adapters later.
- Treat local GPUs as shared infrastructure, not disposable accelerators.

## Phase 1: Reliable Single-Backend Gateway

Status: in progress.

This phase makes one local backend dependable for day-to-day tools such as
opencode, OpenClaw, and Hermes.

- Stable `/healthz` and `/v1/models` endpoints.
- Alias-to-target model rewriting.
- Bounded per-alias queueing and concurrency limits.
- Command-backed profile ensure hooks for LM Studio and similar hosts.
- Response telemetry for route, status, duration, bytes, and usage tokens.
- Linux tarball packaging with systemd installation.
- Docker and Vagrant smoke tests.
- Consistent OpenAI-shaped router errors.
- Request IDs in responses and router logs.

Useful next work:

- Add readiness checks for configured backends.
- Add configurable upstream transport timeouts.
- Capture streaming completion telemetry without buffering streams.
- Publish example configs for common LM Studio, Ollama, and vLLM setups.

## Phase 2: Native Backend Adapters

Status: planned.

Command hooks are a good bridge, but the router needs native adapters for
backends that expose enough local state.

- LM Studio adapter: inspect loaded model, context length, parallel slots, TTL,
  and active slot state.
- Ollama adapter: inspect model availability, keepalive policy, and active load.
- vLLM/SGLang adapter: expose server readiness, model limits, batching state,
  and GPU pressure where available.
- Adapter-level decisions for passive JIT load, explicit profile load, or
  `503` when a backend is busy switching.
- Backend lifecycle telemetry: load duration, unload events, profile mismatch,
  and rejected switches.

## Phase 3: Operator Telemetry And Evals

Status: planned.

The router should make quality and performance decisions measurable instead of
vibe-based.

- Structured request logs with request ID, alias, target model, upstream model,
  queue wait, ensure duration, first-token latency, total duration, status, and
  token usage.
- Optional local JSONL telemetry sink for offline analysis.
- Small repeatable eval harness for real agent workflows:
  - commit hook failure handling
  - push and remote-branch verification
  - MR/PR creation and status checks
  - CI failure parsing
  - long-context repo editing
  - security and SRE review prompts
- Model scorecards that combine quality, latency, throughput, memory use, and
  failure modes.

## Phase 4: Policy Routing

Status: planned.

Once telemetry and evals exist, aliases can become policy-backed instead of
hard-coded to one model.

- `local-coder-auto` route that selects a small, large, or cloud-approved model
  based on prompt size, requested tools, risk class, and current backend state.
- Deterministic routing policies that are explainable in logs.
- Budget and privacy gates for optional remote fallbacks.
- Second-pass review workflows where a stronger model audits selected outputs.
- Graceful degradation when local GPUs are hot, busy, or offline.

## Phase 5: Production Operations

Status: planned.

This phase turns the router from a lab service into durable local infrastructure.

- Auth for non-loopback deployments.
- Rate limits by client, alias, or token.
- Admin endpoint or CLI for draining, reloading config, and inspecting state.
- Prometheus metrics.
- Signed release artifacts and package repository support.
- Upgrade and rollback runbooks.
- Omarchy and desktop integration profiles.

## Current Bias

Prioritize observability, explicit profiles, and evals before automatic routing.
The project should earn trust by showing exactly what happened for each request
before it starts making hidden model-selection decisions.
