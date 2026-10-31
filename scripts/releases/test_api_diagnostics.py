import subprocess
import unittest
from unittest.mock import patch

import verified_release as v


class APIDiagnosticsTests(unittest.TestCase):
    def test_api_failures_are_denied_without_leaking_response(self):
        for status, category in [(401, 'authentication'), (403, 'permission_or_rate_limit'),
                                 (404, 'missing_or_inaccessible'), (500, 'api_or_transport')]:
            with self.subTest(status=status):
                result = subprocess.CompletedProcess([], 1, b'',
                    f'gh: sensitive-response (HTTP {status}) token=secret'.encode())
                with patch.object(v.subprocess, 'run', return_value=result):
                    with self.assertRaises(v.Denied) as raised:
                        v.GitHub().get('rules/branches/main')
                message = str(raised.exception)
                self.assertIn('GET repos/ghptc/whiterun-music-bot/rules/branches/main', message)
                self.assertIn('exit=1', message)
                self.assertIn('category=' + category, message)
                self.assertNotIn('secret', message)
                self.assertNotIn('sensitive-response', message)

    def test_transport_error_remains_fail_closed(self):
        with patch.object(v.subprocess, 'run', return_value=subprocess.CompletedProcess([], 2, b'', b'network secret')):
            with self.assertRaisesRegex(v.Denied, 'exit=2 http=unknown category=api_or_transport'):
                v.GitHub().get('branches/main')

    def test_repository_metadata_has_no_trailing_slash(self):
        with patch.object(v.subprocess, 'run', return_value=subprocess.CompletedProcess([], 0, b'{"id":99}', b'')) as run:
            self.assertEqual(v.GitHub().get(''), {'id': 99})
            self.assertEqual(run.call_args.args[0], ['gh', 'api', 'repos/ghptc/whiterun-music-bot'])
