# syntax=docker/dockerfile:1
FROM golang:1.27-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN go vet ./... && go test ./... && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/bard ./cmd/bot

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
ENV PATH="/opt/yt-dlp/bin:${PATH}" \
    DENO_DIR=/tmp/deno \
    PYTHONDONTWRITEBYTECODE=1
RUN ffmpeg -version && yt-dlp --version && deno --version \
    && ffmpeg -hide_banner -loglevel error -f lavfi -i anullsrc=r=48000:cl=stereo \
       -t 0.1 -c:a libopus -f opus -y /dev/null
USER bard
WORKDIR /home/bard
ENTRYPOINT ["bard"]
