#!/usr/bin/env python3
"""Run from the Compose project directory; stdout is safe audit JSONL.

No raw message, query, error, stderr, URL, environment or header is exported.
Ctrl-C stops log collection only. It does not stop/restart the Bard service.
"""
import datetime
import json
import re
import subprocess
import sys

EVENTS = set("""
play_request_start discord_acknowledged voice_join_complete queue_inserted
play_request_total collection_metadata_complete first_track_resolution_start
first_track_resolution_complete first_track_committed
background_collection_resolution_start background_collection_resolution_complete
collection_prefix_committed next_track_selected track_playback_start playback_start
ffmpeg_start stream_resolve_start yt_dlp_start yt_dlp_complete
audio_source_start stream_process_started stream_resolve_complete stream_url_resolved
stream_resolve_incomplete yt_dlp_first_byte ffmpeg_first_input ffmpeg_first_output
first_ogg_packet first_opus_packet first_opus_frame first_voice_frame_attempt
first_discord_send first_audio_frame next_track_start ffmpeg_complete
audio_source_exit playback_context_complete playback_complete track_playback_end
track_failure player_transition player_failure_limit player_loop_exit
dave_ready_wait_start dave_ready_wait_complete voice_unhealthy voice_recovered
voice_disconnected collection_position_failed
""".split())
NUMERIC = set("""
sequence position collection_id elapsed_ms request_elapsed_ms exit_code yt_exit_code
ffmpeg_exit_code ready pending consecutive_failures workers remaining through_position
spotify_tracks youtube_resolved youtube_failed skipped_positions unfinished candidates
""".split())
BOOLEAN = {"halted", "next_pending", "canceled", "success", "main_ready", "voice_ready", "cache_known"}
ENUMS = {
    "category": set("eof canceled yt_dlp_exit source_http_403 source_gone source_expired source_broken_pipe ffmpeg_exit ffmpeg_start yt_dlp_start no_audio voice_send voice_speaking unexpected_cancellation playback_failure playback_panic audio_stall decoder_after_eof source_after_eof metadata stream ffmpeg".split()),
    "purpose": set("playback audio_decode spotify_music_search spotify_video_search spotify_candidate_verification direct_video_metadata flat_search canonical_audio_search selected_candidate_verification".split()),
    "cancel_reason": {"stop", "skip", "leave", "context canceled", "context deadline exceeded"},
}
PATTERNS = {
    "provider_challenge": ("not a bot", "confirm you’re not a bot", "confirm you're not a bot"),
    "http_403": ("403",), "rate_limit": ("429", "too many requests", "rate limit"),
    "decoder_invalid_input": ("invalid data found",),
    "canceled": ("context canceled",), "deadline": ("deadline exceeded", "timeout"),
    "voice_readiness": ("dave readiness",), "broken_pipe": ("broken pipe",),
    "no_audio": ("without opus", "no_audio"),
}
ALIASES = {"guild_id": {}, "interaction_id": {}}


def sanitize(line):
    try:
        row = json.loads(line)
    except (ValueError, TypeError):
        return None
    if not isinstance(row, dict):
        return None
    event = row.get("msg")
    if not isinstance(event, str) or event not in EVENTS:
        event = row.get("stage")
    if not isinstance(event, str) or event not in EVENTS:
        return None
    out = {"event": event}
    for key, aliases in ALIASES.items():
        value = str(row.get(key, ""))
        if re.fullmatch(r"[0-9]{1,25}", value):
            out[key + "_alias"] = aliases.setdefault(value, len(aliases) + 1)
    stamp = row.get("time")
    if isinstance(stamp, str) and re.fullmatch(r"[0-9T:.+Z-]{10,40}", stamp):
        out["time"] = stamp
    for key in NUMERIC:
        value = row.get(key)
        if type(value) in (int, float):
            out[key] = value
    for key in BOOLEAN:
        if type(row.get(key)) is bool:
            out[key] = row[key]
    for key, allowed in ENUMS.items():
        if key in row:
            value = row[key]
            out[key] = value if value is None or (isinstance(value, str) and value in allowed) else "other"
    diagnostic = " ".join(str(row.get(key, "")) for key in
                          ("error", "yt_stderr_tail", "ffmpeg_stderr_tail", "cause", "parent_error"))
    out["has_error"] = bool(row.get("error"))
    out["error_flags"] = [name for name, patterns in PATTERNS.items()
                          if any(pattern in diagnostic.lower() for pattern in patterns)]
    return out


def main():
    print(json.dumps({"capture_schema": 1, "started_utc": datetime.datetime.now(datetime.timezone.utc).isoformat()}), flush=True)
    process = subprocess.Popen(
        ["docker", "compose", "logs", "--follow", "--since", "1m", "--no-color", "--no-log-prefix", "bard"],
        stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, text=True,
    )
    count = 0
    try:
        for line in process.stdout:
            record = sanitize(line)
            if record is not None:
                print(json.dumps(record, allow_nan=False), flush=True)
                count += 1
    except KeyboardInterrupt:
        pass
    finally:
        if process.poll() is None:
            process.terminate()
        try:
            process.wait(timeout=5)
        except subprocess.TimeoutExpired:
            process.kill()
            process.wait()
        print(json.dumps({"capture_end": True, "records": count}), flush=True)
        if count == 0:
            print("No matching lifecycle records captured; check Compose project/service and Docker access.", file=sys.stderr)


if __name__ == "__main__":
    main()
