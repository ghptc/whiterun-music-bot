"""Owner-authorized merge policy keeps release provenance and CI mandatory."""
import unittest

import verified_release as v
from test_verified_release import API, A


class OwnerPolicyTests(unittest.TestCase):
    def api(self):
        api = API()
        api.rules[1]['parameters'].update(required_approving_review_count=0,
                                          require_last_push_approval=False)
        return api

    def test_owner_merge_does_not_require_independent_review(self):
        api = self.api()
        self.assertEqual(v.verified_main(api, A), A)
        self.assertEqual(v.release_manifest(api, 20, api.attest)['commit'], A)

    def test_owner_policy_keeps_required_pr(self):
        api = self.api()
        api.rules[:] = [r for r in api.rules if r['type'] != 'pull_request']
        with self.assertRaises(v.Denied):
            v.verified_main(api, A)

    def test_owner_policy_does_not_allow_pending_ci(self):
        api = self.api()
        api.ci.update(status='in_progress', conclusion=None)
        with self.assertRaises(v.Denied):
            v.release_manifest(api, 20, api.attest)

    def test_owner_policy_still_requires_production_approval(self):
        api = self.api()
        api.approvals.clear()
        with self.assertRaises(v.Denied):
            v.production_approval(api, 30, 20, 19, api.manifest['image'], False, A)
