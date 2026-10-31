#!/usr/bin/env python3
"""REL-028 trust gate. No offline/provenance-bypass mode exists."""
import argparse
import hashlib
import io
import json
import os
from pathlib import Path
import re
import subprocess
import tempfile
import time
import zipfile

REPO = 'ghptc/whiterun-music-bot'
IMAGE = 'ghcr.io/' + REPO
CI = '.github/workflows/release-ci.yml'
RELEASE = '.github/workflows/release.yml'
DEPLOY = '.github/workflows/deploy-production.yml'
CHECKS = ('release-unit', 'release-race', 'release-contracts', 'release-security', 'release-docker', 'release-ci')
REQUIRED_CHECKS = CHECKS + ('deterministic-ttfa',)
APP = 15368  # GitHub Actions; required-check source is bound, not any status writer.
SHA = re.compile(r'[0-9a-f]{40}')
DIGEST = re.compile(r'sha256:[0-9a-f]{64}')


class Denied(RuntimeError):
    pass


def require(condition, reason):
    if not condition:
        raise Denied(reason)


def command(argv, **kwargs):
    result = subprocess.run(argv, capture_output=True, timeout=180, **kwargs)
    if result.returncode != 0:
        if argv[:2] == ['gh', 'api']:
            # Report only fixed endpoint/status fields; never raw stderr, headers,
            # response bodies, tokens or command arguments from other operations.
            stderr = result.stderr.decode(errors='replace') if isinstance(result.stderr, bytes) else (result.stderr or '')
            match = re.search(r'HTTP (\d{3})', stderr)
            status = match.group(1) if match else 'unknown'
            category = {'401': 'authentication', '403': 'permission_or_rate_limit',
                        '404': 'missing_or_inaccessible', '429': 'rate_limit'}.get(status, 'api_or_transport')
            endpoint = argv[2].split('?', 1)[0]
            safe = endpoint if re.fullmatch(r'[A-Za-z0-9_./-]+', endpoint) else '[redacted]'
            raise Denied(f'GitHub API GET {safe}: exit={result.returncode} http={status} category={category}')
        raise Denied('external verification failed: ' + argv[0])
    return result.stdout


class GitHub:
    def get(self, path):
        return json.loads(command(['gh', 'api', 'repos/' + REPO + ('/' + path if path else '')]))

    def pages(self, path, key):
        rows = []
        separator = '&' if '?' in path else '?'
        for page in range(1, 21):
            data = self.get(path + separator + f'per_page=100&page={page}')
            batch = data[key]
            rows.extend(batch)
            if len(batch) < 100:
                return rows
        raise Denied('pagination limit: cannot establish complete evidence')

    def archive(self, artifact_id):
        return command(['gh', 'api', f'repos/{REPO}/actions/artifacts/{artifact_id}/zip'])


def verified_main(api, sha):
    require(SHA.fullmatch(sha or ''), 'exact commit SHA required')
    branch = api.get('branches/main')
    require(branch.get('protected') is True, 'main is not protected')
    rules = api.get('rules/branches/main')
    bound = set()
    pr_required = False
    for rule in rules:
        p = rule.get('parameters', {})
        if rule['type'] == 'required_status_checks' and p.get('strict_required_status_checks_policy'):
            bound.update(c['context'] for c in p['required_status_checks'] if c.get('integration_id') == APP)
        if rule['type'] == 'pull_request':
            pr_required = True
    require(set(REQUIRED_CHECKS) <= bound and pr_required, 'mandatory protection/checks/PR missing')
    # A dedicated, active, non-bypassable ruleset must actually apply to main.
    ids = {r.get('ruleset_id') for r in rules if r.get('ruleset_id')}
    trusted = False
    for rule_id in ids:
        r = api.get('rulesets/' + str(rule_id))
        if r.get('name') != 'REL-028 verified releases':
            continue
        dedicated = r.get('rules', [])
        mandatory = any(x['type'] == 'required_status_checks'
                        and x.get('parameters', {}).get('strict_required_status_checks_policy')
                        and set(REQUIRED_CHECKS) <= {c['context'] for c in x['parameters']['required_status_checks']
                                           if c.get('integration_id') == APP} for x in dedicated)
        # Owner-authorized solo-maintainer policy: merge is the owner's approval.
        # PRs and all exact-SHA checks stay mandatory; deployment retains its
        # separate production-environment approval.
        pull_request = any(x['type'] == 'pull_request' for x in dedicated)
        trusted = (mandatory and pull_request and r.get('enforcement') == 'active' and not r.get('bypass_actors')
                   and r.get('conditions', {}).get('ref_name') == {'include': ['refs/heads/main'], 'exclude': []})
    require(trusted, 'non-bypassable release ruleset missing')
    compare = api.get(f'compare/{sha}...main')
    require(compare.get('status') in ('ahead', 'identical') and compare.get('behind_by') == 0,
            'commit is not reachable from origin/main')
    return branch['commit']['sha']


def workflow_run(api, run_id, path, sha, events, completed=True):
    run = api.get('actions/runs/' + str(int(run_id)))
    workflow = api.get('actions/workflows/' + Path(path).name)
    repo_id = api.get('')['id']
    require(run.get('head_repository', {}).get('id') == repo_id, 'foreign repository run')
    require(run.get('workflow_id') == workflow['id'] and run.get('path') == path, 'untrusted workflow')
    require(run.get('head_branch') == 'main' and run.get('head_sha') == sha, 'wrong source branch/SHA')
    require(run.get('event') in events, 'unexpected workflow event')
    if completed:
        require(run.get('status') == 'completed' and run.get('conclusion') == 'success',
                'workflow pending, skipped, cancelled or unsuccessful')
    return run


def ci_evidence(api, sha, run_id=None):
    verified_main(api, sha)
    runs = api.pages('actions/workflows/release-ci.yml/runs?event=push&head_sha=' + sha, 'workflow_runs')
    require(runs, 'no protected-main CI for exact SHA')
    runs = [r for r in runs if r.get('head_branch') == 'main' and r.get('head_sha') == sha]
    require(runs, 'no protected-main CI for exact SHA')
    latest = max(runs, key=lambda r: int(r['id']))
    require(run_id is None or int(run_id) == latest['id'], 'stale CI run')
    run = workflow_run(api, latest['id'], CI, sha, {'push'})
    jobs = api.pages(f"actions/runs/{run['id']}/attempts/{run['run_attempt']}/jobs", 'jobs')
    records = api.pages(f'commits/{sha}/check-runs?filter=all', 'check_runs')
    # Same-SHA tag runs must not replace evidence from the selected main CI.
    records = [c for c in records if c['name'] not in CHECKS
               or not c.get('details_url')
               or c['details_url'].startswith(f"https://github.com/{REPO}/actions/runs/{run['id']}/job/")]
    proof = []
    for name in CHECKS:
        matches = [j for j in jobs if j['name'] == name]
        require(len(matches) == 1, 'missing/ambiguous mandatory CI job: ' + name)
        job = matches[0]
        require(job['status'] == 'completed' and job['conclusion'] == 'success', 'CI job did not succeed: ' + name)
        require('self-hosted' not in job.get('labels', []), 'untrusted CI runner')
        checks = [c for c in records if c['name'] == name and c.get('app', {}).get('id') == APP]
        require(checks, 'missing exact-SHA check: ' + name)
        check = max(checks, key=lambda c: c['id'])
        require(check['head_sha'] == sha and check['status'] == 'completed' and check['conclusion'] == 'success',
                'required check not successful for exact SHA: ' + name)
        require(job['check_run_url'].endswith('/' + str(check['id'])), 'check is not from trusted CI run')
        proof.append({'name': name, 'check_id': check['id'], 'job_id': job['id'], 'conclusion': 'success'})
    # Additional required contexts must also succeed, including future protections.
    for rule in api.get('rules/branches/main'):
        if rule['type'] != 'required_status_checks':
            continue
        for required in rule['parameters']['required_status_checks']:
            candidates = [c for c in records if c['name'] == required['context']
                          and c.get('app', {}).get('id') == required.get('integration_id')]
            require(candidates, 'missing additional required check')
            check = max(candidates, key=lambda c: c['id'])
            require(check['head_sha'] == sha and check['status'] == 'completed' and check['conclusion'] == 'success',
                    'additional required check not successful')
            if required['context'] not in {c['name'] for c in proof}:
                proof.append({'name': check['name'], 'check_id': check['id'], 'conclusion': 'success'})
    return {'run_id': run['id'], 'attempt': run['run_attempt'], 'checks': proof}


def image_reference(value):
    prefix = IMAGE + '@'
    require(value.startswith(prefix) and DIGEST.fullmatch(value[len(prefix):]), 'immutable approved GHCR digest required')
    return value[len(prefix):]


def verified_checkout(source, sha):
    require(command(['git', '-C', source, 'rev-parse', 'HEAD']).decode().strip() == sha, 'checkout SHA mismatch')
    require(not command(['git', '-C', source, 'status', '--porcelain', '--untracked-files=all']).strip(), 'dirty checkout')


def attest(subject, sha, run_id, attempt):
    raw = command(['gh', 'attestation', 'verify', subject, '--repo', REPO,
                   '--signer-workflow', REPO + '/' + RELEASE, '--signer-digest', sha,
                   '--source-digest', sha, '--source-ref', 'refs/heads/main',
                   '--deny-self-hosted-runners', '--format', 'json'])
    verified = json.loads(raw)
    expected = f'https://github.com/{REPO}/actions/runs/{run_id}/attempts/{attempt}'
    for item in verified:
        statement = item.get('verificationResult', {}).get('statement', {})
        invocation = statement.get('predicate', {}).get('runDetails', {}).get('metadata', {}).get('invocationId')
        if invocation == expected and statement.get('predicateType') == 'https://slsa.dev/provenance/v1':
            return
    raise Denied('attestation is missing or belongs to another workflow run/attempt')


def release_manifest(api, run_id, attestor=attest):
    run = api.get('actions/runs/' + str(int(run_id)))
    sha = run['head_sha']
    verified_main(api, sha)
    run = workflow_run(api, run_id, RELEASE, sha, {'workflow_run', 'workflow_dispatch'})
    name = f"release-manifest-{run['id']}-{run['run_attempt']}"
    artifacts = [a for a in api.pages(f"actions/runs/{run['id']}/artifacts", 'artifacts')
                 if a['name'] == name and not a['expired']]
    require(len(artifacts) == 1 and artifacts[0]['size_in_bytes'] < 1024 * 1024, 'missing/ambiguous release manifest')
    raw = api.archive(artifacts[0]['id'])
    with zipfile.ZipFile(io.BytesIO(raw)) as archive:
        require(archive.namelist() == ['release-manifest.json'], 'unexpected manifest archive')
        info = archive.getinfo('release-manifest.json')
        require(info.file_size < 128 * 1024, 'oversized manifest')
        body = archive.read(info)
    with tempfile.TemporaryDirectory(prefix='bard-release-') as tmp:
        path = Path(tmp) / 'release-manifest.json'
        path.write_bytes(body)
        attestor(str(path), sha, run['id'], run['run_attempt'])
    m = json.loads(body)
    require(m.get('schema') == 1 and m.get('repository') == REPO and m.get('source_ref') == 'refs/heads/main', 'manifest identity mismatch')
    require(m.get('commit') == sha, 'manifest SHA mismatch')
    require(m.get('release') == {'run_id': run['id'], 'attempt': run['run_attempt'], 'workflow': RELEASE}, 'manifest run mismatch')
    image_reference(m.get('image', ''))
    fresh = ci_evidence(api, sha, m.get('ci', {}).get('run_id'))
    require(m.get('ci') == fresh, 'tampered/stale checks metadata')
    attestor('oci://' + m['image'], sha, run['id'], run['run_attempt'])
    return m


def production_approval(api, run_id, release_id, rollback_id, expected_current, allow_legacy, sha):
    verified_main(api, sha)
    run = workflow_run(api, run_id, DEPLOY, sha, {'workflow_dispatch'}, completed=False)
    require(run['status'] == 'in_progress' and run['run_attempt'] == 1,
            'deployment requires a fresh active run and fresh environment approval')
    title = f'Deploy production release {release_id} rollback {rollback_id} current {expected_current} legacy {str(allow_legacy).lower()}'
    require(run['display_title'] == title, 'approval does not cover these deployment inputs')
    env = api.get('environments/production')
    require(env.get('deployment_branch_policy') == {'protected_branches': True, 'custom_branch_policies': False}, 'production branch policy missing')
    reviewers = {r['reviewer']['id'] for rule in env.get('protection_rules', [])
                 if rule['type'] == 'required_reviewers' for r in rule['reviewers'] if r['type'] == 'User'}
    require(reviewers and env.get('can_admins_bypass') is False, 'production review gate missing/bypassable')
    reviews = api.get(f'actions/runs/{run_id}/approvals')
    relevant = [r for r in reviews if any(e['name'] == 'production' for e in r['environments'])]
    require(relevant and relevant[-1]['state'] == 'approved' and relevant[-1]['user']['id'] in reviewers,
            'production environment has not approved this run')


def wait_required_checks(api, sha, timeout=900, interval=10, clock=time.monotonic, sleep=time.sleep):
    require(0 <= timeout <= 900 and 0 < interval <= 30, 'invalid check wait bounds')
    deadline = clock() + timeout
    while True:
        require(verified_main(api, sha) == sha, 'only current protected main may publish')
        rules = api.get('rules/branches/main')
        required = {(c['context'], c.get('integration_id')) for r in rules
                    if r['type'] == 'required_status_checks'
                    for c in r['parameters']['required_status_checks']}
        records = api.pages(f'commits/{sha}/check-runs?filter=all', 'check_runs')
        runs = api.pages('actions/workflows/release-ci.yml/runs?event=push&head_sha=' + sha, 'workflow_runs')
        runs = [r for r in runs if r.get('head_branch') == 'main' and r.get('head_sha') == sha]
        pending = not runs
        if runs:
            ci = workflow_run(api, max(runs, key=lambda r: int(r['id']))['id'], CI, sha, {'push'}, completed=False)
            if ci['status'] == 'completed':
                require(ci.get('conclusion') == 'success', 'terminal main CI failure')
            else:
                require(ci['status'] in ('queued', 'in_progress', 'waiting', 'pending', 'requested'), 'unknown CI state')
                pending = True
        for name, app in required:
            candidates = [c for c in records if c['name'] == name and c.get('app', {}).get('id') == app]
            if not candidates:
                pending = True
                continue
            check = max(candidates, key=lambda c: c['id'])
            require(check.get('head_sha') == sha, 'wrong check SHA')
            if check['status'] == 'completed':
                require(check.get('conclusion') == 'success', 'terminal required check: ' + name)
            else:
                require(check['status'] in ('queued', 'in_progress', 'waiting', 'pending', 'requested'), 'unknown check state')
                pending = True
        if not pending:
            return
        require(clock() < deadline, 'required checks timed out')
        sleep(min(interval, max(0, deadline - clock())))


def publish(args):
    require(os.environ.get('GITHUB_ACTIONS') == 'true' and os.environ.get('GITHUB_REF') == 'refs/heads/main', 'GitHub protected-main build required')
    require(os.environ.get('GITHUB_SHA') == args.sha, 'workflow/source SHA mismatch')
    api = GitHub()
    require(verified_main(api, args.sha) == args.sha, 'only current protected main may publish')
    if args.phase == 'check':
        wait_required_checks(api, args.sha)
    proof = ci_evidence(api, args.sha, args.ci_run)
    verified_checkout(args.source, args.sha)
    if args.phase == 'check':
        print(json.dumps(proof))
        return
    require(DIGEST.fullmatch(args.digest or ''), 'build digest missing')
    manifest = {'schema': 1, 'repository': REPO, 'source_ref': 'refs/heads/main',
                'commit': args.sha, 'image': IMAGE + '@' + args.digest,
                'release': {'run_id': int(os.environ['GITHUB_RUN_ID']),
                            'attempt': int(os.environ['GITHUB_RUN_ATTEMPT']), 'workflow': RELEASE}, 'ci': proof}
    Path(args.out).write_text(json.dumps(manifest, sort_keys=True, indent=2) + '\n')


def main():
    p = argparse.ArgumentParser()
    sub = p.add_subparsers(dest='operation', required=True)
    pub = sub.add_parser('publish')
    pub.add_argument('--sha', required=True)
    pub.add_argument('--ci-run', type=int)
    pub.add_argument('--phase', choices=['check', 'manifest'], required=True)
    pub.add_argument('--source', default='source')
    pub.add_argument('--digest')
    pub.add_argument('--out', default='release-manifest.json')
    verify = sub.add_parser('verify')
    verify.add_argument('--release-run', required=True, type=int)
    args = p.parse_args()
    try:
        if args.operation == 'publish':
            publish(args)
        else:
            print(json.dumps(release_manifest(GitHub(), args.release_run), sort_keys=True))
    except (Denied, OSError, ValueError, KeyError, subprocess.SubprocessError, zipfile.BadZipFile) as error:
        print('RELEASE DENIED: ' + str(error), file=__import__('sys').stderr)
        return 1
    return 0


if __name__ == '__main__':
    raise SystemExit(main())
