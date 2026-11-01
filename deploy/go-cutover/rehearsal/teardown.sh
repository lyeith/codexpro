#!/usr/bin/env bash
# Remove the rehearsal instance: units, drop-ins, state, token and fixture.
# Keeps only the JSON/text evidence under $R/../ (cutover/rollback evidence dirs).
set -euo pipefail
R="${REHEARSAL_ROOT:-$HOME/Projects/codexpro-cutover-evidence/rehearsal}"
UNITS="$HOME/.config/systemd/user"
systemctl --user stop codexpro-rehearsal.service codexpro-rehearsal.socket 2>/dev/null || true
rm -f "$UNITS/codexpro-rehearsal.socket" "$UNITS/codexpro-rehearsal.service"
rm -rf "$UNITS/codexpro-rehearsal.service.d"
systemctl --user daemon-reload
systemctl --user reset-failed codexpro-rehearsal.service codexpro-rehearsal.socket 2>/dev/null || true
if [[ -f "$R/live-ts-home-listing-before.txt" ]]; then
  ls -la --time-style=full-iso "$HOME/.codexpro" "$HOME/.codexpro/runtime" >"$R/live-ts-home-listing-after.txt"
fi
chmod -R u+w "$R" 2>/dev/null || true
rm -rf "$R/ts-home" "$R/go-state" "$R/fixture" "$R/config"
if (exec 3<>/dev/tcp/127.0.0.1/7212) 2>/dev/null; then echo "WARNING: 127.0.0.1:7212 still accepts connections" >&2; fi
echo "rehearsal removed"
