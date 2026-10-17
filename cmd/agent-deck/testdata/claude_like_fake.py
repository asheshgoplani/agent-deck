#!/usr/bin/env python3
# Claude-composer-like fake from the macOS app audit (F1-DECIDER run3,
# stage/claude_like), without its stdin logger. Used by
# busy_composer_guarded_send_test.go. Behaviour notes from the original:
# Differences from buildmac-audit.py FAKE_AGENT, each mirroring Claude Code's Ink TUI:
#  1. raw tty (no ICRNL/ICANON/ECHO/ISIG), so Enter arrives as b'\r' and the kernel never echoes;
#  2. a framed composer (divider, '❯ <draft>', divider, footer) drawn in idle AND busy screens;
#  3. typed bytes go into the composer draft and are redrawn there; b'\r' submits and CLEARS the draft;
#  4. idle submit appends a main-chain user record (what session.TurnAdvanced looks for);
#     busy submit queues: queue-operation enqueue, composer shows 'Press up to edit queued messages',
#     then after 1s remove(absorbed_mid_turn) + queued_command attachment (unchanged mailbox receipt).
import datetime, json, os, re, select, sys, termios, time, uuid
here = os.path.dirname(os.path.abspath(__file__))
path = json.load(open(os.path.join(here, 'fake-running.json')))[os.path.realpath(os.getcwd())]
mailbox = path + '.midturn.json'
control_path = path + '.input-control.json'
state_path = path + '.input-state.json'
session_id = os.path.basename(path)[:-6]
attrs = termios.tcgetattr(0)
attrs[0] &= ~(termios.ICRNL | termios.INLCR | termios.IGNCR | termios.IXON)
attrs[3] &= ~(termios.ICANON | termios.ECHO | termios.ISIG | termios.IEXTEN)
attrs[6][termios.VMIN], attrs[6][termios.VTIME] = 1, 0
termios.tcsetattr(0, termios.TCSANOW, attrs)
sys.stdout.write('\033[?2004h'); sys.stdout.flush()  # Claude enables bracketed paste
phase, phase_id, draft, queued, pending, submits = None, None, '', [], [], 0
started = time.monotonic()
rendered_rows = 0
def now(): return datetime.datetime.now(datetime.timezone.utc).isoformat().replace('+00:00', 'Z')
def append(*rows):
    with open(path, 'a') as out: out.write(''.join(json.dumps(r) + '\n' for r in rows))
def screen():
    try: cols = os.get_terminal_size(sys.stdout.fileno()).columns
    except OSError: cols = 80
    div = '─' * cols
    if phase in ('busy', 'stream'):
        head = ['⏺ Running the lanes.', '', '✽ Thundering… (%ds · ↓ 135 tokens)' % int(time.monotonic() - started), '']
    else:
        head = ['⏺ All four steps ran.', '', '✻ Sautéed for 45s · done 10:44 AM', '']
    body = draft if draft else ('Press up to edit queued messages' if queued else '')
    return head + [div, '❯ ' + body, div, '  ⏵⏵ bypass permissions on (shift+tab to cycle)']
def render():
    global rendered_rows
    lines = screen()
    for i in range(max(rendered_rows, len(lines))):
        sys.stdout.write(f'\033[{i + 1};1H\033[2K' + (lines[i] if i < len(lines) else ''))
    row = lines.index(next(l for l in lines if l.startswith('❯ '))) + 1
    sys.stdout.write(f'\033[{row};{3 + len(draft) + 1}H'); sys.stdout.flush()
    rendered_rows = len(lines)
def receipt(source, records, kind):
    with open(mailbox + '.receipt.json', 'w') as out:
        json.dump({'request_id': source, 'records': records, 'transport': 'stdin', 'kind': kind,
                   'submits': submits, 'actual_input': records[0].get('content') or records[0]['message']['content']}, out)
def request_id():
    try: return json.load(open(mailbox)).get('id')
    except (OSError, ValueError): return None
def submit(text):
    global submits
    submits += 1
    rid = request_id()
    if phase in ('busy', 'stream'):
        enq = {'type': 'queue-operation', 'operation': 'enqueue', 'timestamp': now(), 'content': text, 'sessionId': session_id}
        append(enq); queued.append(text); pending.append((time.monotonic() + 1.0, rid, enq))
    else:
        rec = {'type': 'user', 'uuid': str(uuid.uuid4()), 'timestamp': now(), 'sessionId': session_id,
               'message': {'role': 'user', 'content': text}}
        append(rec)
        if rid and rid in text: receipt(rid, [rec], 'turn')
def absorb():
    for item in list(pending):
        due, rid, enq = item
        if time.monotonic() < due: continue
        src = rid or str(uuid.uuid4())
        removed = {'type': 'queue-operation', 'operation': 'remove', 'reason': 'absorbed_mid_turn', 'timestamp': now(),
                   'commandUuid': src, 'content': enq['content'], 'sessionId': session_id}
        att = {'type': 'attachment', 'uuid': src + '-attachment', 'timestamp': now(), 'sessionId': session_id,
               'renderedRole': 'system', 'rendered': [{'type': 'text', 'content': '<system-reminder>' + enq['content'] + '</system-reminder>'}],
               'attachment': {'type': 'queued_command', 'prompt': enq['content'], 'source_uuid': src, 'commandMode': 'prompt',
                              'origin': {'kind': 'human'}, 'timestamp': now(), 'humanTurn': True}}
        append(removed, att)
        if rid and rid in enq['content']: receipt(rid, [enq, removed, att], 'queued')
        pending.remove(item); queued.remove(enq['content']); render()
def poll_phase():
    global phase, phase_id
    try: c = json.load(open(control_path))
    except (OSError, ValueError): return
    if (c.get('id'), c.get('phase')) != (phase_id, phase):
        phase_id, phase = c.get('id'), c.get('phase')
        append({'type': 'system', 'subtype': 'turn_duration', 'uuid': 'fixture-idle-' + str(phase_id), 'timestamp': now(), 'durationMs': 1000}
               if phase == 'idle' else
               {'type': 'user', 'uuid': 'fixture-busy-' + str(phase_id), 'timestamp': now(),
                'message': {'role': 'user', 'content': 'Owned fixture work before queued input.'}})
        render()
    tmp = state_path + '.tmp'
    with open(tmp, 'w') as out: json.dump({'id': phase_id, 'phase': phase, 'busy_bytes': 0, 'submits': submits, 'draft': draft}, out)
    os.replace(tmp, state_path)
ESC_RE = re.compile(rb'\x1b\[[0-9;?]*[A-Za-z~]')
render()
last_tick = 0
while True:
    poll_phase(); absorb()
    if phase in ('busy', 'stream') and time.monotonic() - last_tick > 1: render(); last_tick = time.monotonic()
    if not select.select([0], [], [], 0.05)[0]: continue
    chunk = ESC_RE.sub(b'', os.read(0, 65536))  # drops bracketed-paste 200~/201~ framing
    changed = False
    for ch in chunk.decode('utf-8', errors='replace'):
        if ch == '\r':
            if draft.strip(): submit(draft)
            draft = ''; changed = True
        elif ch == '\n': draft += '\n'; changed = True   # Claude: LF (ctrl+J) inserts a newline, it does not submit
        elif ch in '\x7f\b': draft = draft[:-1]; changed = True
        elif ch == '\x03': draft = ''; changed = True
        elif ch >= ' ': draft += ch; changed = True
    if changed: render()
