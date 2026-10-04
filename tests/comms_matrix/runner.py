#!/usr/bin/env python3
"""Black-box comms acceptance rig. Run only inside the disposable Docker rig."""
import argparse
import contextlib
import datetime
import hashlib
import json
import os
from pathlib import Path
import shlex
import signal
import subprocess
import sys
import tempfile
import urllib.request
import time
import uuid

HERE = Path(__file__).resolve().parent
TOOLS = ('claude', 'codex', 'gemini', 'pi', 'hermes', 'opencode', 'cursor')
BINARY = '/usr/local/bin/agent-deck'


def write_json(path, data):
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(data, indent=2) + '\n')


def check(name, actual, target, baseline=None):
    """Only an enumerated, measured baseline may be an expected failure."""
    status = 'PASS' if type(actual) is type(target) and actual == target else 'FAIL'
    if status == 'FAIL' and baseline is not None and any(type(actual) is type(value) and actual == value for value in baseline):
        status = 'XFAIL'
    return dict(name=name, status=status, actual=actual, target=target)


def decode_records(text):
    """Accept public NDJSON event frames or the public msg export envelope."""
    if not text.strip():
        return []
    try:
        value = json.loads(text)
    except json.JSONDecodeError:
        value = [json.loads(line) for line in text.splitlines() if line.strip()]
    if isinstance(value, dict):
        value = value.get('records', value.get('events', [value]))
    if not isinstance(value, list):
        raise ValueError('export is not a record list')
    records = []
    for item in value:
        if isinstance(item, dict) and 'cursor' in item:
            if 'record' in item:
                item = item['record']
            elif 'data' in item:
                item = item['data']
        if not isinstance(item, dict) or 'kind' not in item:
            raise ValueError('export contains an invalid record')
        records.append(item)
    return records


class Rig:
    def __init__(self, output, root=None):
        self.output = Path(output)
        self.output.mkdir(parents=True, exist_ok=True)
        self.root = Path(root or tempfile.mkdtemp(prefix='comms-matrix-'))
        self.root.mkdir(parents=True, exist_ok=True)
        self.socket = 'cm-' + uuid.uuid4().hex[:12]
        self.sessions = []
        self.processes = []
        self.live = None
        self.calls = []
        self.hook_outputs = []
        self.env = {k: v for k, v in os.environ.items() if k in ('PATH', 'LANG', 'TERM', 'COMMS_REMOTE')}
        self.env.update(HOME=str(self.root), XDG_CONFIG_HOME=str(self.root / 'config'),
                        XDG_DATA_HOME=str(self.root / 'data'), XDG_CACHE_HOME=str(self.root / 'cache'),
                        XDG_STATE_HOME=str(self.root / 'state'), XDG_RUNTIME_DIR=str(self.root / 'runtime'),
                        TMUX_TMPDIR=str(self.root / 'tmux'), AGENTDECK_SKIP_UPDATE_CHECK='1',
                        CI='true', COMMS_CAPTURE_DIR=str(self.root / 'captures'),
                        PATH=str(HERE / 'bin') + ':' + os.environ['PATH'], TERM='xterm-256color')
        for name in ('config/agent-deck', 'data', 'cache', 'state', 'runtime', 'tmux', 'captures', 'work'):
            (self.root / name).mkdir(parents=True, exist_ok=True)
        self.config = self.root / 'config/agent-deck/config.toml'
        self.config.write_text('[comms]\nledger = true\n[tmux]\nsocket_name = ' + json.dumps(self.socket) + '\n')
        self.ports = {}
        self.hook_envs = {}
        self.parent = None
        launcher = self.root / 'agent-deck-env'
        launcher.write_text('#!/bin/sh\nexec env ' + ' '.join(shlex.quote(k + '=' + v) for k, v in self.env.items()) + ' ' + BINARY + ' \"$@\"\n')
        launcher.chmod(0o755)
        self.child = None

    def run(self, args, *, env=None, input=None, timeout=20, ok=True):
        started = time.monotonic()
        result = subprocess.run(args, env=env or self.env, input=input, text=True,
                                capture_output=True, timeout=timeout)
        self.calls.append(dict(argv=args, code=result.returncode, stdout=result.stdout,
                               stderr=result.stderr, elapsed=time.monotonic() - started))
        if ok and result.returncode:
            raise RuntimeError(f'{args}: exit {result.returncode}: {result.stderr} {result.stdout}')
        return result

    def cli(self, *args, **kw):
        return self.run([BINARY, '-p', 'default', *args], **kw)

    def add(self, tool, title, parent=None, state='ready'):
        command = 'cursor-agent' if tool == 'cursor' else tool
        args = ['add', '-t', title, '-c', command, '--json', '--no-identity',
                '--wrapper', f'env COMMS_CAPTURE_DIR={self.root}/captures COMMS_PANE_STATE={state} {{command}}']
        args += ['--parent', parent] if parent else ['--no-parent']
        result = json.loads(self.cli(*args, str(self.root / 'work')).stdout)
        session = result.get('session', result)
        identity = session.get('id', result.get('id'))
        if not identity:
            raise RuntimeError(f'add returned no id: {result}')
        # Register cleanup before start, including partially failed starts.
        self.sessions.append(identity)
        wrapper = f'export PATH={HERE}/bin:/usr/local/bin:/usr/bin:/bin COMMS_CAPTURE={self.root}/captures/{identity}.raw COMMS_PANE_STATE={state} COMMS_HTTP_RECEIPT={self.output}/http-{identity}.jsonl; {{command}}'
        self.cli('session', 'set', identity, 'wrapper', wrapper)
        self.cli('session', 'start', identity, timeout=30)
        capture = self.root / 'captures' / (identity + '.raw')
        deadline = time.monotonic() + 5
        while not capture.exists() and time.monotonic() < deadline:
            time.sleep(.05)
        if not capture.exists():
            raise RuntimeError(f'fake {tool} did not start: {self.cli("session", "show", identity, "--json").stdout}')
        # Observe only the owned fake process. Native generation metadata must
        # reach replay hooks exactly as it would when the harness spawns them.
        for path in Path('/proc').glob('[0-9]*/environ'):
            with contextlib.suppress(OSError, UnicodeDecodeError):
                environment = dict(part.split('=', 1) for part in path.read_bytes().decode().split('\0') if '=' in part)
                argv = (path.parent / 'cmdline').read_bytes().decode().split('\0')
                if environment.get('COMMS_CAPTURE') != str(capture) or not any(Path(a).resolve() == HERE / 'fake_harness.py' for a in argv if a.endswith('fake_harness.py')):
                    continue
                self.hook_envs[identity] = {k: v for k, v in environment.items() if k.startswith('AGENTDECK_')}
                if tool == 'opencode' and '--port' in argv:
                    self.ports[identity] = int(argv[argv.index('--port') + 1])
                write_json(self.output / ('spawn-' + identity + '.json'), dict(argv=argv, hook_env=self.hook_envs[identity]))
                break
        return identity

    def setup(self, tool, state='ready'):
        self.parent = self.add(tool, 'conductor-matrix-parent', state=state)
        self.child = self.add(tool, 'matrix-child', self.parent)
        self.tick()
        if tool not in ('claude', 'codex'):
            log = (self.output / 'daemon.log').open('w')
            self.live = subprocess.Popen([BINARY, '-p', 'default', 'notify-daemon'], env=self.env, stdout=log, stderr=log)
            log.close()
            self.processes.append(self.live)
            self.tick()

    def tick(self):
        if self.live is not None:
            if self.live.poll() is not None:
                raise RuntimeError('continuous daemon exited')
            time.sleep(2.2)
            return None
        return self.cli('notify-daemon', '--once', timeout=30)

    def capture(self, identity):
        return (self.root / 'captures' / (identity + '.raw')).read_bytes()

    def hook(self, tool, event, payload, identity):
        path = self.output / ('payload-' + uuid.uuid4().hex + '.json')
        write_json(path, payload)
        result = self.run(['python3', str(HERE / 'fake_harness.py'), '--tool', tool,
                         '--event', event, '--payload', str(path), '--instance', identity,
                         '--binary', BINARY], env=dict(self.env, **self.hook_envs.get(identity, {})))
        self.hook_outputs.append(dict(tool=tool, event=event, instance=identity, stdout=result.stdout))
        return result

    def fixture(self, name):
        return json.loads((HERE.parents[1] / 'cmd/agent-deck/testdata/comms' / name).read_text())

    def emit(self, tool, tier, turn='turn-1', text=None, identity=None):
        identity = identity or self.child
        text = text or ('Built and tested.\n===AGENTDECK_DONE=== status=ok summary=matrix' if tier == 'urgent' else 'Background task completed with a new result.')
        sid = '019a0000-0000-7000-8000-' + hashlib.sha256(identity.encode()).hexdigest()[:12]
        prompt = f'[agent-deck from:{self.parent}] build it' if tier == 'urgent' else '<task-notification>background task complete</task-notification>'
        common = dict(session_id=sid, cwd=str(self.root / 'work'))
        if tool == 'claude':
            transcript = self.root / '.claude/projects/matrix' / (sid + '.jsonl')
            transcript.parent.mkdir(parents=True, exist_ok=True)
            now = datetime.datetime.now(datetime.timezone.utc).isoformat()
            frames = [dict(type='user', uuid='user-' + turn, timestamp=now, message=dict(role='user', content=prompt)),
                      dict(type='assistant', uuid=turn, timestamp=now, message=dict(role='assistant', content=[dict(type='text', text=text)]))]
            with transcript.open('a') as stream:
                for frame in frames:
                    stream.write(json.dumps(frame) + '\n')
            self.cli('session', 'set', identity, 'claude-session-id', sid)
            common['transcript_path'] = str(transcript)
            ups = self.fixture('claude_userpromptsubmit_v1.json')
            ups.update(common, prompt=prompt)
            self.hook(tool, 'UserPromptSubmit', ups, identity)
            stop = self.fixture('claude_stop_v1.json')
            stop.update(common, last_assistant_message=text)
            self.hook(tool, 'Stop', stop, identity)
        elif tool == 'codex':
            transcript = self.root / '.codex/sessions/2026/10/04' / ('rollout-2026-10-04T00-00-00-' + sid + '.jsonl')
            transcript.parent.mkdir(parents=True, exist_ok=True)
            transcript.write_text(json.dumps(dict(type='session_meta', payload=dict(id=sid, source='cli'))) + '\n')
            payload = self.fixture('codex_notify_v1.json')
            payload.update({'thread-id': sid, 'turn-id': turn, 'cwd': common['cwd'],
                            'input-messages': [prompt], 'last-assistant-message': text})
            self.hook(tool, 'agent-turn-complete', payload, identity)
            self.hook(tool, 'Stop', dict(common, hook_event_name='Stop', turn_id=turn, last_assistant_message=text), identity)
        elif tool == 'gemini':
            self.hook(tool, 'BeforeAgent', dict(common, hook_event_name='BeforeAgent', prompt=prompt), identity)
            self.tick()
            self.hook(tool, 'AfterAgent', dict(common, hook_event_name='AfterAgent', prompt_response=text, prompt=prompt), identity)
        elif tool == 'hermes':
            self.hook(tool, 'pre_llm_call', dict(common, hook_event_name='pre_llm_call', user_message=prompt), identity)
            self.tick()
            self.hook(tool, 'post_llm_call', dict(common, hook_event_name='post_llm_call', assistant_response=text, user_message=prompt), identity)
        elif tool == 'cursor':
            self.hook(tool, 'beforeSubmitPrompt', dict(conversation_id=sid, hook_event_name='beforeSubmitPrompt', prompt=prompt), identity)
            self.tick()
            self.hook(tool, 'afterAgentResponse', dict(conversation_id=sid, hook_event_name='afterAgentResponse', text=text), identity)
            self.hook(tool, 'stop', dict(conversation_id=sid, hook_event_name='stop', status='completed'), identity)
        elif tool == 'pi':
            self.cli('pi-hooks', 'install')
            self.hook(tool, 'turn_start', dict(type='turn_start'), identity)
            self.tick()
            self.hook(tool, 'turn_end', dict(type='turn_end', message=dict(role='assistant', content=[dict(type='text', text=text)])), identity)
        elif tool == 'opencode':
            text = json.loads((HERE / 'fixtures/opencode_messages_v1.json').read_text())[0]['parts'][0]['text']
            port = self.ports.get(identity)
            if not port:
                raise RuntimeError('OpenCode session has no allocated server port')
            for status in ('busy', 'idle'):
                payload = dict(type='session.status', properties=dict(sessionID='matrix-opencode-session', status=dict(type=status)))
                request = urllib.request.Request(f'http://127.0.0.1:{port}/emit', data=json.dumps(payload).encode(), headers={'Content-Type': 'application/json'})
                with urllib.request.urlopen(request, timeout=5) as response:
                    json.load(response)
                self.tick()
        return text

    def records(self):
        result = self.cli('msg', 'export', '--json', ok=False)
        if result.returncode == 0:
            return decode_records(result.stdout), True
        # Only the exact pre-P2 absence permits the public event-stream fallback.
        if 'Unknown command' not in result.stderr + result.stdout and 'unknown command' not in result.stderr + result.stdout and '"msg" is not a recognized command' not in result.stderr + result.stdout:
            raise RuntimeError('msg export failed unexpectedly: ' + result.stderr + result.stdout)
        p = subprocess.Popen([BINARY, '-p', 'default', 'events', 'follow', '--bus', 'comms', '--json'], env=self.env,
                             stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
        try:
            stdout, stderr = p.communicate(timeout=.7)
        except subprocess.TimeoutExpired:
            p.send_signal(signal.SIGTERM)
            stdout, stderr = p.communicate(timeout=5)
        if p.returncode:
            if 'comms: no ledger for this profile yet' in stderr:
                return [], False
            raise RuntimeError('events follow failed: ' + stderr)
        return decode_records(stdout), False

    def close(self):
        for p in self.processes:
            if p.poll() is None:
                p.terminate()
                p.wait(timeout=5)
        # Resolve EXACTLY the same named socket and TMUX_TMPDIR as start.
        found = self.run(['tmux', '-L', self.socket, 'list-sessions', '-F', '#{session_id}'], ok=False)
        if found.returncode == 0:
            for session in found.stdout.splitlines():
                self.run(['tmux', '-L', self.socket, 'kill-session', '-t', session], ok=False)
        write_json(self.output / 'commands.json', self.calls)
        for directory in ('cache', 'captures'):
            for src in (self.root / directory).rglob('*'):
                if src.is_file():
                    dst = self.output / directory / src.relative_to(self.root / directory)
                    dst.parent.mkdir(parents=True, exist_ok=True)
                    dst.write_bytes(src.read_bytes())


def local_cell(output, tool, tier, root=None):
    rig = Rig(output, root)
    try:
        rig.setup(tool, 'blocked' if tier == 'info' else 'ready')
        before = rig.capture(rig.parent)
        send_env = dict(rig.env, AGENTDECK_INSTANCE_ID=rig.parent)
        sent = rig.cli('session', 'send', '--json', '--no-wait', '--timeout', '2s', rig.child, 'Matrix parent-assigned task.', env=send_env, ok=False, timeout=15)
        write_json(rig.output / 'send.json', dict(code=sent.returncode, stdout=sent.stdout, stderr=sent.stderr))
        started = time.monotonic()
        text = rig.emit(tool, tier)
        rig.tick()
        if tool not in ('claude', 'codex'):
            rig.tick()
        if rig.live is not None:
            rig.live.terminate()
            rig.live.wait(timeout=10)
            rig.live = None
        shown = json.loads(rig.cli('session', 'show', rig.child, '--json').stdout)
        records, export = rig.records()
        turns = [r for r in records if r.get('from') == rig.child and r['kind'] == 'turn']
        stats = json.loads(rig.cli('inbox', 'stats', '--json', '--all').stdout)
        checks = [check('msg_export_available', export, True, [False]),
                  check('one_turn', len(turns), 1, [0] if tool not in ('claude', 'codex') else None),
                  check('turn_text', [r.get('text') for r in turns], [text], [[]] if tool not in ('claude', 'codex') else None),
                  check('turn_tier', [r.get('tier') for r in turns], [tier], [[]] if tool not in ('claude', 'codex') else None)]
        if tool != 'opencode':
            checks.append(check('hook_receipt_fresh', shown.get('hook_status_fresh', False), True))
            checks.append(check('hook_receipt_completed', shown.get('hook_status'), 'waiting'))
        else:
            http = rig.output / ('http-' + rig.child + '.jsonl')
            calls = [json.loads(line) for line in http.read_text().splitlines()] if http.exists() else []
            checks.append(check('sse_consumer_connected', any(c['path'] == '/event' for c in calls), True, [False]))
        keys = [r.get('key') for r in turns]
        checks.append(check('duplicate_keys', len(keys) - len(set(keys)), 0))
        checks.append(check('text_cap', all(0 < len(r.get('text', '').encode()) <= 2048 for r in turns), True))
        typed = rig.capture(rig.parent)[len(before):]
        if tier == 'info':
            checks.append(check('info_stop_no_block', any('\"block\"' in h['stdout'] for h in rig.hook_outputs if h['instance'] == rig.child and h['event'] in ('Stop', 'stop')), False))
            baseline = None
            if tool == 'hermes':
                # P1's generation-seeded parent receives exactly one legacy nudge.
                # Never apply this allowance to approval safety or another tool.
                legacy = f"[INBOX] urgent · matrix-child ({rig.child}): waiting · details are in this turn's context\r"
                baseline = [legacy.encode().hex()]
            checks.append(check('info_no_input', typed.hex(), '', baseline))
        checks.append(check('no_legacy_doorbell', b'[INBOX] A child just committed' in typed, False, [True]))
        count = sum(s.get('records_urgent', 0) + s.get('records_info', 0) + s.get('records_legacy', 0) for s in stats if s.get('parent') == rig.parent)
        child_records = [r for r in records if r.get('from') == rig.child and r['kind'] in ('turn', 'status')]
        stats_baseline = None
        if tool == 'codex' and len(turns) == 1:
            stats_baseline = [0]
        elif not turns and len(child_records) == 1:
            # Measured legacy-counter overcount on the pre-consumer baseline.
            stats_baseline = {'hermes': [2, 3], 'cursor': [2, 3, 4, 5]}.get(tool)
        checks.append(check('stats_match_records', count, len(child_records), stats_baseline))
        sends = [r for r in records if r['kind'] == 'send' and r.get('from') == rig.parent and rig.child in r.get('to', [])]
        final_states = {'injected', 'typed', 'landed', 'failed'}
        valid_sends = [r for r in sends if r.get('text') == 'Matrix parent-assigned task.' and r.get('th') == hashlib.sha256(r['text'].encode()).hexdigest()[:16] and (r.get('state') in final_states or any(d.get('ref') == r['id'] and d.get('state') in final_states for d in records if d['kind'] == 'delivery'))]
        checks.append(check('send_sender_text_hash_final_state', len(valid_sends), 1, [0]))
        if tier == 'urgent':
            wakes = sum(s.get('wakeups_urgent', 0) for s in stats if s.get('parent') == rig.parent)
            checks.append(check('urgent_woken_once', wakes, 1, [0]))
            checks.append(check('typed_wake_carries_text', not typed or text.encode() in typed, True, [False]))
            # Latency uses ledger timestamps; wall time includes observation overhead.
            checks.append(check('urgent_record_within_2s', len([r for r in turns if r.get('t_signal', 0) > 0 and r.get('t_record', 0) > 0 and 0 <= r['t_record'] - r['t_signal'] <= 2000]), 1, [0] if tool not in ('claude', 'codex') else None))
        if tool in ('claude', 'codex', 'gemini', 'hermes', 'cursor'):
            event = {'claude': 'UserPromptSubmit', 'codex': 'UserPromptSubmit', 'gemini': 'BeforeAgent', 'hermes': 'pre_llm_call', 'cursor': 'sessionStart'}[tool]
            payload = dict(hook_event_name=event, session_id='matrix-parent-thread', prompt='Continue on your own next turn.', user_message='Continue on your own next turn.')
            if tool == 'codex':
                payload['turn_id'] = 'parent-next-turn'
            injection = rig.hook(tool, event, payload, rig.parent).stdout
            write_json(rig.output / 'next-prompt.json', dict(event=event, output=injection))
            injected = json.loads(injection) if injection.strip() else {}
            context = injected.get('hookSpecificOutput', {}).get('additionalContext', injected.get('context', injected.get('additional_context', '')))
            checks.append(check('next_prompt_record_id', bool(turns) and all(r['id'] in context for r in turns), True, [False]))
        elif tool == 'pi':
            injection = rig.hook(tool, 'session_start', dict(type='session_start', session_id=rig.parent), rig.parent).stdout
            checks.append(check('native_consumer_record_id', bool(turns) and all(r['id'] in injection for r in turns), True, [False]))
        elif tool == 'opencode':
            http = rig.output / ('http-' + rig.parent + '.jsonl')
            requests = [json.loads(line) for line in http.read_text().splitlines()] if http.exists() else []
            deliveries = [c for c in requests if c['method'] == 'POST' and c['path'].endswith('/prompt_async')]
            checks.append(check('native_consumer_record_id', bool(turns) and all(any(r['id'] in json.dumps(c['payload']) for c in deliveries) for r in turns), True, [False]))
        result = dict(tool=tool, tier=tier, expected_text=text, child=rig.child, parent=rig.parent, root=str(rig.root), checks=checks,
                      records=records, stats=stats, elapsed=time.monotonic() - started, typed_hex=typed.hex())
        write_json(Path(output) / 'result.json', result)
        return result
    finally:
        rig.close()




def remote_snapshot(rig, host, launcher):
    command = [launcher, '-p', 'default', 'msg', 'export', '--json']
    result = rig.run(['ssh', host, shlex.join(command)], ok=False)
    if result.returncode == 0:
        return decode_records(result.stdout)
    if '"msg" is not a recognized command' not in result.stderr + result.stdout:
        raise RuntimeError('remote export failed unexpectedly: ' + result.stderr)
    command = ['timeout', '--signal=TERM', '1', launcher, '-p', 'default', 'events', 'follow', '--bus', 'comms', '--json']
    result = rig.run(['ssh', host, shlex.join(command)], ok=False)
    if result.returncode in (0, 124):
        return decode_records(result.stdout)
    if 'comms: no ledger for this profile yet' in result.stderr:
        return []
    raise RuntimeError('remote ledger snapshot failed: ' + result.stderr)

def remote_cell(output, tool, tier):
    rig = Rig(output)
    try:
        rig.parent = rig.add(tool, 'matrix-remote-parent', state='blocked' if tier == 'info' else 'ready')
        remote_root = '/tmp/comms-remote-' + uuid.uuid4().hex
        host = os.environ.get('COMMS_REMOTE', 'r1')
        command = ['python3', str(HERE / 'runner.py'), '--single', tool, '--tier', tier,
                   '--root', remote_root, '--output', remote_root + '/evidence']
        result = rig.run(['ssh', host, shlex.join(command)], timeout=120)
        source = json.loads(result.stdout)
        write_json(Path(output) / 'remote-source.json', source)
        # Source sessions have stopped; read-only exports still exercise SSH.
        with rig.config.open('a') as stream:
            stream.write('\n[remotes.r1]\nhost = ' + json.dumps(host) + '\nagent_deck_path = ' + json.dumps(remote_root + '/agent-deck-env') + '\ncommand_timeout_seconds = 5\n')
        source_before = remote_snapshot(rig, host, remote_root + '/agent-deck-env')
        before = rig.capture(rig.parent)
        started = time.monotonic()
        first = rig.cli('remote', 'drain', 'r1', '--into', rig.parent, '--json', timeout=20)
        rig.tick()
        records, export = rig.records()
        pull_seconds = time.monotonic() - started
        second = rig.cli('remote', 'drain', 'r1', '--into', rig.parent, '--json', timeout=20)
        rig.tick()
        repeated, _ = rig.records()
        source_after = remote_snapshot(rig, host, remote_root + '/agent-deck-env')
        imported = [r for r in records if r.get('origin') == 'r1' and r.get('from') == source['child'] and r['kind'] == 'turn']
        again = [r for r in repeated if r.get('origin') == 'r1' and r.get('from') == source['child'] and r['kind'] == 'turn']
        source_turns = [r for r in source['records'] if r.get('from') == source['child'] and r['kind'] == 'turn']
        checks = [dict(c, name='remote_source.' + c['name']) for c in source['checks']]
        checks += [check('pull_does_not_change_remote_records', source_after, source_before),
                   check('pull_completed_within_10s', pull_seconds <= 10, True),
                   check('imported_turn_count', len(imported), 1, [0]),
                   check('remote_text_preserved', [r.get('text') for r in imported], [source['expected_text']], [[]]),
                   check('remote_tier_preserved', [r.get('tier') for r in imported], [tier], [[]]),
                   check('pull_idempotent', len(again) - len(imported), 0),
                   check('second_pull_writes_zero', json.loads(second.stdout).get('written'), 0),
                   check('cursor_continuity', json.loads(second.stdout).get('cursor_before'), json.loads(first.stdout).get('cursor_after')),
                   check('remote_key_preserved', [r['key'] for r in imported], [r['key'] for r in source_turns], [[]]),
                   check('remote_latency_fields', len([r for r in imported if all(r.get(k, 0) > 0 for k in ('t_signal', 't_record', 't_seen'))]), 1, [0])]
        if tier == 'info':
            checks.append(check('remote_info_no_input', rig.capture(rig.parent)[len(before):].hex(), ''))
        receipt = dict(tool=tool, tier=tier, location='remote', checks=checks, records=records,
                       pull_seconds=pull_seconds, first=json.loads(first.stdout), second=json.loads(second.stdout))
        write_json(Path(output) / 'result.json', receipt)
        return receipt
    finally:
        rig.close()

def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--output', required=True)
    parser.add_argument('--group', choices=['all', 'claude', 'codex', 'others', 'failures'], default='all')
    parser.add_argument('--single', choices=TOOLS)
    parser.add_argument('--tier', choices=['urgent', 'info'], default='urgent')
    parser.add_argument('--root')
    args = parser.parse_args()
    if not Path('/.dockerenv').exists():
        parser.error('matrix runs only in its disposable Docker containers')
    if args.single:
        print(json.dumps(local_cell(args.output, args.single, args.tier, args.root)))
        return
    tools = TOOLS if args.group == 'all' else ((args.group,) if args.group in TOOLS else TOOLS[2:])
    results = []
    if args.group != 'failures':
        for tool in tools:
            for tier in ('urgent', 'info'):
                for location, probe in [('local', local_cell), ('remote', remote_cell)]:
                    output = Path(args.output) / (tool + '-' + location + '-' + tier)
                    try:
                        result = probe(output, tool, tier)
                    except Exception as error:
                        result = dict(tool=tool, tier=tier, location=location, checks=[dict(name='rig', status='FAIL', actual=str(error), target='successful probe')])
                        write_json(output / 'error.json', result)
                    results.append(result)
                    print(tool, location, tier, [(c['name'], c['status']) for c in result['checks']], flush=True)
    if args.group in ('all', 'failures'):
        from failures import run_failures
        results.extend(run_failures(Path(args.output) / 'failures'))
        from scenarios import run_scenarios
        results.extend(run_scenarios(Path(args.output) / 'scenarios'))
    write_json(Path(args.output) / 'results.json', results)
    counts = {status: sum(c['status'] == status for r in results for c in r['checks']) for status in ('PASS', 'XFAIL', 'FAIL', 'BLOCKED')}
    summary = '# Comms matrix observations\n\n' + '\n'.join(f'* {key}: {value}' for key, value in counts.items()) + '\n\nXFAIL is an unmet release target. Green CI only means the checked baseline or target matched.\n'
    (Path(args.output) / 'summary.md').write_text(summary)
    print(summary, flush=True)
    sys.exit(any(c['status'] in ('FAIL', 'BLOCKED') for r in results for c in r['checks']))


if __name__ == '__main__':
    main()
