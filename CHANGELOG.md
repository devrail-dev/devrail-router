# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- Configurable keyword preclassifier for high-confidence route selection before
  falling back to the LLM classifier.
- Route-classifier benchmark corpus and runner for comparing guardrail,
  keyword, and live OpenAI-compatible classifier policies.
- Request metrics now include the selected `route_rule` label when a routing
  decision is made.
- `/metrics` now exposes `devrail_router_prompt_chars` buckets with request
  labels so prompt-size guardrail behavior can be tuned from real traffic.
- `/metrics` now exposes `devrail_router_inflight_requests` so open streams
  can be distinguished from completed or stalled client-side requests.
- Opt-in command-backed model profile ensure hooks.
- Roadmap for maturing DevRail Router from a single-backend gateway into an
  observable local inference control plane.
- Request IDs are now added to router responses and structured logs.
- Streaming backend responses now log first event latency and usage tokens when
  the backend sends OpenAI-compatible streamed usage chunks.
- `/metrics` now exposes Prometheus-compatible request, queue, latency, byte,
  and token telemetry.
- `/metrics` now exposes model ensure/profile-switch duration telemetry.

### Changed

- Router-originated proxy and request validation failures now return
  OpenAI-shaped JSON errors consistently.

## [1.0.0] - 2026-03-01

### Added

- Makefile with all 7 language ecosystems (Python, Bash, Terraform, Ansible, Ruby, Go, JavaScript/TypeScript)
- `make init` / `make _init` config scaffolding target
- CI workflows: lint, format, test, security, scan, docs
- Pre-commit hooks for all supported languages (commented out by default)
- Agent instruction files (CLAUDE.md, AGENTS.md, .cursorrules, .opencode/agents.yaml)
- DevRail compliance badge in README
- Retrofit guide for adding DevRail to existing repositories
- `.devrail.yml` with all 7 languages listed (commented out)
- `.editorconfig`, `.gitignore`, `DEVELOPMENT.md`, `CHANGELOG.md`, `LICENSE`
