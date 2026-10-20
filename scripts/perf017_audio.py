#!/usr/bin/env python3
"""Local PERF-017 reproduction: real FFmpeg, synthetic source, no Discord/network."""
import json
import resource
import statistics
import subprocess
import time

OUTPUT = ['-ac', '2', '-ar', '48000', '-c:a', 'libopus', '-b:a', '96k',
          '-vbr', 'off', '-application', 'audio', '-frame_duration', '20',
          '-f', 'opus', '-flush_packets', '1']

for page in [[], ['-page_duration', '20000']]:
    samples = []
    for _ in range(8):
        args = ['ffmpeg', '-hide_banner', '-loglevel', 'error', '-re', '-f',
                'lavfi', '-i', 'sine=frequency=440:sample_rate=48000', '-t', '2']
        started = time.monotonic()
        process = subprocess.Popen(args + OUTPUT + page + ['pipe:1'], stdout=subprocess.PIPE)
        try:
            while True:
                header = process.stdout.read(27)
                if len(header) != 27 or header[:4] != b'OggS':
                    raise RuntimeError('missing Ogg page')
                sizes = process.stdout.read(header[26])
                data = process.stdout.read(sum(sizes))
                if not data.startswith((b'OpusHead', b'OpusTags')):
                    break
            samples.append(round((time.monotonic() - started) * 1000, 3))
        finally:
            process.terminate()
            process.communicate()
    # Encode the same ten seconds without pacing to measure CPU/container cost.
    before = resource.getrusage(resource.RUSAGE_CHILDREN)
    result = subprocess.run(['ffmpeg', '-hide_banner', '-loglevel', 'error', '-f',
                             'lavfi', '-i', 'sine=frequency=440:sample_rate=48000',
                             '-t', '10'] + OUTPUT + page + ['pipe:1'],
                            stdout=subprocess.PIPE, check=True)
    after = resource.getrusage(resource.RUSAGE_CHILDREN)
    print(json.dumps({'page_duration_us': 20000 if page else 1000000,
                      'first_audio_packet_ms': samples,
                      'median_ms': statistics.median(samples),
                      'ten_second_payload_bytes': len(result.stdout),
                      'ten_second_cpu_ms': round(1000 * (after.ru_utime + after.ru_stime
                                                          - before.ru_utime - before.ru_stime), 3),
                      'children_peak_rss_kib': after.ru_maxrss}), flush=True)
