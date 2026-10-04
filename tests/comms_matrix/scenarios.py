"""Focused local comms scenarios using production commands and producer inputs.

Remote sibling routing and remote subagent/flap scenarios are not exercised by
this module. Generic remote matrix coverage does not substitute for those cases.
"""
import hashlib
import json
from pathlib import Path
import uuid

from runner import Rig, check, write_json


def thread_id(identity):
    return '019a0000-0000-7000-8000-' + hashlib.sha256(identity.encode()).hexdigest()[:12]


def tool_turns(rig, identity):
    records, _ = rig.records()
    return records, [row for row in records if row.get('kind') == 'turn' and row.get('from') == identity]


def codex_subagent(rig):
    rig.setup('codex', 'blocked')
    text = rig.emit('codex', 'info', turn='main-before-subagent', text='Main thread result retained.')
    rig.tick()
    main = thread_id(rig.child)
    rig.hook('codex', 'UserPromptSubmit', dict(hook_event_name='UserPromptSubmit', session_id=main,
                                             turn_id='main-still-running', cwd=str(rig.root / 'work'),
                                             prompt='Main thread continues working.'), rig.child)
    before = json.loads(rig.cli('session', 'show', '--json', rig.child).stdout)
    sub = str(uuid.uuid4())
    transcript = rig.root / '.codex/sessions/2026/10/04' / ('rollout-2026-10-04T01-00-00-' + sub + '.jsonl')
    transcript.parent.mkdir(parents=True, exist_ok=True)
    # Producer rollout fixture: source and parent relationship are the actual
    # Codex session_meta shape, not a fabricated agent-deck status record.
    metadata = dict(id=sub, session_id=main, parent_thread_id=main,
                    source={'subagent': {'thread_spawn': {'parent_thread_id': main, 'depth': 1,
                                                        'agent_path': '/matrix/subagent'}}},
                    thread_source='subagent')
    transcript.write_text(json.dumps(dict(type='session_meta', payload=metadata)) + '\n')
    payload = rig.fixture('codex_notify_v1.json')
    subtext = 'Subagent completion must not become the main pane completion.'
    payload.update({'thread-id': sub, 'turn-id': 'subagent-turn', 'cwd': str(rig.root / 'work'),
                    'input-messages': ['subagent task'], 'last-assistant-message': subtext})
    rig.hook('codex', 'agent-turn-complete', payload, rig.child)
    rig.tick()
    after = json.loads(rig.cli('session', 'show', '--json', rig.child).stdout)
    records, turns = tool_turns(rig, rig.child)
    return [check('main_hook_receipt_fresh', before.get('hook_status_fresh', False), True),
            check('main_running_before_subagent', before.get('hook_status'), 'running'),
            check('main_stays_running_after_subagent', after.get('hook_status'), 'running'),
            check('subagent_does_not_create_turn', len(turns), 1),
            check('main_text_preserved', [row.get('text') for row in turns], [text]),
            check('subagent_text_absent', any(row.get('text') == subtext for row in records), False),
            check('main_transcript_identity_present', main in before.get('transcript_ids', []), True),
            check('subagent_does_not_replace_session_identity', after.get('transcript_ids'), before.get('transcript_ids'))], {
                'before': before, 'after': after, 'records': records, 'subagent_payload': payload,
                'subagent_rollout': metadata}


def codex_flapping(rig):
    rig.setup('codex', 'blocked')
    text = rig.emit('codex', 'info', turn='unchanged-turn', text='One unchanged generation result.')
    rig.tick()
    sid = thread_id(rig.child)
    notify = rig.fixture('codex_notify_v1.json')
    notify.update({'thread-id': sid, 'turn-id': 'unchanged-turn', 'cwd': str(rig.root / 'work'),
                   'input-messages': ['<task-notification>background task complete</task-notification>'],
                   'last-assistant-message': text})
    for _ in range(5):
        rig.hook('codex', 'UserPromptSubmit', dict(hook_event_name='UserPromptSubmit', session_id=sid,
                                                  turn_id='unchanged-turn', cwd=str(rig.root / 'work'),
                                                  prompt='Same generation, status replay only.'), rig.child)
        rig.tick()
        rig.hook('codex', 'agent-turn-complete', notify, rig.child)
        rig.tick()
    records, turns = tool_turns(rig, rig.child)
    return [check('flapping_one_turn', len(turns), 1),
            check('flapping_one_text', [row.get('text') for row in turns], [text]),
            check('flapping_unique_keys', len({row['key'] for row in turns}), 1)], {
                'replayed_running_waiting_pairs': 5, 'unchanged_turn_id': 'unchanged-turn', 'records': records}


def conductor_self(rig, tool):
    rig.setup(tool, 'blocked')
    before = rig.capture(rig.parent)
    rig.emit(tool, 'info', turn='conductor-self', identity=rig.parent,
             text='Internal conductor note, not addressed to the user.')
    rig.tick()
    shown = json.loads(rig.cli('session', 'show', '--json', rig.parent).stdout)
    records, turns = tool_turns(rig, rig.parent)
    exported = json.loads(rig.cli('inbox', 'export', '--json').stdout)
    rows = exported if isinstance(exported, list) else exported.get('records', [])
    journal = [row for row in rows if row.get('child_session_id') == rig.parent]
    return [check('self_hook_receipt_fresh', shown.get('hook_status_fresh', False), True),
            check('no_self_addressed_turn', sum(rig.parent in row.get('to', []) for row in turns), 0),
            check('no_unaddressed_conductor_turn', len(turns), 0, [1]),
            check('no_conductor_completion_journal', len(journal), 0),
            check('no_conductor_self_wake_bytes', rig.capture(rig.parent)[len(before):].hex(), '')], {
                'records': records, 'inbox_export': exported, 'session': shown}


def sibling_reply(rig, tool):
    rig.setup(tool, 'blocked')
    conductor = rig.parent
    receiver = rig.child
    sender = rig.add(tool, 'matrix-sibling-sender', conductor, state='blocked')
    if tool == 'codex':
        rig.emit(tool, 'info', turn='receiver-ready', identity=receiver, text='Receiver is ready for sibling task.')
        rig.tick()
        shown = json.loads(rig.cli('session', 'show', '--json', receiver).stdout)
        transcript = Path(shown['transcript_path'])
        # A native rollout records its durable turn-start identity. This is
        # producer input, and is required for the real sender acceptance gate.
        with transcript.open('a') as stream:
            stream.write(json.dumps(dict(type='event_msg', payload=dict(type='task_started', turn_id='receiver-ready'))) + '\n')
    prior_records, _ = rig.records()
    prior_ids = {row['id'] for row in prior_records}
    before_parent = rig.capture(conductor)
    before_receiver = rig.capture(receiver)
    message = 'Sibling task with distinct sender and receiver.'
    response = rig.cli('session', 'send', '--json', '--no-wait', '--timeout', '2s', receiver, message,
                       env=dict(rig.env, AGENTDECK_INSTANCE_ID=sender), ok=False, timeout=15)
    # The subsequent fixture is the receiver's recorded answer to that exact
    # sender envelope. Receipt checks separately expose any failed typed send.
    rig.parent = sender
    text = rig.emit(tool, 'urgent', turn='sibling-reply', identity=receiver,
                    text='Sibling task finished with a useful result.')
    rig.parent = conductor
    rig.tick()
    records, replies = tool_turns(rig, receiver)
    replies = [row for row in replies if row['id'] not in prior_ids]
    sends = [row for row in records if row.get('kind') == 'send' and row.get('from') == sender
             and receiver in row.get('to', [])]
    valid = [row for row in sends if message in row.get('text', '')
             and row.get('th') == hashlib.sha256(row['text'].encode()).hexdigest()[:16]]
    final = [row for row in records if row.get('kind') == 'delivery' and row.get('state') in
             ('injected', 'typed', 'landed', 'failed') and any(row.get('ref') == send.get('id') for send in sends)]
    typed = rig.capture(receiver)[len(before_receiver):]
    checks = [check('sibling_send_text_and_sender_record', len(valid), 1, [0]),
              check('sibling_final_delivery_record', len(final), 1, [0]),
              check('sibling_receiver_pane_contains_task', message.encode() in typed, True),
              check('sibling_one_reply', len(replies), 1),
              check('sibling_reply_text', [row.get('text') for row in replies], [text]),
              check('sibling_reply_to_sender', [row.get('reply_to') for row in replies], [sender]),
              check('sibling_reply_addresses_parent_and_sender', [set(row.get('to', [])) == {conductor, sender} for row in replies], [True]),
              check('sibling_parent_no_wake', rig.capture(conductor)[len(before_parent):].hex(), '')]
    return checks, {'sender': sender, 'receiver': receiver, 'conductor': conductor,
                    'send_receipt': {'code': response.returncode, 'stdout': response.stdout, 'stderr': response.stderr},
                    'receiver_typed_hex': typed.hex(), 'records': records,
                    'reply_fixture_scope': 'producer-boundary reply; actual send delivery asserted separately'}


def run_scenarios(output):
    if not Path('/.dockerenv').exists():
        raise RuntimeError('scenarios run only in disposable Docker containers')
    probes = [('codex-subagent-filter', codex_subagent), ('codex-unchanged-generation-flap', codex_flapping)]
    for tool in ('claude', 'codex'):
        probes.append((tool + '-conductor-self', lambda rig, tool=tool: conductor_self(rig, tool)))
        probes.append((tool + '-sibling-reply', lambda rig, tool=tool: sibling_reply(rig, tool)))
    results = []
    for name, probe in probes:
        rig = Rig(Path(output) / name)
        try:
            checks, evidence = probe(rig)
            result = dict(scenario=name, location='local', checks=checks, evidence=evidence)
        except Exception as error:
            result = dict(scenario=name, location='local', checks=[dict(name='scenario', status='FAIL',
                          actual=str(error), target='established fixture and public observations')])
        finally:
            rig.close()
        write_json(rig.output / 'result.json', result)
        results.append(result)
        print(name, [(row['name'], row['status']) for row in result['checks']], flush=True)
    write_json(Path(output) / 'coverage.json', {
        'local': ['Codex subagent filtering', 'Codex unchanged generation flapping',
                  'Claude/Codex conductor self', 'Claude/Codex sibling send and reply boundaries'],
        'not_exercised': ['remote variants of these focused scenarios', 'other harness conductor self',
                          'orphan integrator hierarchy', 'plain-shell and Codex-error status-only matrix']})
    write_json(Path(output) / 'scenarios.json', results)
    return results
