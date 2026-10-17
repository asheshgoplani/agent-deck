"""Guard against false-green result parsing and exception masking."""
import json
import shutil
import subprocess
import tempfile
import unittest
from pathlib import Path

from runner import check, decode_records

HERE = Path(__file__).resolve().parent


class ResultContractTests(unittest.TestCase):
    def test_expected_failure_only_accepts_enumerated_value(self):
        self.assertEqual(check('turns', 0, 1, [0])['status'], 'XFAIL')
        self.assertEqual(check('turns', 1, 1, [0])['status'], 'PASS')
        self.assertEqual(check('turns', 2, 1, [0])['status'], 'FAIL')
        self.assertEqual(check('turns', 'ssh failed', 1, [0])['status'], 'FAIL')
        self.assertEqual(check('turns', False, 1, [0])['status'], 'FAIL')

    def test_invalid_export_never_becomes_an_empty_success(self):
        for bad in ('not JSON', '{"records":{}}', '{"records":[{}]}'):
            with self.subTest(bad=bad), self.assertRaises(ValueError):
                decode_records(bad)

    def test_public_export_and_event_frames_preserve_record_identity(self):
        record = {'id': 'r1', 'key': 'turn:one', 'kind': 'turn', 'text': 'answer'}
        self.assertEqual(decode_records(json.dumps({'records': [record]})), [record])
        frames = [dict(cursor=i, data=record) for i in (1, 2)]
        # Decoder must not "fix" duplicates. The behavioral assertion sees both.
        self.assertEqual(decode_records('\n'.join(json.dumps(f) for f in frames)), [record, record])

    def test_cursor_record_export_envelope_preserves_record_identity(self):
        record = {'id': 'r1', 'key': 'turn:one', 'kind': 'turn', 'text': 'answer'}
        envelope = {'v': 1, 'records': [dict(cursor=i, record=record) for i in (1, 2)]}
        self.assertEqual(decode_records(json.dumps(envelope)), [record, record])
        envelope['records'] = [dict(cursor=1, record={})]
        with self.assertRaises(ValueError):
            decode_records(json.dumps(envelope))


PROBE_EXTENSION = """
export default function (pi) {
  let seen = 0;
  let first;
  const record = (name) => async (payload, ctx) => {
    first ??= ctx;
    seen += 1;
    pi.sendMessage({ name, seen, pid: process.pid, text: payload?.text ?? null,
      session: ctx.sessionManager.getSessionId(), sameCtx: ctx === first });
  };
  for (const name of ['agent_start', 'input', 'turn_start', 'turn_end', 'agent_settled']) pi.on(name, record(name));
}
"""


def ndjson(steps):
    return ''.join(json.dumps(step) + '\n' for step in steps)


def node_can_strip_types():
    if not shutil.which('node'):
        return False
    probe = subprocess.run(
        ['node', '-e', "require('node:module').stripTypeScriptTypes('let a: number = 1')"],
        capture_output=True,
    )
    return probe.returncode == 0


@unittest.skipUnless(node_can_strip_types(), 'node >= 22.13 required')
class PiDriverSequenceTests(unittest.TestCase):
    def setUp(self):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.extension = Path(tmp.name) / 'probe.ts'
        self.extension.write_text(PROBE_EXTENSION)

    def drive(self, stdin, *args):
        return subprocess.run(
            ['node', str(HERE / 'pi_driver.mjs'), str(self.extension), *args],
            input=stdin, capture_output=True, text=True, timeout=15,
        )

    def calls(self, result):
        self.assertEqual(result.returncode, 0, result.stderr)
        out = [json.loads(line) for line in result.stdout.splitlines() if line.strip()]
        return [o['message'] for o in out if o.get('api') == 'sendMessage']

    def test_sequence_runs_every_event_in_order_in_one_process_with_one_context(self):
        steps = [
            {'event': name, 'payload': {'text': name.upper(), 'session_id': 'matrix-pi-session'}}
            for name in ('input', 'turn_start', 'turn_end', 'agent_settled')
        ]
        calls = self.calls(self.drive(ndjson(steps)))
        self.assertEqual([c['name'] for c in calls], ['input', 'turn_start', 'turn_end', 'agent_settled'])
        self.assertEqual([c['seen'] for c in calls], [1, 2, 3, 4], 'extension state must survive between events')
        self.assertEqual(len({c['pid'] for c in calls}), 1)
        self.assertTrue(all(c['sameCtx'] for c in calls), 'every event must see the same ctx')
        self.assertEqual({c['session'] for c in calls}, {'matrix-pi-session'})
        self.assertEqual([c['text'] for c in calls], ['INPUT', 'TURN_START', 'TURN_END', 'AGENT_SETTLED'])

    def test_sequence_fails_loudly_on_an_event_the_extension_does_not_handle(self):
        steps = [{'event': 'input', 'payload': {'session_id': 's'}}, {'event': 'no_such_event', 'payload': {}}]
        result = self.drive(ndjson(steps))
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('no_such_event', result.stderr)

    def test_pause_acknowledges_then_waits_for_the_next_line(self):
        process = subprocess.Popen(
            ['node', str(HERE / 'pi_driver.mjs'), str(self.extension)],
            stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True,
        )
        self.addCleanup(process.kill)
        step = lambda name: json.dumps({'event': name, 'payload': {'session_id': 's'}}) + '\n'
        process.stdin.write(step('input') + '{"pause": true}\n')
        process.stdin.flush()
        lines = []
        while True:
            line = json.loads(process.stdout.readline())
            lines.append(line)
            if line.get('paused'):
                break
        self.assertEqual([l.get('message', {}).get('name') for l in lines[:-1]], ['input'])
        self.assertIsNone(process.poll(), 'the driver must wait for the next line after the ack')
        process.stdin.write(step('turn_end'))
        out, err = process.communicate(timeout=15)
        self.assertEqual(process.returncode, 0, err)
        self.assertEqual([json.loads(l)['message']['name'] for l in out.splitlines()], ['turn_end'])

    def test_single_event_mode_is_unchanged(self):
        result = self.drive(json.dumps({'text': 'one', 'session_id': 's'}), 'turn_end')
        calls = self.calls(result)
        self.assertEqual([(c['name'], c['seen'], c['text']) for c in calls], [('turn_end', 1, 'one')])


class PiFixtureTests(unittest.TestCase):
    def test_v2_turn_end_fixture_carries_the_identity_and_outcome_the_extension_reads(self):
        fixture = json.loads((HERE / 'fixtures' / 'pi_turnend_v2.json').read_text())
        self.assertEqual(fixture['type'], 'turn_end')
        self.assertTrue(fixture['messageEntryId'])
        self.assertEqual(fixture['outcome'], 'completed')
        self.assertEqual(fixture['message']['role'], 'assistant')
        v1 = json.loads((HERE / 'fixtures' / 'pi_turnend_v1.json').read_text())
        self.assertNotIn('messageEntryId', v1, 'v1 stays the payload without an entry id')


if __name__ == '__main__':
    unittest.main()
