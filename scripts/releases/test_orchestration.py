import unittest
from unittest.mock import patch
import verified_release as v
from test_verified_release import API, A


class OrchestrationTests(unittest.TestCase):
    def test_newer_tag_ci_does_not_replace_main_ci(self):
        api = API()
        original = api.pages
        def pages(path, key):
            rows = original(path, key)
            if key == 'workflow_runs':
                return rows + [dict(api.ci, id=99, head_branch='v1.0.0')]
            return rows
        api.pages = pages
        self.assertEqual(v.ci_evidence(api, A)['run_id'], 10)

    def test_pending_becomes_success(self):
        api = API()
        api.checks[-1].update(status='in_progress', conclusion=None)
        def finish(_):
            api.checks[-1].update(status='completed', conclusion='success')
        v.wait_required_checks(api, A, sleep=finish)

    def test_terminal_failures_do_not_wait(self):
        for conclusion in ['failure', 'cancelled', 'skipped', 'timed_out', 'neutral']:
            api = API()
            api.checks[-1].update(conclusion=conclusion)
            with patch.object(v.time, 'sleep') as sleep:
                with self.assertRaises(v.Denied):
                    v.wait_required_checks(api, A, sleep=sleep)
                sleep.assert_not_called()

    def test_pending_times_out(self):
        api = API()
        api.checks[-1].update(status='queued', conclusion=None)
        with self.assertRaisesRegex(v.Denied, 'timed out'):
            v.wait_required_checks(api, A, timeout=0)

    def test_wrong_sha_is_terminal(self):
        api = API()
        api.checks[-1]['head_sha'] = 'c' * 40
        with self.assertRaisesRegex(v.Denied, 'wrong check SHA'):
            v.wait_required_checks(api, A)

    def test_main_advance_is_terminal(self):
        api = API()
        api.branch['commit']['sha'] = 'c' * 40
        with self.assertRaises(v.Denied):
            v.wait_required_checks(api, A)

    def test_real_sanitized_dispatch_and_tag_selection(self):
        import json
        from pathlib import Path
        root = Path(__file__).with_name('testdata')
        manual = json.loads((root / 'manual-release-event.json').read_text())
        runs = json.loads((root / 'main-tag-events.json').read_text())
        self.assertEqual(manual['event'], 'workflow_dispatch')
        self.assertEqual(manual['head_branch'], 'main')
        self.assertEqual(max(runs, key=lambda r: r['id'])['head_branch'], 'v1.0.0')
        main = [r for r in runs if r['head_branch'] == 'main' and r['head_sha'] == manual['head_sha']]
        self.assertEqual(main[0]['id'], 41250377872)
