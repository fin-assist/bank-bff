"""Execute the actual read-only origin gate before any write-enabled review job."""
import json
from pathlib import Path
import re
import subprocess
import unittest


WORKFLOW = Path(__file__).resolve().parents[1] / '.github/workflows/claude.yml'


class ClaudeOriginTests(unittest.TestCase):
    def run_gate(self, payload, pull=None, failure=False):
        workflow = WORKFLOW.read_text()
        script = re.search(r'          script: \|\n((?:            .*\n|\n)+)', workflow)[1]
        script = '\n'.join(line[12:] for line in script.splitlines())
        harness = '''
const input = JSON.parse(process.argv[1]);
const outputs = {};
const calls = [];
const context = {repo: {owner: 'fin-assist', repo: 'bank-bff'}, payload: input.payload};
const core = {setOutput: (key, value) => {outputs[key] = value;}};
const github = {rest: {pulls: {get: async args => {
  calls.push(args);
  if (input.failure) throw new Error('API denied');
  return {data: input.pull};
}}}};
(async () => {
  try {
    await (async () => {SCRIPT})();
    process.stdout.write(JSON.stringify({outputs, calls}));
  } catch (error) {
    process.stdout.write(JSON.stringify({outputs, calls, error: error.message}));
    process.exitCode = 1;
  }
})();
'''.replace('SCRIPT', script)
        result = subprocess.run(['node', '-e', harness, json.dumps({
            'payload': payload, 'pull': pull, 'failure': failure,
        })], capture_output=True, text=True, timeout=10, check=False)
        return result.returncode, json.loads(result.stdout)

    @staticmethod
    def pull(head='fin-assist/bank-bff', state='open', base='fin-assist/bank-bff'):
        return {'state': state, 'head': {'repo': {'full_name': head} if head else None},
                'base': {'repo': {'full_name': base} if base else None}}

    def test_same_repository_pr_is_allowed_for_all_pr_events(self):
        # issue_comment has no top-level pull_request; fetch its live PR separately.
        for payload in ({'issue': {'number': 6, 'pull_request': {'url': 'ignored'}}},
                        {'pull_request': {'number': 6}}):
            with self.subTest(payload=payload):
                code, result = self.run_gate(payload, self.pull())
                self.assertEqual(code, 0, result)
                self.assertEqual(result['outputs']['allowed'], 'true')
                self.assertEqual(result['calls'], [{'owner': 'fin-assist', 'repo': 'bank-bff',
                                                    'pull_number': 6}])

    def test_fork_pr_is_denied_even_if_comment_event_has_base_repo_permissions(self):
        for payload in ({'issue': {'number': 6, 'pull_request': {'url': 'ignored'}}},
                        {'pull_request': {'number': 6}}):
            with self.subTest(payload=payload):
                code, result = self.run_gate(payload, self.pull(head='attacker/bank-bff'))
                self.assertEqual(code, 0, result)
                self.assertEqual(result['outputs']['allowed'], 'false')

    def test_deleted_head_wrong_base_and_closed_pr_fail_closed(self):
        for pull in (self.pull(head=None), self.pull(base=None),
                     self.pull(base='other/bank-bff'), self.pull(state='closed')):
            with self.subTest(pull=pull):
                code, result = self.run_gate({'pull_request': {'number': 6}}, pull)
                self.assertEqual(code, 0, result)
                self.assertEqual(result['outputs']['allowed'], 'false')

    def test_standalone_issue_and_missing_pr_do_not_call_api(self):
        for payload in ({'issue': {'number': 6}}, {}):
            with self.subTest(payload=payload):
                code, result = self.run_gate(payload)
                self.assertEqual(code, 0, result)
                self.assertEqual(result, {'outputs': {'allowed': 'false'}, 'calls': []})

    def test_api_failure_cannot_enable_the_write_job(self):
        code, result = self.run_gate({'pull_request': {'number': 6}}, failure=True)
        self.assertNotEqual(code, 0)
        self.assertEqual(result['outputs']['allowed'], 'false')

    def test_permissions_and_dependency_guard(self):
        workflow = WORKFLOW.read_text()
        origin, privileged = workflow.split('  review-origin:\n', 1)[1].split('  claude:\n')
        self.assertIn('    permissions:\n      pull-requests: read\n', origin)
        self.assertNotIn('write', origin)
        self.assertNotIn('actions/checkout', origin)
        self.assertNotIn('secrets.', origin)
        self.assertIn('    needs: review-origin\n', privileged)
        self.assertIn("    if: needs.review-origin.outputs.allowed == 'true'\n", privileged)


if __name__ == '__main__':
    unittest.main()
