#!/usr/bin/env bash
set -euo pipefail

cd /vagrant

goarch="${DEVRAIL_VAGRANT_GOARCH:-}"
if [ -z "$goarch" ]; then
	case "$(uname -m)" in
	aarch64 | arm64) goarch="arm64" ;;
	x86_64 | amd64) goarch="amd64" ;;
	*)
		echo "unsupported VM architecture: $(uname -m)" >&2
		exit 2
		;;
	esac
fi

package_path=$(find dist -maxdepth 1 -name "devrail-router_*_linux_${goarch}.tar.gz" | sort | tail -n 1)
if [ -z "$package_path" ]; then
	echo "missing Linux ${goarch} package in dist/" >&2
	echo "run: make package GOOS=linux GOARCH=${goarch}" >&2
	exit 2
fi

apt-get update
DEBIAN_FRONTEND=noninteractive apt-get install -y ca-certificates curl tar

work_dir=$(mktemp -d)
trap 'rm -rf "$work_dir"' EXIT

tar -xzf "$package_path" -C "$work_dir"
package_dir=$(find "$work_dir" -maxdepth 1 -type d -name "devrail-router_*_linux_${goarch}" | head -n 1)

cd "$package_dir"
START_SERVICE=1 ./packaging/linux/install.sh

systemctl is-enabled devrail-router.service
systemctl is-active devrail-router.service

for _ in $(seq 1 30); do
	if curl -fsS http://127.0.0.1:8080/healthz >/dev/null 2>&1; then
		break
	fi
	sleep 1
done

curl -fsS http://127.0.0.1:8080/healthz | grep -q '"status":"ok"'
curl -fsS http://127.0.0.1:8080/v1/models | grep -q '"id":"local-coder"'

printf '# preserved by vagrant smoke\n' >>/etc/devrail/router.yaml
./packaging/linux/install.sh
grep -q 'preserved by vagrant smoke' /etc/devrail/router.yaml

systemctl restart devrail-router.service
systemctl is-active devrail-router.service

for _ in $(seq 1 30); do
	if curl -fsS http://127.0.0.1:8080/healthz >/dev/null 2>&1; then
		break
	fi
	sleep 1
done

curl -fsS http://127.0.0.1:8080/healthz | grep -q '"status":"ok"'

echo "vagrant smoke passed"
