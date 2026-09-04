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

## Omarchy

Omarchy support should be an integration profile, not a fork of the core router.
See `integrations/omarchy/README.md` for the expected plugin layout and safety
constraints.

## macOS ARM

macOS support should arrive after the Linux service is stable:

- Homebrew tap under `devrail-dev/tap`
- launchd plist
- LM Studio adapter using macOS paths
- no local GPU assumptions for the first macOS release
