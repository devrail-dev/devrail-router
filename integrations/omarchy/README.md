# Omarchy Integration

Omarchy support is planned as a plugin or installer profile for hosts where
DevRail Router should feel native in the desktop environment.

Reference: <https://plugins.omarchy.org/develop.html>

## Direction

- Keep the core router service independent of Omarchy.
- Use Omarchy plugin files only for status, controls, and setup affordances.
- Prefer a user-owned plugin under `~/.config/omarchy/plugins/`.
- Validate plugin folders with `omarchy plugin validate`.
- Lint QML entrypoints with `qmllint -I "$OMARCHY_PATH/shell"`.

## Likely Plugin Shape

A first plugin should probably be a `bar-widget` with a details panel showing:

- router status
- active backend
- loaded model/profile when known
- recent routing decisions
- thermal or health warnings when available

The plugin must not start a second Quickshell process. Any privileged setup
belongs in the Linux installer or an explicit admin command, not in the QML
runtime.
