#!/usr/bin/env bash
set -euo pipefail

compose() {
	if docker compose version >/dev/null 2>&1; then
		docker compose "$@"
	else
		docker-compose "$@"
	fi
}

docker_helper_dir=""
tmp_dir=""

cleanup() {
	compose down --remove-orphans --volumes >/dev/null 2>&1 || true
	if [ -n "$docker_helper_dir" ]; then
		rm -rf "$docker_helper_dir"
	fi
	if [ -n "$tmp_dir" ]; then
		rm -rf "$tmp_dir"
	fi
}
trap cleanup EXIT

if [ -f "${HOME}/.docker/config.json" ] &&
	grep -q '"credsStore"[[:space:]]*:[[:space:]]*"desktop"' "${HOME}/.docker/config.json" &&
	! command -v docker-credential-desktop >/dev/null 2>&1; then
	docker_helper_dir=$(mktemp -d)
	{
		printf '#!/usr/bin/env sh\n'
		printf 'case "$1" in\n'
		printf '  get) printf '"'"'{"Username":"","Secret":""}\\n'"'"' ;;\n'
		printf '  list) printf '"'"'{}\\n'"'"' ;;\n'
		printf '  store|erase) cat >/dev/null; exit 0 ;;\n'
		printf '  *) exit 1 ;;\n'
		printf 'esac\n'
	} >"$docker_helper_dir/docker-credential-desktop"
	chmod +x "$docker_helper_dir/docker-credential-desktop"
	export PATH="$docker_helper_dir:$PATH"
fi

compose up --build -d

published_address=$(compose port router 8080)
published_port=${published_address##*:}
router_url="http://127.0.0.1:${published_port:?router port was not published}"

for _ in $(seq 1 60); do
	if curl -fsS "$router_url/healthz" >/dev/null; then
		break
	fi
	sleep 1
done

curl -fsS "$router_url/healthz" | grep -q '"status":"ok"'
curl -fsS "$router_url/v1/models" | grep -q '"id":"local-coder"'

response=$(
	curl -fsS "$router_url/v1/chat/completions" \
		-H 'Content-Type: application/json' \
		-d '{
			"model": "local-coder",
			"messages": [{"role": "user", "content": "Reply with ok."}],
			"max_tokens": 16
		}'
)

echo "$response" | grep -q '"model":"qwen3-coder-30b-a3b-instruct"'
echo "$response" | grep -q 'ok from qwen3-coder-30b-a3b-instruct'

large_response=$(
	curl -fsS "$router_url/v1/chat/completions" \
		-H 'Content-Type: application/json' \
		-d '{
			"model": "local-coder-large",
			"messages": [{"role": "user", "content": "Reply with ok."}],
			"max_tokens": 16
		}'
)

echo "$large_response" | grep -q '"model":"qwen/qwen3.6-35b-a3b"'
echo "$large_response" | grep -q 'ok from qwen/qwen3.6-35b-a3b'

tmp_dir=$(mktemp -d)
delayed_body="$tmp_dir/delayed.json"
queued_headers="$tmp_dir/queued.headers"
queued_body="$tmp_dir/queued.json"

curl -fsS "$router_url/v1/chat/completions" \
	-H 'Content-Type: application/json' \
	-d '{
		"model": "local-coder-large",
		"messages": [{"role": "user", "content": "Hold the slot."}],
		"max_tokens": 16,
		"devrail_mock_delay_ms": 400
	}' >"$delayed_body" &
delayed_pid=$!

sleep 0.05

curl -fsS "$router_url/v1/chat/completions" \
	-D "$queued_headers" \
	-H 'Content-Type: application/json' \
	-d '{
		"model": "local-coder-large",
		"messages": [{"role": "user", "content": "Queue behind the slot."}],
		"max_tokens": 16
	}' >"$queued_body"

wait "$delayed_pid"

queue_wait_ms=$(awk -F': ' 'tolower($1)=="x-devrail-queue-wait-ms" {gsub("\r", "", $2); print $2}' "$queued_headers")
test "${queue_wait_ms:-0}" -gt 0
grep -q 'ok from qwen/qwen3.6-35b-a3b' "$queued_body"
grep -q 'ok from qwen/qwen3.6-35b-a3b' "$delayed_body"

echo "docker compose smoke passed"
