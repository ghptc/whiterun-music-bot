#!/usr/bin/env python3
"""Sole supported production entrypoint. Verify before any SSH/mutation."""
import argparse
import json
import os
from pathlib import Path
import re
import subprocess
import sys

from verified_release import (Denied, GitHub, command, image_reference,
                              production_approval, release_manifest, require, verified_checkout)


def authorize(api, release_id, rollback_id, expected_current, allow_legacy, run_id, workflow_sha,
              loader=release_manifest):
    release = loader(api, release_id)
    rollback = loader(api, rollback_id)
    require(release_id != rollback_id, 'separate previously verified rollback release required')
    if expected_current.startswith('sha256:'):
        require(allow_legacy and re.fullmatch(r'sha256:[0-9a-f]{64}', expected_current), 'legacy migration not explicitly authorized')
    else:
        image_reference(expected_current)
        require(not allow_legacy and expected_current == rollback['image'], 'current release must equal verified rollback')
    production_approval(api, run_id, release_id, rollback_id, expected_current, allow_legacy, workflow_sha)
    return release, rollback


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--release-run', type=int, required=True)
    parser.add_argument('--rollback-run', type=int, required=True)
    parser.add_argument('--expected-current', required=True)
    parser.add_argument('--allow-legacy-current', action='store_true')
    args = parser.parse_args()
    try:
        require(os.environ.get('GITHUB_ACTIONS') == 'true' and os.environ.get('GITHUB_REF') == 'refs/heads/main',
                'deployment only from approved GitHub main workflow')
        release, rollback = authorize(GitHub(), args.release_run, args.rollback_run, args.expected_current,
                                      args.allow_legacy_current, int(os.environ['GITHUB_RUN_ID']), os.environ['GITHUB_SHA'])
        verified_checkout('.', os.environ['GITHUB_SHA'])
        # Labels are checked only AFTER cryptographic image/manifest verification.
        for artifact in [release, rollback]:
            command(['docker', 'pull', artifact['image']])
            labels = json.loads(command(['docker', 'image', 'inspect', artifact['image'], '--format', '{{json .Config.Labels}}']))
            require(labels.get('org.opencontainers.image.revision') == artifact['commit'], 'image revision label mismatch')
        host, user = os.environ['PRODUCTION_HOST'], os.environ['PRODUCTION_USER']
        require(re.fullmatch(r'[A-Za-z0-9.-]+', host) and re.fullmatch(r'[a-z_][a-z0-9_-]*', user), 'invalid SSH destination')
        context = {'release': release, 'rollback': rollback, 'expected_current': args.expected_current,
                   'allow_legacy': args.allow_legacy_current, 'authorization_run': int(os.environ['GITHUB_RUN_ID'])}
        template = Path(__file__).with_name('remote_apply.py').read_text()
        script = 'import json\nCONTEXT = json.loads(' + repr(json.dumps(context)) + ')\n' + template
        # Strict host verification; no tokens or environment files are sent over SSH.
        result = subprocess.run(['ssh', '-o', 'BatchMode=yes', '-o', 'StrictHostKeyChecking=yes',
                                 user + '@' + host, 'sudo', 'python3', '-'], input=script.encode(), check=True)
        return result.returncode
    except (Denied, KeyError, ValueError, OSError, subprocess.SubprocessError) as error:
        print('DEPLOYMENT DENIED: ' + str(error), file=sys.stderr)
        return 1


if __name__ == '__main__':
    raise SystemExit(main())
