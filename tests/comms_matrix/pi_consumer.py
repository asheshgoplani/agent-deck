"""Matrix cells for a pi parent that the ledger serves (PR-B).

`pi listed`: the parent is named in [comms] consumers, so it enrols, its doorbell
rides the extension's socket, and the context arrives through before_agent_start.
`pi unlisted`: the same rig with the socket on at launch but the parent not
listed; PR-A's typed behaviour, and no socket traffic at all.

The parent here is the production extension in pi_driver.mjs, a persistent
process that keeps its socket open while the child runs and the daemon ticks.
"""
import contextlib
import io
import json
import os
from pathlib import Path
import time

from runner import PAUSE_ACK_TIMEOUT_S, Rig, check, read_json_line, write_json

PARENT_TITLE = 'conductor-matrix-parent'


class FifoDriver:
    """The driver hosted in the parent's pane, seen through its two FIFOs.

    Shaped like a Popen for runner.read_json_line: stdout and stderr read like
    pipes. There is no process group to kill from here, since the pane owns the
    driver's life, and the pane holds both FIFOs open, so stdout never ends.
    """

    def __init__(self, base):
        self.base = base
        self.stdin = open(base + '.driver.in', 'w', buffering=1)
        fd = os.open(base + '.driver.out', os.O_RDONLY | os.O_NONBLOCK)
        os.set_blocking(fd, True)
        self.stdout = os.fdopen(fd, 'r')

    @property
    def pid(self):
        # kill_group falls back to kill() on this.
        raise ProcessLookupError('the pane owns the pi driver')

    @property
    def stderr(self):
        return io.StringIO(self.stderr_text())

    def poll(self):
        return None

    def wait(self, timeout=None):
        return None

    def stderr_text(self):
        path = Path(self.base + '.driver.err')
        return path.read_text() if path.exists() else ''

    def kill(self):
        # stdout stays open: after a timeout read_json_line's reader thread is
        # still blocked on it, and closing it under that thread would block too.
        with contextlib.suppress(Exception):
            self.stdin.close()


class PiParent:
    """The parent's pi extension, alive across daemon ticks, inside its pane."""

    def __init__(self, rig):
        self.rig = rig
        self.socket = rig.hook_envs.get(rig.parent, {}).get('AGENTDECK_PI_SOCKET', '')
        self.process = FifoDriver(str(rig.root / 'captures' / (rig.parent + '.raw')))
        self.lines = []
        self.sid = 'pi-parent-session'

    def step(self, event, payload):
        self.process.stdin.write(json.dumps(dict(event=event, payload=dict(payload, type=event, session_id=self.sid))) + '\n')

    def start(self):
        self.step('session_start', {})
        if not self.socket:
            return
        end = time.monotonic() + 10
        while not Path(self.socket).exists():
            if time.monotonic() > end:
                raise RuntimeError('the extension never bound its socket ' + self.socket + '; stderr: ' + self.process.stderr_text())
            time.sleep(.05)

    def wait_for(self, api, deadline=15.0):
        """Read the driver's lines until one is `api`; every line is kept."""
        end = time.monotonic() + deadline
        while True:
            line = read_json_line(self.process, max(.1, end - time.monotonic()))
            self.lines.append(line)
            if line.get('api') == api or api in line:
                return line

    def prompt(self, text):
        """A human prompt: the run start that returns the context and carries the prior receipt."""
        self.step('before_agent_start', dict(messageType='user', prompt=text))
        return self.wait_for('before_agent_start')

    def finish(self):
        self.step('session_shutdown', {})
        self.process.stdin.write(json.dumps(dict(pause=True)) + '\n')
        self.wait_for('paused', PAUSE_ACK_TIMEOUT_S)
        lines = [l for l in self.lines if l.get('api') != 'paused' and 'paused' not in l]
        self.rig.calls.append(dict(argv=['pi_driver.mjs', 'parent'], code=0, stdout=json.dumps(self.lines), stderr=self.process.stderr_text()))
        return lines


def squash(text):
    return ' '.join(text.split())


def legacy_doorbell(child):
    return f"[INBOX] urgent \u00b7 matrix-child ({child}): waiting \u00b7 details are in this turn's context\r".encode()


def _find(rig, pattern):
    return sorted(rig.root.rglob(pattern))


def consumer_state(rig, identity):
    paths = _find(rig, 'cursors/' + identity + '.json')
    return json.loads(paths[0].read_text()) if paths else None


def wake_records(records, parent):
    return [r for r in records if r.get('kind') == 'wake' and parent in r.get('to', [])]


def pi_consumer_cell(output, listed, tier):
    """One local cell with a pi parent, listed or not."""
    rig = Rig(output, consumers=[PARENT_TITLE] if listed else None, pi_driver=True)
    parent = None
    try:
        # The extension is installed before the sessions launch, so the launch
        # exports AGENTDECK_PI_SOCKET for the parent whether or not it is listed.
        rig.cli('pi-hooks', 'install')
        rig.setup('pi', 'blocked' if tier == 'info' else 'ready')
        before = rig.capture(rig.parent)
        parent = PiParent(rig)
        parent.start()
        if tier == 'info':
            # As in PR-A's info cells the parent is mid-run, so the child's turn
            # waits for its next run start instead of ringing a doorbell.
            parent.step('turn_start', {})
        started = time.monotonic()
        text = rig.emit('pi', tier)
        # The daemon is live: let it pass the ledger twice, then wait for the
        # doorbell's run when the parent can be rung.
        rig.tick()
        rig.tick()
        if listed and tier == 'urgent':
            parent.wait_for('run_done', 20)
        _, export = rig.records()
        prompt = parent.prompt('Continue on your own next turn.')
        if listed and tier == 'info':
            # A second run start: the first one's receipt rides it, and nothing is injected again.
            parent.prompt('Anything else?')
        lines = parent.finish()
        rig.tick()
        if rig.live is not None:
            rig.live.terminate()
            rig.live.wait(timeout=10)
            rig.live = None
        records, _ = rig.records()
        typed = rig.capture(rig.parent)[len(before):]
        turns = [r for r in records if r.get('from') == rig.child and r['kind'] == 'turn']
        wakes = wake_records(records, rig.parent)
        enrolment = _find(rig, 'consumers/' + rig.parent + '.json')
        sent = [l for l in lines if l.get('api') == 'sendMessage']
        returned = [l.get('returned') for l in lines if l.get('api') == 'before_agent_start']
        contexts = [m['content'] for m in returned if m]
        state = consumer_state(rig, rig.parent)
        socket_wakes = [r for r in wakes if r.get('via') == 'socket']
        tmux_wakes = [r for r in wakes if r.get('via') == 'tmux']
        checks = [check('msg_export_available', export, True, [False]),
                  check('one_turn', len(turns), 1),
                  check('socket_env_at_launch', bool(rig.hook_envs.get(rig.parent, {}).get('AGENTDECK_PI_SOCKET')), True),
                  check('enrolment_file', bool(enrolment), listed)]
        if listed:
            checks.append(check('parent_enrolled_as_pi', json.loads(enrolment[0].read_text()).get('tool') if enrolment else None, 'pi'))
        if tier == 'info':
            checks += [check('info_no_input', typed.hex(), ''),
                       check('info_no_doorbell_frame', len(sent), 0),
                       check('info_no_wake_record', len(wakes), 0)]
            if listed:
                checks += [check('prompt_context_carries_text', len(contexts) >= 1 and squash(text) in squash(contexts[0]), True),
                           check('next_prompt_does_not_reinject', len(contexts), 1)]
        elif listed:
            checks += [check('urgent_woken_once', len(sent), 1),
                       check('wake_via_socket', [r.get('state') for r in socket_wakes], ['landed']),
                       check('no_tmux_wake_record', len(tmux_wakes), 0),
                       check('single_wake_record', len(wakes), 1),
                       check('parent_pane_untouched', typed.hex(), ''),
                       check('wake_injected_context', bool(returned) and returned[0] is not None and squash(text) in squash(returned[0]['content']), True),
                       check('receipt_echoed_and_acked', (state or {}).get('pending'), 0),
                       check('no_attempt_left_in_flight', (state or {}).get('inflight', []), []),
                       check('turn_shown_to_the_parent', any(k.startswith('turn:' + rig.child + ':') for k in (state or {}).get('shown', [])), True),
                       check('next_prompt_does_not_reinject', len(contexts), 1)]
        else:
            checks += [check('no_socket_frame', len(sent), 0),
                       check('no_socket_wake_record', len(socket_wakes), 0)]
            if tier == 'urgent':
                # PR-A's line, byte for byte: the legacy inbox doorbell, typed once.
                checks += [check('single_tmux_wake_record', len(tmux_wakes), 1),
                           check('typed_line_is_the_legacy_doorbell', typed.hex(), legacy_doorbell(rig.child).hex())]
            else:
                checks += [check('info_no_input', typed.hex(), ''), check('info_no_wake_record', len(wakes), 0)]
            checks.append(check('no_injected_context', len(contexts), 0))
        result = dict(tool='pi', tier=tier, location='local', listed=listed, expected_text=text, child=rig.child,
                      parent=rig.parent, root=str(rig.root), checks=checks, records=records, driver=lines,
                      typed_hex=typed.hex(), consumer_state=state, elapsed=time.monotonic() - started)
        write_json(Path(output) / 'result.json', result)
        return result
    finally:
        if parent is not None:
            parent.process.kill()
        rig.close()
