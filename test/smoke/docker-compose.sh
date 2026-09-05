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

cleanup() {
	compose down --remove-orphans --volumes >/dev/null 2>&1 || true
	if [ -n "$docker_helper_dir" ]; then
		rm -rf "$docker_helper_dir"
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

echo "docker compose smoke passed"
