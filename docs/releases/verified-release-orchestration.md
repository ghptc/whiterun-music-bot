# Verified Release orchestration correction

Approved source: b7d7f73ffb404b42938198bc46b83661036b5748.

Automatic run 41250660493 started after Release CI completed, while the independent
deterministic-ttfa check was still running. ci_evidence correctly denied a pending
check, but publication orchestration had no bounded waiting stage. The correction
adds a preflight state machine with a monotonic 900-second deadline, observations
at most every 10 seconds, and fresh protected-main verification on every pass.
Success continues; terminal non-success, unknown states and wrong SHA reject.
Missing/pending checks wait within the deadline; timeout rejects. Main CI itself
must also complete successfully. API failures remain fail-closed.

Manual run 41252447667 was main/workflow_dispatch at the correct SHA. Its CI lookup
selected max(run.id) across same-SHA push events. Run 41251952309 (v1.0.0 tag) was
newer than the valid main run 41250377872. workflow_run then correctly rejected
the selected tag's head_branch. Fix: filter exact SHA/main before choosing latest;
retain strict workflow/repository/event checks. Same-SHA tag check records cannot
replace the selected main CI job evidence. No dispatch SHA/branch check is relaxed.

The existing workflow_run trigger remains: the bounded wait occurs in publish's
check phase, before build. Manifest generation revalidates successful evidence
without waiting or accepting pending checks. No new infrastructure or retries of
publication, builds or failed workflows are introduced. Existing 30-minute workflow
timeout accommodates the bounded preflight. No permission change is needed.

Fixtures under scripts/releases/testdata contain sanitized real API events.
Regression tests cover tag/main selection, pending-to-success, timeout, terminal
failure/cancelled/skipped, wrong SHA and main advancement. Existing provenance,
API, identity/performance assertions and branch protection remain unchanged.

Final GitHub checks must succeed before merge. No merge, deploy, EC2 access or
manual publication is performed. A successful PR does not itself publish a release;
after approved merge, the new exact main SHA must pass its own checks.

Original evidence:
https://github.com/ghptc/whiterun-music-bot/actions/runs/41250660493
https://github.com/ghptc/whiterun-music-bot/actions/runs/41252447667
