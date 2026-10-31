# Verified Release API failure

Source: 35bd329e46094c63e59c7ffd3f88beb4d74b366b; failed run 41244695471.
The original wrapper discarded gh stderr and exit code, so its historical exit
code cannot be recovered from that log. The identical read-only verification
was reproduced with an Actions token in run 41247971071:

`gh api repos/ghptc/whiterun-music-bot/`

Exit 1, HTTP 404, category missing_or_inaccessible. Earlier main protection,
rulesets and CI-list calls passed. `workflow_run` selected the correct merge SHA;
the repository metadata request inside workflow_run validation failed before
check/job evidence could be evaluated. get('') appended an invalid trailing slash.
This is an endpoint-construction defect, not evidence of insufficient permission.

Fix: omit the slash for an empty API path. Preserve all exact-SHA, protected-main,
trusted-workflow, successful-check, digest and attestation requirements. Report
sanitized endpoint, exit and HTTP category on API failure without response bodies,
headers or raw stderr. A 403 remains ambiguous between permission and rate limit.

Effective original token: Actions read, Checks read, Contents read, Metadata read,
Packages write, Attestations write. No additional permission is requested.

Regression tests cover the exact repository endpoint, 401/403/404/500, transport
failure and secret redaction. Existing wrong-SHA tests remain unchanged. Local
release-policy tests pass. The new read-only Actions diagnostic reproduces the
original call chain against the original merge SHA without publishing anything.
GitHub checks for the corrected HEAD must pass before merge; no merge or deploy
is authorized. No EC2 access, image publication or protection changes occurred.

Evidence: https://github.com/ghptc/whiterun-music-bot/actions/runs/41247971071
Original: https://github.com/ghptc/whiterun-music-bot/actions/runs/41244695471
