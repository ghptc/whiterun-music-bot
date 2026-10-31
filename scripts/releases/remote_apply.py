"""Invoked only by the verified GitHub deployment entrypoint; never standalone."""
import datetime
import hashlib
import json
import os
import re
from pathlib import Path
import shutil
import sqlite3
import subprocess
import tarfile
import time


def execute(context):
    os.umask(0o077)
    service = Path('/opt/services/bardao')
    compose = service / 'compose.yaml'
    container = 'bardao-production-bard-1'
    base = ['docker', 'compose', '-p', 'bardao-production', '--project-directory', str(service), '-f', str(compose)]

    def out(args):
        return subprocess.check_output(args, text=True, timeout=120)

    def inspect():
        return json.loads(out(['docker', 'inspect', container]))[0]

    def digest(path):
        h = hashlib.sha256()
        with path.open('rb') as stream:
            for block in iter(lambda: stream.read(1024 * 1024), b''):
                h.update(block)
        return h.hexdigest()

    def tables(volume):
        db = sqlite3.connect((Path(volume) / 'bard.db').as_uri() + '?mode=ro', uri=True, timeout=5)
        db.execute('BEGIN')
        state = {}
        for name in ['guilds', 'music_catalog', 'tracks', 'history', 'playback_events', 'catalog_positions']:
            rows = db.execute('select * from ' + name + ' order by 1,2').fetchall()
            state[name] = hashlib.sha256(json.dumps(rows, default=str, separators=(',', ':')).encode()).hexdigest()
        db.rollback()
        db.close()
        return state

    def idle(volume):
        db = sqlite3.connect((Path(volume) / 'bard.db').as_uri() + '?mode=ro', uri=True, timeout=5)
        try:
            for (raw,) in db.execute('select state from guilds'):
                state = json.loads(raw)
                assert not state.get('Current') and not state.get('Queue'), 'busy playback: stop and seek specific authorization'
        finally:
            db.close()

    old = inspect()
    expected = context['expected_current']
    assert (old['Image'] == expected if context['allow_legacy'] else old['Config']['Image'] == expected), 'production changed'
    volume = next(m for m in old['Mounts'] if m['Destination'] == '/home/bard/data')
    assert volume['Name'] == 'bardao-production-data'
    assert shutil.disk_usage('/opt').free >= 2 * 1024**3, 'insufficient disk headroom'
    idle(volume['Source'])
    original = compose.read_text()
    original_config = json.loads(out(base + ['config', '--format', 'json']))
    assert set(original_config['services']) == {'bard'}
    env_hash = digest(service / '.env')
    others = {c['Id']: c['State']['StartedAt'] for c in json.loads(out(['docker', 'inspect'] + out(['docker', 'ps', '-q']).split())) if c['Id'] != old['Id']}
    backup = Path('/opt/backups/bardao') / ('verified-' + str(context['authorization_run']) + '-' + datetime.datetime.now(datetime.timezone.utc).strftime('%Y%m%dT%H%M%SZ'))
    backup.mkdir(parents=True, mode=0o700)
    (backup / 'authorization.json').write_text(json.dumps(context, indent=2) + '\n')
    shutil.copy2(compose, backup / 'compose.yaml')
    shutil.copy2(service / '.env', backup / '.env')
    (backup / 'previous-container.private.json').write_text(json.dumps(old))
    log = (backup / 'operations.private.log').open('ab')

    def run(args):
        subprocess.run(args, stdout=log, stderr=log, check=True, timeout=600)

    for name in ['bard.db', 'bard-logs.db']:
        source = sqlite3.connect((Path(volume['Source']) / name).as_uri() + '?mode=ro', uri=True, timeout=10)
        destination = sqlite3.connect(str(backup / name))
        source.backup(destination, pages=256, sleep=0.05)
        assert destination.execute('pragma quick_check').fetchone()[0] == 'ok'
        destination.close()
        source.close()
    # Preserve old bytes for evidence, even if legacy. Never use unverified bytes for rollback.
    run(['docker', 'save', '-o', str(backup / 'previous-image.tar'), old['Image']])
    for artifact in [context['release'], context['rollback']]:
        run(['docker', 'pull', artifact['image']])
        info = json.loads(out(['docker', 'image', 'inspect', artifact['image']]))[0]
        assert info['Config'].get('Labels', {}).get('org.opencontainers.image.revision') == artifact['commit']
    assert inspect()['Id'] == old['Id'] and digest(service / '.env') == env_hash
    idle(volume['Source'])
    stopped = False
    baseline = None

    def set_image(image):
        config = json.loads(json.dumps(original_config))
        config['services']['bard']['image'] = image
        # Keep raw variable references and credentials exactly where they were.
        pattern = r'(?m)^([ \t]*)image:[^\r\n]*$'
        assert len(re.findall(pattern, original)) == 1, 'ambiguous image reference'
        text = re.sub(pattern, lambda m: m.group(1) + 'image: ' + image, original)
        compose.write_text(text)
        assert json.loads(out(base + ['config', '--format', 'json'])) == config

    def healthy(image, seconds=90):
        deadline = time.monotonic() + seconds
        consecutive = 0
        while time.monotonic() < deadline:
            c = inspect()
            assert c['State']['Running'] and c['RestartCount'] == 0
            try:
                h = json.loads(out(['docker', 'exec', container, 'cat', '/tmp/bard-runtime-health.json']))
                ok = (c['Config']['Image'] == image and c['State'].get('Health', {}).get('Status') == 'healthy'
                      and h['process_alive'] and h['sqlite_accessible'] and h['session_recovery_available']
                      and h['runtime']['gateway_connected'] and h['runtime']['workers_responsive'])
            except subprocess.SubprocessError:
                ok = False
            consecutive = consecutive + 1 if ok else 0
            if consecutive >= 5:
                return c
            time.sleep(3)
        raise RuntimeError('operational readiness failed')

    try:
        run(base + ['stop', '-t', '30', 'bard'])
        stopped = True
        assert not inspect()['State']['Running']
        with tarfile.open(backup / 'volume-offline.tar.gz', 'w:gz') as archive:
            archive.add(volume['Source'], arcname='data')
        baseline = tables(volume['Source'])
        for name in ['bard.db', 'bard-logs.db']:
            db = sqlite3.connect((Path(volume['Source']) / name).as_uri() + '?mode=ro', uri=True)
            assert db.execute('pragma quick_check').fetchone()[0] == 'ok'
            db.close()
        (backup / 'backup-manifest.json').write_text(json.dumps({
            'volume_sha256': digest(backup / 'volume-offline.tar.gz'),
            'baseline_tables': baseline, 'config_sha256': digest(backup / 'compose.yaml'),
            'env_sha256': env_hash}, indent=2))
        set_image(context['release']['image'])
        run(base + ['up', '-d', '--no-deps', '--no-build', '--pull', 'never', 'bard'])
        new = healthy(context['release']['image'])
        assert new['Mounts'] == old['Mounts']
        for field in ['RestartPolicy', 'Memory', 'NanoCpus', 'ReadonlyRootfs', 'CapDrop', 'SecurityOpt', 'Init', 'Tmpfs']:
            assert new['HostConfig'].get(field) == old['HostConfig'].get(field), field
        for field in ['Healthcheck', 'StopTimeout']:
            assert new['Config'].get(field) == old['Config'].get(field), field
        assert tables(volume['Source']) == baseline and digest(service / '.env') == env_hash
        for ident, started in others.items():
            assert json.loads(out(['docker', 'inspect', ident]))[0]['State']['StartedAt'] == started
        result = {'status': 'verified-release-deployed', 'release': context['release'], 'backup': str(backup), 'rollback_performed': False}
    except Exception:
        if stopped:
            set_image(context['rollback']['image'])
            run(base + ['up', '-d', '--no-deps', '--no-build', '--pull', 'never', 'bard'])
            healthy(context['rollback']['image'])
            assert baseline is None or tables(volume['Source']) == baseline
            (backup / 'result.json').write_text(json.dumps({'status': 'rolled-back', 'image': context['rollback']['image']}))
        raise
    finally:
        log.close()
    (backup / 'result.json').write_text(json.dumps(result, indent=2) + '\n')
    print(json.dumps({'status': result['status'], 'image': context['release']['image'], 'backup': str(backup)}))


if __name__ == '__main__':
    if 'CONTEXT' not in globals():
        raise SystemExit('DENIED: use the verified GitHub deployment entrypoint')
    execute(CONTEXT)
