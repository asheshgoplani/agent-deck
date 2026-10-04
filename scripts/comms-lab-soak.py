#!/usr/bin/env python3
"""Opt-in real-model comms lab. No agent processes run without --run."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import shlex
import signal
import subprocess
import sys
import time


def send_receipt(send, records):
    deliveries = [record for record in records if record.get('kind') == 'delivery'
                  and send.get('id') and record.get('ref') == send['id']]
    final = deliveries[-1] if deliveries else send
    state = final.get('delivery', {}).get('state') or final.get('state')
    text = send.get('text', '')
    hash_matches = isinstance(text, str) and bool(text) and send.get('th') == hashlib.sha256(text.encode()).hexdigest()[:16]
    valid = send.get('from') and hash_matches and state in ('injected', 'typed', 'landed', 'failed')
    return {'id': send.get('id'), 'from': send.get('from'), 'text_present': bool(text),
            'text_hash': send.get('th'), 'text_hash_matches': hash_matches,
            'final_state': state, 'verdict': 'PASS' if valid else 'HOLD'}

HARNESS = ('claude', 'codex', 'gemini', 'pi', 'hermes', 'opencode', 'cursor')
TARGETS = {
    'machine_wakes_per_parent_hour': '<= 4',
    'records_with_text_percent': '>= 95',
    'records_per_finished_event': '<= 3',
    'duplicate_turn_journal_percent': '= 0',
    'conductor_output_calls_per_wake': '= 0',
    'conductor_inbox_drain_calls_per_wake': '= 0',
    'send_records_with_sender_and_text_percent': '= 100',
    'cross_host_records_with_latency': '> 0',
    'remote_notifier_cpu_minutes_per_day': '< 10',
    'tokens_per_wake': 'report observed numerator and denominator',
}
# This helper executes identically on each host. No ambient HOME, XDG, agent
# identity, Telegram credentials, startup files or default tmux socket survive.
HELPER = r'''
import json, os, pathlib, subprocess, sys
p = json.load(sys.stdin)
r = pathlib.Path(p['root'])
assert r.is_absolute() and r.name.startswith('comms-lab-')
assert r.resolve() == r and (r / 'LAB-ONLY').is_file()
assert (r / 'LAB-ONLY').read_text().strip() == 'disposable-comms-lab'
for name in ('home', 'deck', 'config', 'cache', 'data', 'state', 'runtime', 'tmp', 'work'):
    assert (r/name).resolve() == r/name, 'lab paths must not contain symlinks'
env = {'HOME': str(r/'home'), 'PATH': str(r/'bin') + ':' + p['path'], 'TERM': 'xterm-256color',
       'SHELL': '/bin/sh', 'XDG_CONFIG_HOME': str(r/'config'),
       'XDG_CACHE_HOME': str(r/'cache'), 'XDG_DATA_HOME': str(r/'data'),
       'XDG_STATE_HOME': str(r/'state'), 'XDG_RUNTIME_DIR': str(r/'runtime'),
       'TMPDIR': str(r/'tmp'), 'TMUX_TMPDIR': str(r/'tmp'),
       'AGENT_DECK_HOME': str(r/'deck'), 'AGENT_DECK_TEST_HOME_ISOLATED': '1',
       'AGENTDECK_SKIP_UPDATE_CHECK': '1', 'AGENTDECK_TELEMETRY': '0',
       'AGENTDECK_DEBUG': '1'}
if p.get('sender'):
    env['AGENTDECK_INSTANCE_ID'] = p['sender']
if p.get('prepare'):
    assert (r/'home').is_dir(), 'provision a dedicated authenticated lab HOME first'
    assert not (r/'home'/'.agent-deck').exists(), 'existing agent-deck state forbidden'
    assert not (r/'agent-deck').exists(), 'root already used'
    for child in r.rglob('*'):
        assert not child.is_symlink(), 'lab auth must not symlink live config'
    for name in ('deck', 'config', 'cache', 'data', 'state', 'runtime', 'tmp', 'work', 'bin'):
        (r/name).mkdir(mode=0o700, exist_ok=False)
    (r/'config'/'agent-deck').mkdir(mode=0o700)
    (r/'config'/'agent-deck'/'config.toml').write_text(p['config'])
    (r/'agent-deck').write_text('#!/bin/sh\nexec env -i ' + ' '.join(
        __import__('shlex').quote(k+'='+v) for k,v in env.items()) + ' ' +
        __import__('shlex').quote(p['binary']) + ' "$@"\n')
    (r/'agent-deck').chmod(0o700)
    (r/'bin'/'agent-deck').write_text('#!/bin/sh\nexec ' +
        __import__('shlex').quote(p['binary']) + ' "$@"\n')
    (r/'bin'/'agent-deck').chmod(0o700)
    print('{}')
elif p.get('tokens'):
    import re
    sid = p['tokens']
    assert re.fullmatch(r'[A-Za-z0-9-]+', sid)
    messages, files = {}, []
    for path in (r/'home'/'.claude'/'projects').rglob(sid + '.jsonl'):
        assert path.resolve().is_relative_to(r/'home')
        files.append(str(path))
        for line in path.read_text().splitlines():
            try:
                row = json.loads(line)
            except ValueError:
                continue
            message = row.get('message', {})
            if row.get('type') == 'assistant' and message.get('id') and message.get('usage'):
                messages[message['id']] = message['usage']
    fields = ('input_tokens', 'output_tokens', 'cache_read_input_tokens', 'cache_creation_input_tokens')
    totals = {k: sum(v.get(k, 0) for v in messages.values()) for k in fields} if messages else None
    print(json.dumps({'exit': 0, 'stdout': json.dumps({'files': files, 'messages_with_usage': len(messages),
          'tokens': totals, 'basis': 'native Claude assistant usage, dedup by message id'}), 'stderr': ''}))
else:
    result = subprocess.run(p['command'], env=env, cwd=r/'work',
                            capture_output=True, text=True, timeout=p.get('timeout', 120))
    print(json.dumps({'exit': result.returncode, 'stdout': result.stdout,
                      'stderr': result.stderr}))
'''


class Lab:
    def __init__(self, args):
        self.args = args
        self.receipts = []
        self.prepared = []
        self.children = []
        self.started = None
        self.parent = None
        self.token_receipt = None

    def host(self, remote=False):
        return self.args.remote_root if remote else self.args.root

    def call(self, command, remote=False, required=True, **extra):
        payload = dict(root=self.host(remote), path=self.args.tool_path,
                       command=command, **extra)
        argv = [sys.executable, '-c', HELPER]
        if remote:
            argv = ['ssh', '-F', self.args.ssh_config, '-o', 'BatchMode=yes',
                    '-o', 'ConnectTimeout=10', '-o', 'IdentityAgent=none',
                    '-o', 'IdentitiesOnly=yes', '-o', 'StrictHostKeyChecking=yes',
                    '-o', 'ControlMaster=no', '-o', 'ControlPath=none', self.args.remote,
                    shlex.join(['python3', '-c', HELPER])]
        result = subprocess.run(argv, input=json.dumps(payload), text=True,
                                capture_output=True, timeout=150)
        receipt = dict(host='remote' if remote else 'local', command=command,
                       timestamp=time.time(), transport_exit=result.returncode)
        try:
            receipt.update(json.loads(result.stdout))
        except json.JSONDecodeError:
            receipt.update(exit=-1, stdout=result.stdout, stderr=result.stderr)
        self.receipts.append(receipt)
        self.save()
        if required and (result.returncode or receipt.get('exit', 0)):
            raise RuntimeError(f"{receipt['host']} command failed: {command}; see receipts.json")
        return receipt

    def deck(self, *args, remote=False, required=True, sender=None):
        binary = self.args.remote_binary if remote else self.args.binary
        return self.call([binary, *args], remote, required, sender=sender)

    def save(self):
        Path(self.args.root, 'receipts.json').write_text(json.dumps(self.receipts, indent=2))

    def prepare(self, remote):
        config = '[comms]\nledger = true\n[tmux]\nsocket_name = "comms-lab"\n[updates]\nauto_install = false\nmanage_timer = false\n'
        if not remote:
            config += ('[remotes.r1]\nhost = ' + json.dumps(self.args.remote) +
                       '\nagent_deck_path = ' + json.dumps(self.args.remote_root + '/agent-deck') + '\n')
        self.call([], remote, prepare=True, config=config,
                  binary=self.args.remote_binary if remote else self.args.binary)
        self.prepared.append(remote)
        # Keep this owned socket alive while individual harnesses start/exit.
        self.call(['tmux', '-L', 'comms-lab', 'new-session', '-d', '-s', 'lab-keeper'], remote)
        self.call(['tmux', '-L', 'comms-lab', 'new-session', '-d', '-s', 'lab-notifier',
                   shlex.join([self.host(remote) + '/agent-deck', 'notify-daemon'])], remote)

    def launch(self, title, tool, remote=False, parent=None):
        args = ['launch', '--json', '-t', title, '-c', tool]
        args += ['--parent', parent] if parent else ['--no-parent']
        args += [self.host(remote) + '/work']
        receipt = self.deck(*args, remote=remote)
        data = json.loads(receipt['stdout'])
        sid = data.get('session_id') or data.get('id')
        if not sid:
            raise RuntimeError('launch returned no session id')
        return sid

    def snapshot(self):
        for remote in (False, True):
            for command in [('msg', 'export', '--json'), ('inbox', 'stats', '--json', '--all'),
                            ('health', '--json'), ('list', '--json'),
                            ('recall', 'search', 'COMMS_LAB', '--json'),
                            ('session', 'metrics', '--json', '--all')]:
                self.deck(*command, remote=remote, required=False)
        self.deck('remote', 'drain', 'r1', '--into', self.parent, '--json', required=False)

    def cleanup(self):
        # Never kill-server, process-match, or address a live/default socket.
        # Enumerate exact pane IDs only within this run's private TMUX_TMPDIR.
        for remote in reversed(self.prepared):
            try:
                result = self.call(['tmux', '-L', 'comms-lab', 'list-panes', '-a',
                                    '-F', '#{pane_id}'], remote, required=False)
                for pane in result.get('stdout', '').splitlines():
                    if re.fullmatch(r'%\d+', pane):
                        self.call(['tmux', '-L', 'comms-lab', 'kill-pane', '-t', pane],
                                  remote, required=False)
            except Exception as exc:
                self.receipts.append({'cleanup_error': str(exc), 'remote': remote})
        self.save()

    def run(self):
        self.prepare(True)
        self.prepare(False)
        # A title-recognized conductor role, deliberately no `conductor setup`:
        # setup may install a machine-wide launchd service even with a new HOME.
        self.parent = self.launch('conductor-comms-lab', 'claude')
        self.deck('session', 'send', self.parent,
                  'COMMS_LAB: Act as the throwaway lab conductor. Stay idle until a child '
                  'message arrives. Read injected messages, do not call session output or '
                  'inbox drain, and do not contact any human or external service. Reply '
                  'briefly to a received child result. Do not create more sessions.')
        for remote in (False, True):
            for tool in HARNESS:
                for tier in ('urgent', 'info'):
                    sid = self.launch(f'comms-lab-{tool}-{tier}', tool, remote,
                                      None if remote else self.parent)
                    self.children.append((remote, tool, tier, sid))
        self.started = time.monotonic()
        next_send = self.started
        turn = 0
        while time.monotonic() - self.started < 1800:
            if time.monotonic() >= next_send:
                turn += 1
                for remote, tool, tier, sid in self.children:
                    marker = f'COMMS_LAB {tool} {tier} turn {turn}'
                    if tier == 'urgent':
                        prompt = (marker + ': Reply with this marker and ===AGENTDECK_DONE===. '
                                  'Do not edit files, run tools, or contact external services.')
                    else:
                        prompt = (marker + ': Use your native background subagent or async task facility '
                                  'to ask a background worker to answer 2+2, then return control without '
                                  'waiting. When its native completion notification arrives, report that '
                                  'answer and this marker. Do not manufacture a task-notification payload. '
                                  'If no native background facility exists, reply UNSUPPORTED_BACKGROUND. '
                                  'Do not edit files, invoke shell commands, or contact humans.')
                    self.deck('session', 'send', sid, prompt, remote=remote, required=False,
                              sender=self.parent if not remote and tier == 'urgent' else None)
                next_send += 300
            self.snapshot()
            time.sleep(30)
        self.snapshot()
        receipt = self.deck('list', '--json')
        rows = json.loads(receipt['stdout'])
        for row in rows if isinstance(rows, list) else []:
            if row.get('id') == self.parent and row.get('claude_session_id'):
                self.token_receipt = self.call([], tokens=row['claude_session_id'], required=False)
                break

    def report(self, error=None):
        metrics = {key: {'target': value, 'observed': None, 'verdict': 'UNKNOWN'}
                   for key, value in TARGETS.items()}
        send_contracts = []
        cells = []
        exports = [r for r in self.receipts if r.get('command', [])[1:] ==
                   ['msg', 'export', '--json'] and r.get('host') == 'local' and r.get('exit') == 0]
        if exports:
            try:
                data = json.loads(exports[-1]['stdout'])
                records = data if isinstance(data, list) else data.get('records')
                if not isinstance(records, list) or not all(isinstance(r, dict) and 'kind' in r for r in records):
                    raise ValueError('unrecognized export schema')
                if records:
                    count = sum(bool(r.get('text')) for r in records)
                    metrics['records_with_text_percent'].update(numerator=count,
                        denominator=len(records), observed=100 * count / len(records))
                sends = [r for r in records if r['kind'] == 'send']
                if sends:
                    count = sum(bool(r.get('text') and r.get('from')) for r in sends)
                    metrics['send_records_with_sender_and_text_percent'].update(numerator=count,
                        denominator=len(sends), observed=100 * count / len(sends))
                send_contracts.extend(send_receipt(send, records) for send in sends)
                for remote, tool, tier, sid in self.children:
                    turns = [r for r in records if r.get('kind') == 'turn' and
                             r.get('from') in (sid, 'r1:' + sid) and bool(r.get('origin')) == remote]
                    cells.append({'host': 'remote' if remote else 'local', 'tool': tool,
                        'requested_tier': tier, 'session_id': sid, 'records': len(turns),
                        'matching_tier_records': sum(r.get('tier') == tier for r in turns),
                        'background_task_records': sum(r.get('trigger') == 'task' for r in turns),
                        'verdict': 'HOLD'})
                count = sum(bool(r.get('origin') and r.get('t_signal') and r.get('t_record')
                                 and r.get('t_seen')) for r in records)
                metrics['cross_host_records_with_latency']['observed'] = count
            except (ValueError, TypeError, AttributeError):
                pass
        report = dict(verdict='HOLD', error=error, targets=metrics,
                      elapsed_seconds=time.monotonic() - self.started if self.started else 0,
                      children=self.children, cell_observations=cells, send_contracts=send_contracts,
                      conductor_token_receipt=self.token_receipt,
                      limitations=['Background/info is attempted through native tasks; missing native support is not a passing cell. Sibling, approval and human lanes need separate lab cases.',
                                   'Remote children are unparented and drained into the local conductor: cross-host parent IDs unsupported.',
                                   'Tokens, finished events, wake counts, CPU/day and duplicate journal identity require transcript/journal review.',
                                   'CLI receipts and retained isolated HOME logs are evidence, not a PASS verdict.'])
        Path(self.args.root, 'report.json').write_text(json.dumps(report, indent=2))
        print(json.dumps(report, indent=2))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--run', action='store_true', help='Opt in to paid real-agent execution')
    for name in ('root', 'remote-root', 'remote', 'ssh-config', 'binary', 'remote-binary', 'tool-path'):
        parser.add_argument('--' + name)
    args = parser.parse_args()
    if not args.run:
        print(json.dumps({'mode': 'plan-only', 'duration_seconds': 1800,
                          'harnesses': HARNESS, 'targets': TARGETS}, indent=2))
        return 0
    if not all(getattr(args, name.replace('-', '_')) for name in
               ('root', 'remote-root', 'remote', 'ssh-config', 'binary', 'remote-binary', 'tool-path')):
        parser.error('--run requires all lab paths, remote, binaries and tool-path')
    root = Path(args.root)
    if not root.is_absolute() or root.resolve() != root or not root.name.startswith('comms-lab-'):
        parser.error('root must be a canonical absolute comms-lab-* directory')
    if Path(args.ssh_config) != root / 'home' / '.ssh' / 'config':
        parser.error('ssh-config must be <root>/home/.ssh/config for agent-deck SSH too')
    if args.remote.startswith('-') or not re.fullmatch(r'[A-Za-z0-9_.@-]+', args.remote):
        parser.error('remote must be an SSH host or user@host')
    os.umask(0o077)
    lab = Lab(args)
    error = None
    def interrupted(signum, frame):
        raise KeyboardInterrupt(f'signal {signum}')
    signal.signal(signal.SIGTERM, interrupted)
    try:
        lab.run()
    except (Exception, KeyboardInterrupt) as exc:
        error = str(exc)
    finally:
        lab.cleanup()
        lab.report(error)
    # A soak capture is never automatically the release acceptance gate.
    return 1 if error else 2


if __name__ == '__main__':
    sys.exit(main())
