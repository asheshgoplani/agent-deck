"""Guard against false-green result parsing and exception masking."""
import json
import shutil
import subprocess
import sys
import tempfile
import time
import os
import unittest
from pathlib import Path
from unittest import mock

import runner
from runner import check, decode_records, read_json_line

HERE = Path(__file__).resolve().parent


def process_is_gone(pid):
    """Dead, or a zombie nobody has reaped yet. The CI container has no init, so python is PID 1 and an
    orphaned grandchild stays a zombie for good: kill(pid, 0) still succeeds on it."""
    try:
        stat = Path(f'/proc/{pid}/stat').read_text()
    except FileNotFoundError:
        if Path('/proc/self/stat').exists():
            return True  # /proc works and has no entry for pid
    except OSError:
        pass
    else:
        # "<pid> (<comm>) <state> ...": comm may hold spaces and parentheses, so cut after the last ")".
        return stat[stat.rindex(')') + 2] in 'ZX'
    try:  # no /proc: signal 0 is all there is
        os.kill(pid, 0)
    except ProcessLookupError:
        return True
    return False


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
        self.addCleanup(lambda: (process.kill(), process.wait()))
        step = lambda name: json.dumps({'event': name, 'payload': {'session_id': 's'}}) + '\n'
        process.stdin.write(step('input') + '{"pause": true}\n')
        process.stdin.flush()
        lines = []
        while True:
            line = read_json_line(process, 15)
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


class DriverReadDeadlineTests(unittest.TestCase):
    """read_json_line(process, timeout_s): a live but silent driver must fail, not hang."""

    def spawn(self, code):
        process = subprocess.Popen([sys.executable, '-c', code], stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                                   stderr=subprocess.PIPE, text=True)
        self.addCleanup(lambda: (process.kill(), process.wait()))
        return process

    def test_pause_ack_timeout_fails_loudly(self):
        process = self.spawn('import sys, time; sys.stderr.write("driver diagnostics\\n"); sys.stderr.flush(); time.sleep(60)')
        started = time.monotonic()
        with self.assertRaises(Exception) as caught:
            read_json_line(process, 0.5)
        self.assertLess(time.monotonic() - started, 10, 'the read must give up at its deadline')
        self.assertNotIsInstance(caught.exception, json.JSONDecodeError)
        self.assertIn('driver diagnostics', str(caught.exception), 'the driver stderr must be in the failure')
        self.assertIn('silent for 0.5s', str(caught.exception))
        self.assertNotIn('exited', str(caught.exception))
        self.assertIsNotNone(process.returncode, 'the silent driver must be killed and reaped, not left running')

    def test_eof_raises_with_driver_stderr_not_a_bare_decode_error(self):
        process = self.spawn('import sys; sys.stderr.write("exited early\\n")')
        with self.assertRaises(Exception) as caught:
            read_json_line(process, 5)
        self.assertNotIsInstance(caught.exception, json.JSONDecodeError)
        self.assertIn('exited early', str(caught.exception))
        self.assertIn('pi driver exited before', str(caught.exception))
        self.assertIsNotNone(process.returncode, 'the failure path must reap the driver itself')

    def test_a_blank_line_from_a_live_driver_is_not_called_exited_or_silent(self):
        process = self.spawn('import sys, time; sys.stderr.write("still alive\\n"); sys.stderr.flush(); print(flush=True); time.sleep(60)')
        with self.assertRaises(Exception) as caught:
            read_json_line(process, 5)
        message = str(caught.exception)
        self.assertIn('pi driver sent a blank line before', message)
        self.assertNotIn('exited', message)
        self.assertNotIn('silent', message)
        self.assertIn('still alive', message)
        self.assertIsNotNone(process.returncode, 'the driver must be killed and reaped')

    def test_a_line_that_arrives_in_time_is_returned_parsed(self):
        process = self.spawn('import sys, time; print(\'{"paused": true}\', flush=True); time.sleep(60)')
        self.assertEqual(read_json_line(process, 10), {'paused': True})
        self.assertIsNone(process.poll(), 'a successful read leaves the driver running')


class DriverFailurePathTests(unittest.TestCase):
    """Every way a driver can fail ends in an exception and a reaped driver, within the deadline."""

    def spawn(self, code):
        # Its own session, as the rig starts the real driver: failure may then kill the whole group.
        process = subprocess.Popen([sys.executable, '-c', code], stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                                   stderr=subprocess.PIPE, text=True, start_new_session=True)
        self.addCleanup(lambda: (process.kill(), process.wait()))
        return process

    def assert_fails_and_reaps(self, process, timeout_s=5):
        with self.assertRaises(Exception) as caught:
            read_json_line(process, timeout_s)
        self.assertIsNotNone(process.returncode, 'the failure path must kill and reap the driver, not leave it to cleanup')
        return str(caught.exception)

    def test_malformed_json_line_raises_and_reaps(self):
        self.assert_fails_and_reaps(self.spawn('import time; print("{not json", flush=True); time.sleep(60)'))

    def test_non_object_json_line_raises_and_reaps(self):
        # [1] parses, but a driver frame is an object: it is a protocol error, not a result.
        self.assert_fails_and_reaps(self.spawn('import time; print("[1]", flush=True); time.sleep(60)'))

    def test_final_record_without_a_newline_then_exit_is_a_protocol_error(self):
        message = self.assert_fails_and_reaps(self.spawn('import sys; sys.stdout.write(\'{"paused": true}\')'))
        self.assertTrue(message, 'the failure must say something')

    def test_closed_stdout_on_a_live_driver_is_labelled_closed_stdout(self):
        started = time.monotonic()
        message = self.assert_fails_and_reaps(self.spawn('import os, time; os.close(1); time.sleep(60)'))
        self.assertLess(time.monotonic() - started, 10)
        self.assertIn('closed stdout', message)
        self.assertNotIn('exited', message)

    def test_a_grandchild_holding_the_pipes_does_not_defeat_the_deadline(self):
        pid_file = Path(tempfile.mkdtemp()) / 'grandchild.pid'
        self.addCleanup(shutil.rmtree, pid_file.parent, True)
        code = ('import subprocess, sys, time\n'
                'g = subprocess.Popen(["sleep", "8"])\n'  # inherits the driver's stdout and stderr
                f'open({str(pid_file)!r}, "w").write(str(g.pid))\n'
                'time.sleep(60)\n')
        process = self.spawn(code)
        deadline = time.monotonic() + 5
        # The file can exist before its content is written: wait for the pid itself.
        while time.monotonic() < deadline:
            if pid_file.exists() and pid_file.read_text().strip():
                break
            time.sleep(0.02)
        grandchild = int(pid_file.read_text())
        self.addCleanup(lambda: not process_is_gone(grandchild) and os.kill(grandchild, 9))
        started = time.monotonic()
        with self.assertRaises(Exception):
            read_json_line(process, 0.5)
        self.assertLess(time.monotonic() - started, 4, 'a grandchild holding stderr must not extend the deadline')
        self.assertIsNotNone(process.returncode)
        for _ in range(100):
            if process_is_gone(grandchild):
                break
            time.sleep(0.02)
        else:
            self.fail('the driver\'s grandchild survived the failure')


class PiRunFailurePathTests(unittest.TestCase):
    """The same failures through Rig.pi_run, with a fake `node` standing in for the driver."""

    def run_with_driver(self, driver_code, communicate_timeout=None):
        tmp = Path(tempfile.mkdtemp())
        self.addCleanup(shutil.rmtree, tmp, True)
        fake_bin = tmp / 'bin'
        fake_bin.mkdir()
        (tmp / 'driver.py').write_text(driver_code)
        node = fake_bin / 'node'
        node.write_text(f'#!/bin/sh\nexec {sys.executable} {tmp / "driver.py"}\n')
        node.chmod(0o755)
        spawned = []
        real_popen = subprocess.Popen

        def capturing(*args, **kwargs):
            process = real_popen(*args, **kwargs)
            spawned.append(process)
            self.addCleanup(lambda: (process.kill(), process.wait()))
            if communicate_timeout is not None:
                real_communicate = process.communicate
                # pi_run asks for 30 s; the test only needs it to expire.
                process.communicate = lambda *a, timeout=None, **k: real_communicate(*a, timeout=communicate_timeout, **k)
            return process

        rig = runner.Rig(tmp / 'out', tmp / 'root')
        parts = [[{'event': 'input', 'payload': {}}], [{'event': 'turn_end', 'payload': {}}]]
        with mock.patch.object(runner, 'BINARY', str(fake_bin / 'agent-deck')), \
                mock.patch.object(runner.subprocess, 'Popen', capturing), \
                mock.patch.object(rig, 'tick'):  # the daemon tick needs the real binary
            with self.assertRaises(Exception):
                rig.pi_run('pi-identity', parts)
        self.assertEqual(len(spawned), 1, 'pi_run did not start the fake driver')
        return spawned[0]

    def test_malformed_json_from_the_driver_fails_and_reaps(self):
        process = self.run_with_driver('import time\nprint("{not json", flush=True)\ntime.sleep(60)\n')
        self.assertIsNotNone(process.returncode, 'pi_run must reap the driver on a malformed line')

    def test_non_object_json_from_the_driver_fails_and_reaps(self):
        process = self.run_with_driver('import time\nprint("[1]", flush=True)\ntime.sleep(60)\n')
        self.assertIsNotNone(process.returncode, 'pi_run must reap the driver on a non-object line')

    def test_a_driver_that_acks_the_pause_then_hangs_is_reaped_when_communicate_times_out(self):
        pid_file = Path(tempfile.mkdtemp()) / 'grandchild.pid'
        self.addCleanup(shutil.rmtree, pid_file.parent, True)
        # Acks the pause, then hangs with a grandchild holding the pipes open: only
        # communicate's timeout can end the run.
        code = ('import subprocess, time\n'
                'print(\'{"paused": true}\', flush=True)\n'
                'g = subprocess.Popen(["sleep", "60"])\n'
                f'open({str(pid_file)!r}, "w").write(str(g.pid))\n'
                'time.sleep(60)\n')
        started = time.monotonic()
        process = self.run_with_driver(code, communicate_timeout=1)
        self.assertLess(time.monotonic() - started, 8, 'the communicate timeout must end the run')
        self.assertIsNotNone(process.returncode, 'pi_run must reap the driver when communicate times out')
        grandchild = int(pid_file.read_text())
        self.addCleanup(lambda: not process_is_gone(grandchild) and os.kill(grandchild, 9))
        for _ in range(100):
            if process_is_gone(grandchild):
                break
            time.sleep(0.02)
        else:
            self.fail("the driver's grandchild survived the communicate timeout")


if __name__ == '__main__':
    unittest.main()
