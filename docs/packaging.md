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
