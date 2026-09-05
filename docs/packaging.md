# Packaging

DevRail Router packages the portable service separately from host-specific
integration.

## Linux First

The primary Linux installation target is:

- Binary: `/usr/local/bin/devrail-router`
- Config: `/etc/devrail/router.yaml`
- State: `/var/lib/devrail-router`
- Service user: `devrail-router`
- Service manager: systemd

The systemd unit lives at `packaging/systemd/devrail-router.service`.

Build a tarball for the current platform:

```sh
make package
```

Build a Linux AMD64 tarball and run package smoke checks:

```sh
make package-smoke GOOS=linux GOARCH=amd64
```

The generated archive is written to `dist/` with this layout:

```text
devrail-router_<version>_<os>_<arch>/
  devrail-router
  configs/router.example.yaml
  packaging/linux/install.sh
  packaging/systemd/devrail-router.service
  docs/
  README.md
  CHANGELOG.md
  LICENSE
```

Install from an unpacked tarball:

```sh
sudo ./packaging/linux/install.sh
```

The installer creates the `devrail-router` service user and group when missing,
installs the binary, installs a default config only if `/etc/devrail/router.yaml`
does not already exist, installs the systemd unit, reloads systemd, and enables
the service. It does not start the service unless `START_SERVICE=1` is set.

Useful installer flags:

```sh
DRY_RUN=1 ./packaging/linux/install.sh
FORCE_CONFIG=1 sudo ./packaging/linux/install.sh
START_SERVICE=1 sudo ./packaging/linux/install.sh
```

## Container Image

A container image is useful for proxy-only deployments and CI smoke tests. It is
not the first-class LM Studio host install path because local desktop app and GPU
integration are easier from a native Linux service.

Build the image:

```sh
make docker-build
```

Run the Compose smoke stack:

```sh
make docker-smoke
```

The smoke stack uses `configs/router.docker.yaml` and starts two services:

- `router`: DevRail Router listening on an ephemeral `127.0.0.1` host port
- `mock-openai`: a tiny OpenAI-compatible backend used only for tests

Set `DEVRAIL_ROUTER_HOST_PORT` to override the host port:

```sh
DEVRAIL_ROUTER_HOST_PORT=18081 make docker-smoke
```

The smoke script verifies:

- `/healthz` responds
- `/v1/models` exposes configured aliases
- `local-coder` is rewritten to the configured target model
- `local-coder-large` is rewritten separately
- backend auth is injected from `MOCK_OPENAI_API_KEY`
- chat completion requests are proxied end to end

This is intentionally not a replacement for systemd acceptance testing. Docker
does not prove native installer behavior, service restart behavior, journald
logging, LM Studio desktop integration, `lms` discovery, or GPU/runtime behavior.

## Omarchy

Omarchy support should be an integration profile, not a fork of the core router.
See `integrations/omarchy/README.md` for the expected plugin layout and safety
constraints.

## Vagrant Acceptance Smoke

Vagrant is optional and aimed at native Linux installer acceptance. It is useful
for the systemd/user/config paths that Docker does not model cleanly.

```sh
make vagrant-smoke
```

The Vagrant smoke test builds a Linux package, boots an Ubuntu VM, installs
DevRail Router through `packaging/linux/install.sh`, starts the systemd service,
checks `/healthz` and `/v1/models`, verifies reinstall preserves
`/etc/devrail/router.yaml`, restarts the service, and destroys the VM after a
successful run.

On Apple Silicon Macs, the default Vagrant box is ARM64 and `make vagrant-smoke`
builds a Linux ARM64 package. On x86 hosts, the default package architecture is
Linux AMD64.

Use `DEVRAIL_VAGRANT_BOX` to try another Linux box:

```sh
DEVRAIL_VAGRANT_BOX=bento/fedora-40 make vagrant-smoke
```

Use `VAGRANT_PROVIDER` and `VAGRANT_GOARCH` to override provider or package
architecture:

```sh
VAGRANT_PROVIDER=virtualbox VAGRANT_GOARCH=amd64 make vagrant-smoke
```

Keep the VM after a run for debugging:

```sh
VAGRANT_DESTROY=0 make vagrant-smoke
```

The Vagrant harness is not run in CI yet because it needs a local provider such
as VirtualBox, libvirt, or another Vagrant-compatible VM backend.

## macOS ARM

macOS support should arrive after the Linux service is stable:

- Homebrew tap under `devrail-dev/tap`
- launchd plist
- LM Studio adapter using macOS paths
- no local GPU assumptions for the first macOS release
