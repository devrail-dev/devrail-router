# Benchmarking

DevRail Router includes a small streamed benchmark harness for comparing
OpenAI-compatible model aliases such as `local-coder`.

The harness sends fixed chat-completion cases with `stream=true` and
`stream_options.include_usage=true`, then writes one JSON object per case to
stdout. Each result captures:

- case ID
- model alias
- router request ID, when returned
- HTTP status
- time to first SSE event
- total request duration
- response bytes
- prompt, completion, and total tokens when the backend emits streamed usage
- a short first-content sample for sanity checking

## Local-Coder Baseline

Run the default coding-oriented cases against the llm-srv router:

```sh
go run ./cmd/devrail-router bench \
  -base-url http://llm-srv-01.mfsoho.linkridge.net:18080/v1 \
  -model local-coder \
  -cases test/bench/local-coder.cases.json \
  -max-tokens 512
```

Save a baseline:

```sh
go run ./cmd/devrail-router bench \
  -base-url http://llm-srv-01.mfsoho.linkridge.net:18080/v1 \
  -model local-coder \
  -cases test/bench/local-coder.cases.json \
  -max-tokens 512 \
  > local-coder-baseline.jsonl
```

Run the same cases against another alias, such as an experimental
`local-coder-parallel`, by changing `-model` only. Keeping the case file and
token cap stable makes queue wait, first-token latency, duration, and token
throughput easier to compare in Grafana.

## Custom Cases

Case files are JSON arrays. A case can use a simple `prompt`:

```json
[
  {
    "id": "small-refactor",
    "prompt": "Refactor this Go function and explain the tradeoff."
  }
]
```

Or an explicit OpenAI-style message list:

```json
[
  {
    "id": "reviewer",
    "messages": [
      {"role": "system", "content": "You are a concise Go reviewer."},
      {"role": "user", "content": "Find the highest-risk bug in this proxy."}
    ]
  }
]
```

Use stable, short IDs. They appear in JSONL output and make it easier to line
up command results with router request IDs, logs, and Prometheus samples.
