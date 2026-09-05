#!/usr/bin/env bash
set -euo pipefail

cd /vagrant

package_path=$(find dist -maxdepth 1 -name 'devrail-router_*_linux_amd64.tar.gz' | sort | tail -n 1)
if [ -z "$package_path" ]; then
	echo "missing Linux AMD64 package in dist/" >&2
	echo "run: make package GOOS=linux GOARCH=amd64" >&2
	exit 2
fi

apt-get update
DEBIAN_FRONTEND=noninteractive apt-get install -y ca-certificates curl tar

work_dir=$(mktemp -d)
trap 'rm -rf "$work_dir"' EXIT

tar -xzf "$package_path" -C "$work_dir"
package_dir=$(find "$work_dir" -maxdepth 1 -type d -name 'devrail-router_*_linux_amd64' | head -n 1)

cd "$package_dir"
START_SERVICE=1 ./packaging/linux/install.sh

systemctl is-enabled devrail-router.service
systemctl is-active devrail-router.service
curl -fsS http://127.0.0.1:8080/healthz | grep -q '"status":"ok"'
curl -fsS http://127.0.0.1:8080/v1/models | grep -q '"id":"local-coder"'

printf '# preserved by vagrant smoke\n' >>/etc/devrail/router.yaml
./packaging/linux/install.sh
grep -q 'preserved by vagrant smoke' /etc/devrail/router.yaml

systemctl restart devrail-router.service
systemctl is-active devrail-router.service
curl -fsS http://127.0.0.1:8080/healthz | grep -q '"status":"ok"'

echo "vagrant smoke passed"
