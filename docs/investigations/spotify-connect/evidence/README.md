# Validation evidence

These files contain only test summaries and an explicit allowlist of local
playback observations. Raw daemon logs, credentials, pairing codes, access tokens
and account profile/status responses are excluded.

`local-poc.json` is a failed live audio test, despite successful device pairing.
It must never be used as evidence that a complete track was heard locally or
in Discord. Tests use synthetic PCM and fake servers; no Spotify requests occur
in the default suite. Benchmark results are a local performance smoke check,
not a replacement for existing CI budgets or live audio resource measurements.

The security scanner reported no reachable vulnerabilities; it also reported
two module-level advisories outside called/imported vulnerable packages. This
scan covers Bard, not the separately built upstream executable.
