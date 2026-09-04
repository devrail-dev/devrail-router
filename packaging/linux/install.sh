#!/usr/bin/env bash
set -euo pipefail

SERVICE_NAME="devrail-router"
SERVICE_USER="${SERVICE_USER:-devrail-router}"
SERVICE_GROUP="${SERVICE_GROUP:-devrail-router}"
PREFIX="${PREFIX:-/usr/local}"
BIN_DIR="${BIN_DIR:-${PREFIX}/bin}"
CONFIG_DIR="${CONFIG_DIR:-/etc/devrail}"
STATE_DIR="${STATE_DIR:-/var/lib/devrail-router}"
SYSTEMD_DIR="${SYSTEMD_DIR:-/etc/systemd/system}"
ENABLE_SERVICE="${ENABLE_SERVICE:-1}"
START_SERVICE="${START_SERVICE:-0}"
FORCE_CONFIG="${FORCE_CONFIG:-0}"
DRY_RUN="${DRY_RUN:-0}"

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
package_root="$(cd -- "${script_dir}/../.." && pwd)"
binary_src="${DEVRAIL_ROUTER_BINARY:-${package_root}/devrail-router}"
config_src="${DEVRAIL_ROUTER_CONFIG:-${package_root}/configs/router.example.yaml}"
unit_src="${DEVRAIL_ROUTER_SYSTEMD_UNIT:-${package_root}/packaging/systemd/devrail-router.service}"

if [[ $EUID -eq 0 ]]; then
	sudo_cmd=()
else
	sudo_cmd=(sudo)
fi

run() {
	printf '+ %q' "$@"
	printf '\n'
	if [[ "${DRY_RUN}" == "1" ]]; then
		return 0
	fi
	"${sudo_cmd[@]}" "$@"
}

need_file() {
	local path="$1"
	if [[ ! -f "${path}" ]]; then
		echo "missing required file: ${path}" >&2
		exit 1
	fi
}

need_file "${binary_src}"
need_file "${config_src}"
need_file "${unit_src}"

if ! getent group "${SERVICE_GROUP}" >/dev/null 2>&1; then
	run groupadd --system "${SERVICE_GROUP}"
fi

if ! id -u "${SERVICE_USER}" >/dev/null 2>&1; then
	run useradd --system --gid "${SERVICE_GROUP}" --home-dir "${STATE_DIR}" --shell /usr/sbin/nologin "${SERVICE_USER}"
fi

run install -d -m 0755 "${BIN_DIR}" "${CONFIG_DIR}" "${SYSTEMD_DIR}"
run install -d -m 0750 -o "${SERVICE_USER}" -g "${SERVICE_GROUP}" "${STATE_DIR}"
run install -m 0755 "${binary_src}" "${BIN_DIR}/devrail-router"

if [[ ! -f "${CONFIG_DIR}/router.yaml" || "${FORCE_CONFIG}" == "1" ]]; then
	run install -m 0640 -o root -g "${SERVICE_GROUP}" "${config_src}" "${CONFIG_DIR}/router.yaml"
else
	echo "keeping existing ${CONFIG_DIR}/router.yaml"
fi

run install -m 0644 "${unit_src}" "${SYSTEMD_DIR}/${SERVICE_NAME}.service"

if command -v systemctl >/dev/null 2>&1; then
	run systemctl daemon-reload
	if [[ "${ENABLE_SERVICE}" == "1" ]]; then
		run systemctl enable "${SERVICE_NAME}.service"
	fi
	if [[ "${START_SERVICE}" == "1" ]]; then
		run systemctl restart "${SERVICE_NAME}.service"
	fi
else
	echo "systemctl not found; installed files but did not enable service"
fi

echo "DevRail Router installed."
echo "Config: ${CONFIG_DIR}/router.yaml"
echo "Binary: ${BIN_DIR}/devrail-router"
