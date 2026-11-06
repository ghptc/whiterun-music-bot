#!/usr/bin/env bash
set -euo pipefail
umask 077
root="${BARD_SPOTIFY_LOCAL_ROOT:-$PWD/.local/spotify-connect}"
[[ "$root" = /* && -x "$root/bin/go-librespot" ]] || { echo 'Run setup-local.sh first' >&2; exit 1; }
[[ ! -L "$root/run/daemon.pid" ]] || { echo 'Unsafe PID file' >&2; exit 1; }
if [[ -f "$root/run/daemon.pid" ]]; then
 read -r existing_pid < "$root/run/daemon.pid"
 if [[ "$existing_pid" =~ ^[0-9]+$ ]] && kill -0 "$existing_pid" 2>/dev/null; then
  echo 'Recorded local daemon is still running; stop it before starting again' >&2; exit 1
 fi
fi
printf '%s\n' "$$" > "$root/run/daemon.pid"
# Pairing details remain in the operator's terminal. No shell tracing or logs.
exec "$root/bin/go-librespot" --config_dir "$root/config"
