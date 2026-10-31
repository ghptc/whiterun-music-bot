import copy
import io
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch
import zipfile

import verified_release as v
import deploy_production as deploy

A = 'a' * 40
D = 'sha256:' + 'b' * 64
IMAGE = v.IMAGE + '@' + D


class API:
    def __init__(self):
        self.sha = A
        self.branch = {'protected': True, 'commit': {'sha': A}}
        self.rules = [
            {'type': 'required_status_checks', 'ruleset_id': 1, 'parameters': {
                'strict_required_status_checks_policy': True,
                'required_status_checks': [{'context': name, 'integration_id': v.APP} for name in v.CHECKS]}},
            {'type': 'pull_request', 'ruleset_id': 1, 'parameters': {'required_approving_review_count': 1}},
        ]
        self.rule = {'name': 'REL-028 verified releases', 'enforcement': 'active', 'bypass_actors': [],
                     'conditions': {'ref_name': {'include': ['refs/heads/main'], 'exclude': []}}, 'rules': self.rules}
        self.compare = {'status': 'identical', 'behind_by': 0}
        self.ci = self.run(10, v.CI, 'push', 101)
        self.release = self.run(20, v.RELEASE, 'workflow_run', 102)
        self.deployment = self.run(30, v.DEPLOY, 'workflow_dispatch', 103)
        self.deployment.update(status='in_progress', conclusion=None,
                               display_title=f'Deploy production release 20 rollback 19 current {IMAGE} legacy false')
        self.jobs = [{'id': i, 'name': name, 'status': 'completed', 'conclusion': 'success',
                      'labels': ['ubuntu-latest'], 'check_run_url': f'https://api.github.com/check-runs/{100+i}'}
                     for i, name in enumerate(v.CHECKS, 1)]
        self.checks = [{'id': 100+i, 'head_sha': A, 'name': name, 'status': 'completed',
                        'conclusion': 'success', 'app': {'id': v.APP}} for i, name in enumerate(v.CHECKS, 1)]
        self.environment = {'can_admins_bypass': False,
                            'deployment_branch_policy': {'protected_branches': True, 'custom_branch_policies': False},
                            'protection_rules': [{'type': 'required_reviewers', 'reviewers': [{'type': 'User', 'reviewer': {'id': 9}}]}]}
        self.approvals = [{'state': 'approved', 'environments': [{'name': 'production'}], 'user': {'id': 9}}]
        self.manifest = {'schema': 1, 'repository': v.REPO, 'source_ref': 'refs/heads/main', 'commit': A,
                         'image': IMAGE, 'release': {'run_id': 20, 'attempt': 1, 'workflow': v.RELEASE}}
        self.artifacts = [{'id': 40, 'name': 'release-manifest-20-1', 'expired': False, 'size_in_bytes': 1000}]
        self.zip_name = 'release-manifest.json'
        self.manifest['ci'] = v.ci_evidence(self, A)
        self.attested_body = json.dumps(self.manifest).encode()

    @staticmethod
    def run(number, path, event, workflow_id):
        return {'id': number, 'head_sha': A, 'head_branch': 'main', 'head_repository': {'id': 99},
                'path': path, 'workflow_id': workflow_id, 'event': event, 'status': 'completed',
                'conclusion': 'success', 'run_attempt': 1}

    def get(self, path):
        fixed = {'': {'id': 99}, 'branches/main': self.branch, 'rules/branches/main': self.rules,
                 'rulesets/1': self.rule, f'compare/{A}...main': self.compare,
                 'actions/runs/10': self.ci, 'actions/runs/20': self.release, 'actions/runs/30': self.deployment,
                 'actions/workflows/release-ci.yml': {'id': 101}, 'actions/workflows/release.yml': {'id': 102},
                 'actions/workflows/deploy-production.yml': {'id': 103},
                 'environments/production': self.environment, 'actions/runs/30/approvals': self.approvals}
        return copy.deepcopy(fixed[path])

    def pages(self, path, key):
        if key == 'workflow_runs':
            return [self.ci]
        if key == 'jobs':
            return self.jobs
        if key == 'check_runs':
            return self.checks
        if key == 'artifacts':
            return self.artifacts
        raise AssertionError(path)

    def archive(self, artifact_id):
        stream = io.BytesIO()
        with zipfile.ZipFile(stream, 'w') as archive:
            archive.writestr(self.zip_name, json.dumps(self.manifest))
        return stream.getvalue()

    def attest(self, subject, sha, run, attempt):
        v.require((sha, run, attempt) == (A, 20, 1), 'attestation binding mismatch')
        if subject.startswith('oci://'):
            v.require(subject == 'oci://' + IMAGE, 'image digest lacks attestation')
        else:
            v.require(Path(subject).read_bytes() == self.attested_body, 'tampered signed manifest')


class ReleaseTests(unittest.TestCase):
    def setUp(self):
        self.api = API()

    def verify(self):
        return v.release_manifest(self.api, 20, self.api.attest)

    def test_valid_verified_release(self):
        self.assertEqual(self.verify()['image'], IMAGE)

    def test_negative_release_matrix(self):
        cases = {
            'feature branch': lambda a: a.release.update(head_branch='feature'),
            'unmerged commit': lambda a: a.compare.update(status='diverged', behind_by=1),
            'unprotected main': lambda a: a.branch.update(protected=False),
            'protection bypass': lambda a: a.rule.update(bypass_actors=[{'actor_id': 1}]),
            'disabled ruleset': lambda a: a.rule.update(enforcement='disabled'),
            'missing required checks': lambda a: a.rules[0]['parameters'].update(required_status_checks=[]),
            'CI failed': lambda a: a.ci.update(conclusion='failure'),
            'CI pending': lambda a: a.ci.update(status='in_progress', conclusion=None),
            'CI cancelled': lambda a: a.ci.update(conclusion='cancelled'),
            'CI skipped': lambda a: a.ci.update(conclusion='skipped'),
            'job skipped': lambda a: a.jobs[0].update(conclusion='skipped'),
            'wrong CI SHA': lambda a: a.ci.update(head_sha='c'*40),
            'wrong check SHA': lambda a: a.checks[0].update(head_sha='c'*40),
            'wrong check source': lambda a: a.checks[0].update(app={'id': 1}),
            'spoofed check name': lambda a: a.jobs[0].update(check_run_url='https://example/check-runs/999'),
            'self hosted': lambda a: a.jobs[0].update(labels=['self-hosted']),
            'wrong source SHA': lambda a: a.manifest.update(commit='c'*40),
            'wrong image digest': lambda a: a.manifest.update(image=v.IMAGE+'@sha256:'+'c'*64),
            'missing manifest': lambda a: a.artifacts.clear(),
            'expired manifest': lambda a: a.artifacts[0].update(expired=True),
            'tampered metadata': lambda a: a.manifest['ci'].update(attempt=99),
            'mutable latest': lambda a: a.manifest.update(image=v.IMAGE+':latest'),
            'local Docker image': lambda a: a.manifest.update(image='bard:local'),
            'untrusted workflow': lambda a: a.release.update(path='.github/workflows/feature.yml'),
            'foreign repository': lambda a: a.release.update(head_repository={'id': 100}),
            'PR CI is not main CI': lambda a: a.ci.update(event='pull_request'),
            'zip traversal': lambda a: setattr(a, 'zip_name', '../release-manifest.json'),
        }
        for name, mutate in cases.items():
            with self.subTest(name=name):
                api = API()
                mutate(api)
                with self.assertRaises(v.Denied):
                    v.release_manifest(api, 20, api.attest)

    def test_latest_failed_status_cannot_reuse_old_success(self):
        newer = dict(self.api.checks[0], id=999, conclusion='failure')
        self.api.checks.append(newer)
        with self.assertRaises(v.Denied):
            self.verify()

    def test_wrong_digest_even_with_signed_manifest(self):
        self.api.manifest['image'] = v.IMAGE + '@sha256:' + 'c'*64
        self.api.attested_body = json.dumps(self.api.manifest).encode()
        with self.assertRaisesRegex(v.Denied, 'image digest'):
            self.verify()

    def test_unsigned_image_never_passes(self):
        with self.assertRaises(v.Denied):
            v.release_manifest(self.api, 20, lambda *a: v.require(False, 'no cryptographic provenance'))

    def test_attestation_flags_and_run_binding(self):
        result = [{'verificationResult': {'statement': {'predicateType': 'https://slsa.dev/provenance/v1',
                   'predicate': {'runDetails': {'metadata': {'invocationId': f'https://github.com/{v.REPO}/actions/runs/20/attempts/1'}}}}}}]
        with patch.object(v, 'command', return_value=json.dumps(result).encode()) as cmd:
            v.attest('oci://'+IMAGE, A, 20, 1)
            argv = cmd.call_args.args[0]
            for flag in ['--source-digest', '--source-ref', '--signer-digest', '--signer-workflow', '--deny-self-hosted-runners']:
                self.assertIn(flag, argv)
            with self.assertRaises(v.Denied):
                v.attest('oci://'+IMAGE, A, 21, 1)

    def test_valid_deployment_authorization(self):
        def loader(api, number):
            v.require(number in (20, 19), 'unknown release')
            return {'image': IMAGE}
        release, rollback = deploy.authorize(self.api, 20, 19, IMAGE, False, 30, A, loader)
        self.assertEqual(release, rollback)

    def test_deployment_requires_exact_fresh_approval(self):
        for mutate in [lambda a: a.approvals.clear(), lambda a: a.approvals[0].update(state='rejected'),
                       lambda a: a.environment.update(can_admins_bypass=True),
                       lambda a: a.deployment.update(run_attempt=2),
                       lambda a: a.deployment.update(display_title='approved different release')]:
            with self.subTest(mutate=mutate):
                api = API(); mutate(api)
                with self.assertRaises(v.Denied):
                    deploy.authorize(api, 20, 19, IMAGE, False, 30, A, lambda *a: {'image': IMAGE})

    def test_legacy_requires_explicit_migration_and_verified_rollback(self):
        with self.assertRaises(v.Denied):
            deploy.authorize(self.api, 20, 19, 'sha256:'+'c'*64, False, 30, A, lambda *a: {'image': IMAGE})
        with self.assertRaises(v.Denied):
            deploy.authorize(self.api, 20, 20, IMAGE, False, 30, A, lambda *a: {'image': IMAGE})

    def test_unverified_artifact_never_reaches_remote_apply(self):
        with patch.dict('os.environ', {'GITHUB_ACTIONS': 'true', 'GITHUB_REF': 'refs/heads/main', 'GITHUB_RUN_ID': '30', 'GITHUB_SHA': A}), \
             patch('sys.argv', ['deploy', '--release-run', '20', '--rollback-run', '19', '--expected-current', IMAGE]), \
             patch.object(deploy, 'authorize', side_effect=v.Denied('unverified')), \
             patch.object(deploy.subprocess, 'run') as remote:
            self.assertEqual(deploy.main(), 1)
            remote.assert_not_called()

    def test_publication_rejects_local_and_feature_context(self):
        args = type('Args', (), {'sha': A})()
        for env in [{}, {'GITHUB_ACTIONS': 'true', 'GITHUB_REF': 'refs/heads/feature'}]:
            with patch.dict('os.environ', env, clear=True), self.assertRaises(v.Denied):
                v.publish(args)


if __name__ == '__main__':
    unittest.main()
