#!/usr/bin/env bash
# Isolated process/volume test. Never select an existing service or production volume.
set -euo pipefail
image=${REL024_QA_IMAGE:-whiterun-rel024:qa}
qa_dir=$(mktemp -d /tmp/rel024-container.XXXXXX)
qa_name="rel024-qa-$(basename "$qa_dir")"
qa_volume="${qa_name}-data"
cleanup() {
  docker rm -f "$qa_name" >/dev/null 2>&1 || true
  docker volume rm "$qa_volume" >/dev/null 2>&1 || true
}
trap cleanup EXIT
CGO_ENABLED=0 go test -c -o "$qa_dir/bot-tests" ./cmd/bot
docker volume create "$qa_volume" >/dev/null
docker run -d --name "$qa_name" --network none --memory 128m --cpus 0.5 \
  --restart unless-stopped --stop-timeout 5 --cap-drop ALL --security-opt no-new-privileges \
  --read-only --tmpfs /tmp:rw,nosuid,size=16m,mode=1777 \
  --mount "type=bind,src=$qa_dir/bot-tests,dst=/qa-tests,readonly" \
  --mount "type=volume,src=$qa_volume,dst=/home/bard/data" \
  -e REL024_FIXTURE_DB=/home/bard/data/qa.db -e REL024_FIXTURE_MODE=watchdog \
  -e BARD_LOG_DB_PATH=/home/bard/data --entrypoint /qa-tests \
  "$image" -test.run='^TestREL024ProcessFixture$' -test.v >/dev/null
wait_ready() {
  local expected=$1
  local polls=${2:-150}
  for ((i=0; i<polls; i++)); do
    local seen
    seen=$(docker logs "$qa_name" 2>&1 | awk '/REL024_READY/{n++} END{print n+0}')
    if (( seen >= expected )); then return; fi
    sleep 0.2
  done
  docker logs "$qa_name"
  return 1
}
wait_ready 1
wait_ready 2 350
watchdog_ms=$(docker logs "$qa_name" 2>&1 | sed -n 's/.*REL024_WATCHDOG_RESTART detection_ms=//p' | head -1)
if [[ -z "$watchdog_ms" ]] || (( watchdog_ms > 60000 )); then echo "FAIL watchdog detection: ${watchdog_ms}ms"; exit 1; fi
printf 'Functional watchdog detection and real process exit: %sms; limit: 60000ms\n' "$watchdog_ms"
start_ms=$(date +%s%3N)
docker exec "$qa_name" touch /home/bard/data/qa.db.inject-panic
wait_ready 3
elapsed_ms=$(( $(date +%s%3N) - start_ms ))
if (( elapsed_ms > 30000 )); then echo "FAIL restart took ${elapsed_ms}ms"; exit 1; fi
printf 'Fatal worker panic automatic restart: %sms; limit: 30000ms\n' "$elapsed_ms"
docker stats --no-stream --format 'memory={{.MemUsage}} cpu={{.CPUPerc}} pids={{.PIDs}}' "$qa_name"
docker inspect --format 'restarts={{.RestartCount}} network={{.HostConfig.NetworkMode}} memory_limit={{.HostConfig.Memory}}' "$qa_name"
docker stop "$qa_name" >/dev/null
docker start "$qa_name" >/dev/null
wait_ready 4
docker logs "$qa_name" 2>&1
docker stop "$qa_name" >/dev/null
docker run --rm --network none --read-only --tmpfs /tmp:rw,size=16m,mode=1777 \
  --mount "type=bind,src=$qa_dir/bot-tests,dst=/qa-tests,readonly" \
  --mount "type=volume,src=$qa_volume,dst=/home/bard/data" \
  -e REL024_FIXTURE_DB=/home/bard/data/qa.db -e REL024_FIXTURE_MODE=verify \
  -e BARD_LOG_DB_PATH=/home/bard/data --entrypoint /qa-tests \
  "$image" -test.run='^TestREL024ProcessFixture$' -test.v
printf 'PASS disposable container/volume; 123/123 persisted positions; no real Discord session\n'
