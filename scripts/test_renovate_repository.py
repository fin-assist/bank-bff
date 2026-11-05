"""Bank BFF wiring: its actual required checks and existing update-policy invariants."""
import base64
import json
from pathlib import Path
import unittest

from renovate_policy import UPDATES, reconcile
from test_renovate_controller import Fake

ROOT = Path(__file__).resolve().parents[1]


class RepositoryPolicyTests(unittest.TestCase):
    def test_existing_update_groups_and_manual_toolchain_policy_are_preserved(self):
        baseline = json.loads((ROOT / 'renovate-base.json').read_text())
        manual = next(rule for rule in baseline['packageRules']
                      if rule.get('matchManagers') == ['gomod'])
        self.assertEqual(set(manual['matchDepTypes']), {'golang', 'toolchain'})
        self.assertIs(manual['enabled'], False)
        shared = next(rule for rule in baseline['packageRules']
                      if rule.get('groupSlug') == 'finassist-shared-go-ci')
        self.assertEqual(shared['groupName'], 'FinAssist shared Go CI')
        self.assertEqual(shared['schedule'], ['at any time'])
        self.assertIs(shared['pinDigests'], True)
        self.assertEqual(len(baseline['customManagers']), 2)
        # FIN-447 must not introduce the donor library's weekly grouping.
        self.assertEqual(len(baseline['packageRules']), 3)
        self.assertFalse(any(rule.get('groupName') == 'third-party dependencies'
                             for rule in baseline['packageRules']))
        root = json.loads((ROOT / 'renovate.json').read_text())
        self.assertEqual(root['extends'], ['local>fin-assist/bank-bff:base#renovate-policy',
                                         'local>fin-assist/bank-bff:policy#renovate-policy'])
        self.assertIs(root['automerge'], False)
        self.assertTrue(root['commitBody'].startswith('{{#each upgrades}}'))

    def test_only_real_bff_jobs_are_required(self):
        config = json.loads((ROOT / '.github/renovate-policy.json').read_text())
        self.assertEqual(set(config['ciChecks']), {'go', 'lint', 'image'})
        self.assertEqual(set(config['requiredChecks']),
                         {'go', 'lint', 'image', 'Renovate policy tests',
                          'Renovate runtime contract'})
        self.assertEqual(config['controllerLogin'], 'avtomon')
        workflow = (ROOT / '.github/workflows/renovate-policy.yml').read_text()
        self.assertIn('workflows: [CI, Renovate policy tests]', workflow)

    def test_manual_go_identity_blocks_mixed_pr_merge_retry_and_close(self):
        identities = [('github-releases', 'actions/go-versions'),
                      ('github-tags', 'actions/go-versions'), ('docker', 'golang'),
                      ('docker', 'library/golang'), ('docker', 'docker.io/library/golang'),
                      ('golang-version', 'go'), ('golang-version', 'golang/go')]
        for datasource, name in identities:
            for conclusion, attempt in [('success', 1), ('failure', 1), ('failure', 2)]:
                with self.subTest(datasource=datasource, name=name,
                                  conclusion=conclusion, attempt=attempt):
                    api = Fake()
                    items = [{'depName': 'normal', 'datasource': 'npm', 'newValue': '2'},
                             {'depName': 'go', 'packageName': name,
                              'datasource': datasource, 'newValue': '1.27.3'}]
                    api.message = UPDATES + base64.b64encode(json.dumps(items).encode()).decode() + ' -->'
                    api.run.update(conclusion=conclusion, run_attempt=attempt)
                    api.jobs[0]['conclusion'] = conclusion
                    if attempt == 2:
                        api.receipts = {'1:abc': {'state': {'sha': 'abc', 'runs': {
                            'ci.yml': {'run_id': 1, 'attempt': 1, 'retried': True}}}}}
                    reconcile(api, api.config)
                    self.assertEqual(api.actions, [])
                    self.assertEqual(api.pr['state'], 'open')
                    self.assertEqual(api.comments, [])
