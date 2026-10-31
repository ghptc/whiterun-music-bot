#!/usr/bin/env python3
"""Read an offline SQLite log copy; export allowlisted +/-120s timelines.

No tokens, session payloads, URLs, raw errors or subprocess stderr are exported.
Usage: investigate-voice-lifecycle.py /tmp/bard-logs.db /tmp/docker.log output.json
"""
import collections
import datetime
import hashlib
import json
import pathlib
import sqlite3
import sys

FIELDS = set('time level msg guild_id channel_id voice_channel_id incident_id playback_attempt_id sequence position title video_id main_ready voice_ready cache_known same_channel reason component origin action state previous_state next_state confirmed_ms duration_ms queue_size queue_generation queue_version elapsed_ms attempt category cancel_reason cause close_code code exit_code yt_exit_code ffmpeg_exit_code success'.split())

def safe(payload):
    result = {k: v for k, v in payload.items() if k in FIELDS and isinstance(v, (str, int, float, bool, type(None)))}
    error = str(payload.get('error', payload.get('err', ''))).lower()
    for needle, label in [('4006', 'session_invalid_4006'), ('unexpected eof', 'unexpected_eof'), ('already opened or closed', 'already_opened_or_closed'), ('deadline exceeded', 'deadline_exceeded')]:
        if needle in error:
            result['error_class'] = label
            break
    return result

def main():
    db_path, docker_path, output = map(pathlib.Path, sys.argv[1:])
    db = sqlite3.connect(db_path.resolve().as_uri() + '?mode=ro', uri=True)
    rows = [(i, ns, event, json.loads(payload)) for i, ns, event, payload in db.execute('SELECT id,timestamp_ns,event,payload FROM logs ORDER BY timestamp_ns,id')]
    incidents = []
    for i, ns, event, payload in rows:
        if event != 'voice_unhealthy':
            continue
        if incidents and ns - incidents[-1]['timestamp_ns'] < 120_000_000_000:
            continue  # secondary health observations belong to the same incident
        incidents.append({'id': i, 'timestamp_ns': ns, 'observation': safe(payload)})
    for incident in incidents:
        ns = incident['timestamp_ns']
        incident['window_start_utc'] = datetime.datetime.fromtimestamp((ns / 1e9) - 120, datetime.timezone.utc).isoformat()
        incident['window_end_utc'] = datetime.datetime.fromtimestamp((ns / 1e9) + 120, datetime.timezone.utc).isoformat()
        incident['sqlite_timeline'] = [dict(id=i, **safe(p)) for i, t, _, p in rows if abs(t-ns) <= 120_000_000_000]
        timeline = []
        for line in docker_path.read_text().splitlines():
            try:
                payload = json.loads(line[line.index('{'):])
                stamp = payload.get('time', '')
                t = datetime.datetime.fromisoformat(stamp.replace('Z','+00:00')).timestamp()
                if abs(t - ns/1e9) <= 120:
                    timeline.append(safe(payload))
            except (ValueError, TypeError):
                continue
        incident['docker_timeline'] = timeline
    result = {
        'sqlite_sha256': hashlib.sha256(db_path.read_bytes()).hexdigest(),
        'docker_sha256': hashlib.sha256(docker_path.read_bytes()).hexdigest(),
        'records_scanned': len(rows), 'first': rows[0][3]['time'], 'last': rows[-1][3]['time'],
        'event_counts': dict(collections.Counter(row[2] for row in rows)),
        'transport_incidents': incidents,
        'explicit_leave_requests': [dict(id=i, **safe(p)) for i, _, event, p in rows if event == 'playback_cancel_requested' and p.get('cancel_reason') == 'leave'],
        'shutdowns': [dict(id=i, **safe(p)) for i, _, event, p in rows if event == 'shutting down'],
        'limitations': ['Historical voice_ready conflates main and voice gateway readiness.', 'No historical confirmed outbound progress, UDP, DAVE state or administrative actor.', 'A local leave request does not prove remote membership removal or its initiator.'],
    }
    for item in result['explicit_leave_requests'] + result['shutdowns']:
        ns = next(ns for i, ns, _, _ in rows if i == item['id'])
        item['window_start_utc'] = datetime.datetime.fromtimestamp(ns/1e9-120, datetime.timezone.utc).isoformat()
        item['window_end_utc'] = datetime.datetime.fromtimestamp(ns/1e9+120, datetime.timezone.utc).isoformat()
        item['sqlite_timeline'] = [dict(id=i, **safe(p)) for i, t, _, p in rows if abs(t-ns) <= 120_000_000_000]
    output.write_text(json.dumps(result, ensure_ascii=False, indent=2) + '\n')

if __name__ == '__main__':
    main()
