"""Fault injection through public commands and disposable OS resources.

Expected failures require an observed, enumerated missing capability or exact
baseline result. The five-second queue probe does not claim deadline exhaustion.
"""
import concurrent.futures
import datetime
import json
import os
from pathlib import Path
import signal
import shlex
import uuid
import subprocess
import time

from runner import BINARY, Rig, check, write_json



def turns(rig):
    records, _ = rig.records()
    return [record for record in records
            if record.get('kind') == 'turn' and record.get('from') == rig.child]


def daemon(rig):
    log = open(rig.output / ('daemon-' + str(len(rig.processes)) + '.log'), 'w')
    process = subprocess.Popen([BINARY, '-p', 'default', 'notify-daemon'],
                               env=rig.env, stdout=log, stderr=subprocess.STDOUT)
    log.close()
    rig.processes.append(process)
    return process


def wait_for(predicate, seconds=10):
    deadline = time.monotonic() + seconds
    while time.monotonic() < deadline:
        value = predicate()
        if value:
            return value
        time.sleep(.15)
    return None


def committed_count(rig, expected):
    try:
        return len(turns(rig)) >= expected
    except RuntimeError as error:
        if 'no ledger for this profile yet' in str(error):
            return False
        raise


def stop(process):
    if process.poll() is None:
        process.send_signal(signal.SIGTERM)
        process.wait(timeout=10)


def daemon_restart(rig):
    rig.setup('claude', 'blocked')
    rig.emit('claude', 'info', turn='before-restart', text='Historical result before restart.')
    # Transcript timestamps are producer fixtures, not agent-deck state. Age
    # this recorded input before the first daemon ever observes the turn.
    historical = (datetime.datetime.now(datetime.timezone.utc) - datetime.timedelta(hours=9)).isoformat()
    for path in (rig.root / '.claude/projects/matrix').glob('*.jsonl'):
        frames = [json.loads(line) for line in path.read_text().splitlines()]
        for frame in frames:
            frame['timestamp'] = historical
        path.write_text(''.join(json.dumps(frame) + '\n' for frame in frames))
    rig.tick()
    initial = turns(rig)
    if len(initial) != 1:
        raise RuntimeError('restart precondition: expected exactly one committed initial turn')
    process = daemon(rig)
    for number in range(4):
        rig.emit('claude', 'info', turn=f'burst-{number}', text=f'Burst result {number}.')
    stop(process)
    for number in range(4, 8):
        rig.emit('claude', 'info', turn=f'burst-{number}', text=f'Burst result {number}.')
    process = daemon(rig)
    wait_for(lambda: len(turns(rig)) >= 9, 15)
    stop(process)
    records = turns(rig)
    keys = [row['key'] for row in records]
    return [check('restart_preserves_all_burst_turns', len(records), 9),
            check('restart_reemits_committed_turn', keys.count(initial[0]['key']), 1),
            check('restart_duplicate_keys', len(keys) - len(set(keys)), 0)], {
                'records': records, 'historical_transcript_timestamp': historical}


def parent_superseded(rig):
    rig.setup('claude', 'blocked')
    rig.emit('claude', 'info', turn='source-parent-history', identity=rig.parent,
             text='Parent context for the disposable cross-harness switch.')
    response = rig.cli('session', 'switch', '--json', '--to-harness', 'codex',
                       '--confirm-context-loss', rig.parent, ok=False, timeout=45)
    try:
        receipt = json.loads(response.stdout)
    except json.JSONDecodeError:
        receipt = {'stdout': response.stdout, 'stderr': response.stderr}
    target = receipt.get('target_id', '')
    if target:
        rig.sessions.append(target)
    if not receipt.get('source_archived') or not receipt.get('source_superseded_by'):
        known_absence = ('switch is plan-only for claude -> codex: target lifecycle, fresh identity, '
                         'readiness verification, and registry commit are not implemented')
        actual = receipt.get('error', receipt)
        return [check('supersession_precondition', actual,
                      'public session switch verifies target ready and supersedes source',
                      [known_absence])], {'switch': receipt}
    old = rig.parent
    rig.emit('claude', 'info', turn='after-supersession', text='Completion after parent supersession.')
    rig.tick()
    records = turns(rig)
    destinations = [row.get('to') for row in records]
    peek = rig.cli('inbox', 'peek', '--json', old, ok=False)
    return [check('superseded_parent_destination', destinations, [[target]]),
            check('old_parent_has_no_new_text', 'Completion after parent supersession.' in peek.stdout, False)], {
                'switch': receipt, 'records': records, 'old_inbox': peek.stdout}


def parent_removed(rig):
    rig.setup('claude', 'blocked')
    old_parent = rig.parent
    rig.cli('remove', '--json', old_parent)
    rig.emit('claude', 'info', turn='orphan-turn', text='Orphan completion must remain visible.')
    rig.tick()
    rig.tick()
    records = turns(rig)
    export = rig.cli('inbox', 'export', '--json')
    exported = json.loads(export.stdout)
    items = exported if isinstance(exported, list) else exported.get('records', [])
    surfaced = [row for row in items if row.get('child_session_id') == rig.child
                and row.get('target_kind') == 'unowned'
                and row.get('text') == 'Orphan completion must remain visible.']
    first = rig.cli('inbox', 'drain', '--json', '_unowned', ok=False)
    second = rig.cli('inbox', 'drain', '--json', '_unowned', ok=False)
    drains = [first, second]
    missing = all(response.returncode != 0 and '_unowned' in response.stderr + response.stdout
                  and 'could not be resolved' in response.stderr + response.stdout for response in drains)
    if missing:
        owner_check = check('unowned_owner_ack_and_archive', 'unowned target cannot be resolved',
                            [1, 0], ['unowned target cannot be resolved'])
    elif all(response.returncode == 0 for response in drains):
        counts = []
        for response in drains:
            data = json.loads(response.stdout)
            data = data if isinstance(data, list) else data.get('events', data.get('records', []))
            counts.append(sum(row.get('child_session_id') == rig.child for row in data))
        owner_check = check('unowned_owner_ack_and_archive', counts, [1, 0], [[0, 0]])
    else:
        owner_check = check('unowned_owner_ack_and_archive',
                            [response.returncode for response in drains], [0, 0])
    return [check('removed_parent_one_turn', len(records), 1),
            check('removed_parent_routes_unowned', [row.get('to') for row in records], [['_unowned']], [[[old_parent]]]),
            check('unowned_visible_in_public_export', len(surfaced), 1, [0]),
            owner_check], {'records': records, 'inbox_export': exported,
                          'owner_drains': [{'code': result.returncode, 'stdout': result.stdout,
                                            'stderr': result.stderr} for result in drains]}


def remote_unreachable(rig):
    rig.setup('claude', 'blocked')
    # Bootstrap a distinct remote profile through the public CLI, never the
    # remote container's default HOME. The launcher preserves that exact env.
    remote_root = '/tmp/comms-unreachable-' + uuid.uuid4().hex
    host = os.environ.get('COMMS_REMOTE', 'r1')
    bootstrap = ("import sys; sys.path.insert(0, '/workspace/tests/comms_matrix'); "
                 "from runner import Rig; r=Rig(" + repr(remote_root + '/evidence') + ", " + repr(remote_root) + "); "
                 "r.cli('add', '--no-parent', '--no-identity', '-t', 'failure-bootstrap', "
                 "'-c', 'shell', str(r.root / 'work')); r.tick(); r.close()")
    rig.run(['ssh', host, shlex.join(['python3', '-c', bootstrap])], timeout=20)
    slow_path = remote_root + '/slow-agent-deck'
    slow_script = '#!/bin/sh\ncase "$*" in *inbox*export*) sleep 5; exit 1;; *) exec ' + shlex.quote(remote_root + '/agent-deck-env') + ' "$@";; esac\n'
    install = "from pathlib import Path; p=Path(" + repr(slow_path) + "); p.write_text(" + repr(slow_script) + "); p.chmod(0o755)"
    rig.run(['ssh', host, shlex.join(['python3', '-c', install])])
    with rig.config.open('a') as config:
        config.write('\n[remotes.dead]\nhost = ' + json.dumps(host) + '\nagent_deck_path = ' + json.dumps(slow_path) + '\ncommand_timeout_seconds = 3\n')
        config.write('\n[remotes.r1]\nhost = ' + json.dumps(host) + '\nagent_deck_path = '
                     + json.dumps(remote_root + '/agent-deck-env') + '\ncommand_timeout_seconds = 3\n')
    started = time.monotonic()
    with concurrent.futures.ThreadPoolExecutor(max_workers=2) as pool:
        pending = {name: pool.submit(rig.cli, 'remote', 'drain', name, '--into', rig.parent,
                                     '--json', ok=False, timeout=8) for name in ('dead', 'r1')}
        responses = {name: future.result() for name, future in pending.items()}
    elapsed = time.monotonic() - started
    health = rig.cli('health', '--json', '--since', '1h', ok=False)
    # Preserve raw health and log rows for dated failure evidence. No status
    # is invented from a failed subprocess alone.
    dated = []
    for path in (rig.root / 'data').rglob('*.jsonl'):
        if 'health' not in path.parts:
            continue
        for line in path.read_text(errors='replace').splitlines():
            if 'dead' in line and any(word in line.lower() for word in ('failed', 'error', 'unreachable')):
                try:
                    row = json.loads(line)
                except json.JSONDecodeError:
                    continue
                if any(row.get(key) for key in ('time', 'ts', 'timestamp')):
                    dated.append(row)
    return [check('unreachable_remote_errors', responses['dead'].returncode != 0, True),
            check('other_remote_answers', responses['r1'].returncode, 0),
            check('parallel_cli_requests_bounded_one_timeout', elapsed <= 4, True),
            check('dated_remote_health_failure', bool(dated), True, [False])], {
                'elapsed_seconds': elapsed, 'timeout_seconds': 3, 'dated_errors': dated,
                'health': health.stdout, 'remote_results': {name: {'code': value.returncode,
                    'stdout': value.stdout, 'stderr': value.stderr} for name, value in responses.items()}}


def log_unlinked(rig):
    rig.setup('claude', 'blocked')
    rig.env['AGENTDECK_DEBUG'] = '1'
    process = daemon(rig)
    def open_log():
        for fd in Path(f'/proc/{process.pid}/fd').glob('*'):
            try:
                target = Path(os.readlink(fd))
            except FileNotFoundError:
                continue
            if target.name == 'debug.log' and target.is_relative_to(rig.root) and target.exists():
                return target
        return None
    path = wait_for(open_log, 5)
    if path is None:
        raise RuntimeError('daemon never opened a private debug log descriptor before unlink')
    before = path.stat().st_ino
    # Fault injection is deliberately scoped to a file under the private rig.
    # Renaming would not reproduce an unlinked, still-open inode.
    path.unlink()
    rig.emit('claude', 'info', turn='after-unlink', text='Turn after private log inode unlink.')
    wait_for(lambda: committed_count(rig, 1), 10)
    recreated = wait_for(lambda: path.exists() and path.stat().st_size > 0, 4)
    deleted = []
    for fd in Path(f'/proc/{process.pid}/fd').glob('*'):
        try:
            link = os.readlink(fd)
        except FileNotFoundError:
            continue
        if str(path) in link and '(deleted)' in link:
            deleted.append(link)
    stop(process)
    return [check('log_recreated_after_unlink', bool(recreated), True, [False]),
            check('no_deleted_log_descriptor', bool(deleted), False, [True]),
            check('turn_survives_log_unlink', len(turns(rig)), 1)], {
                'log': str(path), 'original_inode': before,
                'new_inode': path.stat().st_ino if path.exists() else None, 'deleted_fds': deleted}


def overflow(rig):
    rig.setup('claude', 'blocked')
    count = 65
    markers = [f'Overflow probe unique turn {number:02d} completed.' for number in range(count)]
    for number, marker in enumerate(markers):
        rig.emit('claude', 'info', turn=f'overflow-{number:02d}', text=marker)
        rig.tick()
    records = turns(rig)
    drain = rig.cli('inbox', 'drain', '--json', rig.parent)
    delivered = sum(marker in drain.stdout for marker in markers)
    dead = rig.cli('inbox', 'dead-letter', 'list', '--json', ok=False)
    return [check('overflow_ledger_preserves_65', len(records), count),
            check('overflow_digest_preserves_all_turns', delivered, count, [64]),
            check('overflow_no_failed_deadletters', 'inbox pending-turn limit reached' in dead.stdout, False, [True])], {
                'records': records, 'drained': json.loads(drain.stdout),
                'deadletters': dead.stdout, 'distinct_delivered_markers': delivered}


def composer_blocked(rig):
    rig.setup('codex', 'blocked')
    before = rig.capture(rig.parent)
    started = time.monotonic()
    response = rig.cli('session', 'send', '--json', '--defer-if-busy', '--defer-timeout', '2s',
                       '--timeout', '2s', rig.parent, 'Blocked composer bounded probe.', ok=False, timeout=8)
    elapsed = time.monotonic() - started
    typed = rig.capture(rig.parent)[len(before):]
    rig.env['AGENTDECK_SEND_LAND_WINDOW'] = '1s'  # Bound cleanup watcher only, not retry deadline.
    queued = json.loads(rig.cli('session', 'send', '--queue', '--json', rig.parent,
                                'Durable blocked-composer probe.').stdout)
    send_id = queued['send_id']
    samples = []
    observation_start = time.monotonic()
    while time.monotonic() - observation_start < 5:
        snapshot = json.loads(rig.cli('session', 'send-status', '--json', send_id).stdout)
        samples.append(snapshot)
        if snapshot.get('state') in ('failed', 'landed') or snapshot.get('settled'):
            break
        time.sleep(.2)
    observed_seconds = time.monotonic() - observation_start
    last = samples[-1]
    attempts = max(sample.get('attempts', 0) for sample in samples)
    queued_typed = rig.capture(rig.parent)[len(before):]
    # The CI target is explicitly the five-second progress window. An XFAIL
    # here is not evidence that the hard-coded 30-minute deadline was tested.
    terminal = check('durable_queue_failed_in_ci_window', last.get('state'), 'failed', ['queued'])
    checks = [check('blocked_composer_types_nothing', queued_typed.hex(), ''),
              check('blocked_sender_gets_error', response.returncode != 0, True),
              check('blocked_return_within_budget', elapsed < 5, True),
              check('durable_queue_attempts_below_18_in_ci_window', attempts < 18, True), terminal]
    # Removal is cleanup only, not a passing retry-exhaustion event. It makes
    # the owned detached worker stop retrying and its bounded watcher settle.
    rig.cli('remove', '--json', rig.parent)
    cleanup_status = None
    def cleaned():
        nonlocal cleanup_status
        cleanup_status = json.loads(rig.cli('session', 'send-status', '--json', send_id).stdout)
        return cleanup_status.get('state') in ('failed', 'landed') or cleanup_status.get('settled')
    if not wait_for(cleaned, 8):
        raise RuntimeError('owned queue worker did not terminate after target cleanup')
    def owned_queue_workers():
        workers = []
        expected_home = ('HOME=' + str(rig.root)).encode()
        for proc in Path('/proc').iterdir():
            if not proc.name.isdigit():
                continue
            try:
                environment = (proc / 'environ').read_bytes().split(b'\0')
                command = (proc / 'cmdline').read_bytes().split(b'\0')
            except (FileNotFoundError, PermissionError, ProcessLookupError):
                continue
            if expected_home in environment and (b'send-worker' in command or b'--queue-worker' in command):
                workers.append(int(proc.name))
        return workers
    if not wait_for(lambda: not owned_queue_workers(), 5):
        raise RuntimeError('owned queue worker remains after terminal cleanup: ' + str(owned_queue_workers()))
    checks.append(check('queue_cleanup_terminal', bool(cleanup_status.get('state') in ('failed', 'landed') or cleanup_status.get('settled')), True))
    checks.append(check('queue_cleanup_owned_workers', owned_queue_workers(), []))
    return checks, {'elapsed_seconds': elapsed, 'stdout': response.stdout, 'stderr': response.stderr,
                    'typed_hex': queued_typed.hex(), 'queue_samples': samples,
                    'ci_observation_seconds': observed_seconds,
                    'retry_deadline_coverage': 'five-second progress only; 30-minute exhaustion not exercised',
                    'cleanup_status': cleanup_status}


def approval_dialog(rig):
    rig.setup('claude', 'approval')
    before = rig.capture(rig.parent)
    response = rig.cli('session', 'send', '--json', '--defer-if-busy', '--defer-timeout', '2s',
                       '--timeout', '2s', rig.parent, 'Do not approve this tool call.', ok=False, timeout=8)
    typed = rig.capture(rig.parent)[len(before):]
    rig.emit('claude', 'urgent', turn='approval-nudge', text='===AGENTDECK_DONE=== status=ok summary=approval-probe')
    rig.tick()
    all_typed = rig.capture(rig.parent)[len(before):]
    return [check('approval_direct_send_no_enter', typed.count(b'\r') + typed.count(b'\n'), 0),
            check('approval_daemon_wake_no_enter', all_typed.count(b'\r') + all_typed.count(b'\n'), 0),
            check('approval_types_nothing', all_typed.hex(), '')], {
                'direct_send_code': response.returncode, 'stdout': response.stdout,
                'stderr': response.stderr, 'typed_hex': all_typed.hex()}


PROBES = {
    'daemon-restart-mid-burst': daemon_restart,
    'parent-superseded': parent_superseded,
    'parent-removed': parent_removed,
    'remote-unreachable': remote_unreachable,
    'log-unlinked': log_unlinked,
    '64-turn-overflow': overflow,
    'composer-blocked': composer_blocked,
    'approval-dialog': approval_dialog,
}


def run_failures(output):
    if not Path('/.dockerenv').exists():
        raise RuntimeError('fault probes run only inside disposable Docker containers')
    results = []
    for name, probe in PROBES.items():
        directory = Path(output) / name
        rig = Rig(directory)
        try:
            checks, evidence = probe(rig)
            result = dict(failure=name, checks=checks, evidence=evidence)
        except Exception as error:
            result = dict(failure=name, checks=[dict(name='probe', status='FAIL', actual=str(error),
                                                     target='established fixture and observed failure-mode contract')])
        finally:
            rig.close()
        write_json(directory / 'result.json', result)
        results.append(result)
        print(name, [(item['name'], item['status']) for item in result['checks']], flush=True)
    write_json(Path(output) / 'failures.json', results)
    return results
