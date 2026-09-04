# Architecture

DevRail Router is a local-first OpenAI-compatible gateway for private AI
infrastructure. It is designed for operators who run a mix of local inference
backends, agent frameworks, coding tools, and optional cloud fallback.

## Initial Scope

- Present stable model aliases to clients such as opencode, Hermes, and
  OpenClaw.
- Route aliases to OpenAI-compatible backends such as LM Studio.
- Preserve a small, debuggable Linux service that can run under systemd.
- Collect enough request and backend signal to support smarter routing later.

## Layers

1. Core service: HTTP API, alias registry, routing policy, auth, and telemetry.
2. Runtime packaging: systemd, Linux tarballs, container image, and eventually
   Homebrew or launchd for macOS.
3. Host integrations: LM Studio, Ollama, vLLM, SGLang, GPU telemetry, thermal
   state, and desktop integrations such as Omarchy.

## Backend Philosophy

DevRail Router should not replace backend-specific lifecycle features. If LM
Studio can Just-In-Time load a model safely, the router should let it. The
router should only intervene when a profile requires specific context length,
parallelism, TTL, GPU offload, auth, or scheduling policy.

## Near-Term Routing

The first router is deliberately simple:

- Clients request a DevRail model alias.
- The router rewrites the request to the configured backend model.
- The backend handles inference.

Future routing can add deterministic policy, queueing, health-aware selection,
RouteLLM-style strong/weak model routing, and second-pass review workflows.
