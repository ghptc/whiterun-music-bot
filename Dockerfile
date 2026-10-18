# syntax=docker/dockerfile:1
FROM golang:1.27-bookworm AS test-env
# Process acceptance tests execute real FFmpeg; runtime-stage packages are not
# available here. Verify the encoder by producing Opus, not just finding a binary.
RUN apt-get update && apt-get install -y --no-install-recommends ffmpeg \
    && rm -rf /var/lib/apt/lists/* \
    && ffmpeg -version \
    && ffmpeg -hide_banner -loglevel error -f lavfi -i anullsrc=r=48000:cl=stereo \
       -t 0.1 -c:a libopus -f opus -y /dev/null
WORKDIR /src
COPY go.mod go.sum ./
# Apply the same EDNS workaround used by the runtime to Go dependency downloads.
RUN GODEBUG=netedns0=0 go mod download
COPY cmd ./cmd
COPY internal ./internal

FROM test-env AS build
RUN go vet ./... && go test ./... && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/bard ./cmd/bot && sha256sum /out/bard

FROM denoland/deno:bin-2.5.6 AS deno

FROM debian:bookworm-slim AS runtime
ARG YTDLP_VERSION=2026.08.19
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates ffmpeg python3 python3-venv \
    && python3 -m venv /opt/yt-dlp \
    && /opt/yt-dlp/bin/pip install --no-cache-dir "yt-dlp[default]==${YTDLP_VERSION}" \
    && rm -rf /var/lib/apt/lists/* \
    && useradd --create-home --uid 10001 bard
COPY --from=deno /deno /usr/local/bin/deno
COPY --from=build /out/bard /usr/local/bin/bard
# Docker DNS forwarding on some networks returns OPT-only answers to EDNS
# queries. Disable EDNS for Go; retain normal DNS servers and IPv4/IPv6 dialing.
ENV GODEBUG="netedns0=0" \
    PATH="/opt/yt-dlp/bin:${PATH}" \
    DENO_DIR=/tmp/deno \
    PYTHONDONTWRITEBYTECODE=1
RUN ffmpeg -version && yt-dlp --version && deno --version \
    && ffmpeg -hide_banner -loglevel error -f lavfi -i anullsrc=r=48000:cl=stereo \
       -t 0.1 -c:a libopus -f opus -y /dev/null
USER bard
WORKDIR /home/bard
ENTRYPOINT ["bard"]
