#!/usr/bin/env bash
set -euo pipefail
root="${BARD_SPOTIFY_LOCAL_ROOT:-$PWD/.local/spotify-connect}"
[[ "$root" = /* && -f "$root/run/daemon.pid" ]] || { echo 'No local daemon PID recorded' >&2; exit 1; }
read -r daemon_pid < "$root/run/daemon.pid"
[[ "$daemon_pid" =~ ^[0-9]+$ ]] || exit 1
if [[ ! -e "/proc/$daemon_pid/exe" ]]; then echo 'Local daemon is stopped'; exit 0; fi
[[ "$(readlink "/proc/$daemon_pid/exe")" = "$root/bin/go-librespot" ]] || { echo 'PID no longer belongs to this local daemon' >&2; exit 1; }
mapfile -d '' -t argv < "/proc/$daemon_pid/cmdline"
[[ "${argv[1]:-}" = --config_dir && "${argv[2]:-}" = "$root/config" ]] || { echo 'Configuration ownership mismatch' >&2; exit 1; }
kill -TERM "$daemon_pid"
echo 'Sent SIGTERM to the verified local daemon'
