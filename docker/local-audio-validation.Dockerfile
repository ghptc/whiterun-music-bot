# Build locally from the same reviewed source as the runtime image. Never build
# or launch this harness on production without separate explicit authorization.
ARG TEST_ENV_IMAGE=whiterun-music-bot-bard:local-audio-test-env
ARG RUNTIME_IMAGE=whiterun-music-bot-bard:local-audio-validated
FROM ${TEST_ENV_IMAGE} AS validation-build
RUN CGO_ENABLED=0 go test -c -o /out/local-audio-validation ./internal/discord

FROM ${RUNTIME_IMAGE}
COPY --from=validation-build /out/local-audio-validation /usr/local/bin/local-audio-validation
COPY --chown=bard:bard internal/player/testdata/local-audio/validation.flac /opt/bard-validation/validation.flac
ENV BARD_TEST_AUDIO_DIR=/opt/bard-validation
ENTRYPOINT ["local-audio-validation", "-test.run=^TestLocalAudioDiscordLive$", "-test.v", "-test.timeout=90s"]
