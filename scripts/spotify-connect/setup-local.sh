#!/usr/bin/env bash
# Build only the reviewed upstream release; never install system packages or services.
set -euo pipefail
umask 077
root="${BARD_SPOTIFY_LOCAL_ROOT:-$PWD/.local/spotify-connect}"
[[ "$root" = /* ]] || { echo 'Use an absolute runtime root' >&2; exit 1; }
[[ ! -L "$root" && ! -L "$root/config" && ! -L "$root/run" && ! -L "$root/bin" ]] || { echo 'Refusing symlink runtime directories' >&2; exit 1; }
[[ "$root" != *"'"* && "$root" != *$'\n'* ]] || { echo 'Unsupported runtime pathname' >&2; exit 1; }
mkdir -p "$root/config" "$root/run" "$root/bin"
chmod 700 "$root" "$root/config" "$root/run" "$root/bin"
if [[ ! -d "$root/source/.git" ]]; then
  git clone --branch v0.10.5 --depth 1 https://github.com/devgianlu/go-librespot.git "$root/source"
fi
expected=90a4fdaf0314269e3fa25698a5f4270c026d2c03
[[ "$(git -C "$root/source" rev-parse HEAD)" = "$expected" ]] || { echo 'Upstream commit mismatch' >&2; exit 1; }
[[ -z "$(git -C "$root/source" status --porcelain)" ]] || { echo 'Upstream checkout is modified' >&2; exit 1; }
pkg-config --exists ogg vorbis flac libmpg123 alsa
(cd "$root/source" && go build -o "$root/bin/go-librespot" ./cmd/daemon)
if [[ ! -e "$root/run/pcm" ]]; then mkfifo -m 600 "$root/run/pcm"; fi
[[ -p "$root/run/pcm" && ! -L "$root/run/pcm" ]] || { echo 'Unsafe FIFO' >&2; exit 1; }
chmod 600 "$root/run/pcm"
if [[ -e "$root/config/config.yml" ]]; then echo 'Existing config preserved'; else
cat > "$root/config/config.yml" <<CONFIG
log_level: info
device_name: Bard Local
zeroconf_enabled: false
credentials:
  type: device_auth
server:
  enabled: true
  address: 127.0.0.1
  port: 3678
  allow_origin: ''
audio_backend: pipe
audio_output_pipe: '$root/run/pcm'
audio_output_pipe_format: s16le
audio_output_pipe_wait_for_reader: false
bitrate: 320
flac_enabled: false
initial_volume: 100
ignore_last_volume: true
external_volume: false
normalisation_disabled: false
normalisation_pregain: 0
crossfade_duration: 0
disable_autoplay: true
cache:
  enabled: false
CONFIG
fi
sha256sum "$root/bin/go-librespot" > "$root/build.sha256"
printf 'Prepared %s\nStart in a local terminal:\nBARD_SPOTIFY_LOCAL_ROOT=%q scripts/spotify-connect/run-local.sh\n' "$expected" "$root"
