#!/usr/bin/env python3
"""Stream explicit read-only sources and write reproducible window metrics."""
import argparse
import collections
import datetime as dt
import glob
import html
import json
import re
from pathlib import Path
import wakes


def timestamp(value):
    if isinstance(value, (int, float)):
        return dt.datetime.fromtimestamp(value / 1000 if value > 1e11 else value, dt.timezone.utc)
    value = value.replace('Z', '+00:00')
    value = re.sub(r'\.(\d+)(?=[+-]|$)', lambda match: '.' + (match.group(1) + '000000')[:6], value)
    return dt.datetime.fromisoformat(value)


def recognized_record(category, record):
    if category in ('bus', 'ledger', 'send_health'):
        return bool(record.get('kind'))
    if category == 'journals':
        # The caller already validated the timestamp. Missing identity affects
        # duplicate attribution, not membership in the configured journal.
        return True
    return bool(record.get('child_session_id'))


def numeric_delta(current, previous):
    return {
        key: value - previous[key]
        for key, value in current.items()
        if isinstance(value, (int, float))
        and isinstance(previous.get(key), (int, float))
    }


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--config', required=True, help='JSON with transcripts map and bus/journals/ledger/transitions/send_health/inbox_stats glob lists')
    parser.add_argument('--start', required=True)
    parser.add_argument('--end', required=True, help='Exclusive ISO timestamp with timezone')
    parser.add_argument('--out', required=True)
    parser.add_argument('--previous', help='Previous metrics.json')
    args = parser.parse_args()
    start, end = timestamp(args.start), timestamp(args.end)
    if start.tzinfo is None or end.tzinfo is None or end <= start:
        parser.error('Use timezone-aware timestamps with end after start')
    config = json.loads(Path(args.config).read_text())
    out = Path(args.out)
    out.mkdir(parents=True, exist_ok=True)
    manifest, errors = [], []
    coverage = {}

    def records(category):
        paths = sorted({p for pattern in config.get(category, []) for p in glob.glob(str(Path(pattern).expanduser()))})
        coverage[category] = {'configured': bool(config.get(category)), 'matched_files': len(paths), 'status': 'unavailable', 'parsed': 0, 'timestamped': 0, 'recognized': 0, 'first': None, 'last': None}
        for path in paths:
            stat = Path(path).stat()
            manifest.append({'category': category, 'path': path, 'bytes': stat.st_size, 'mtime_ns': stat.st_mtime_ns})
            with open(path, errors='replace') as stream:
                for line_number, line in enumerate(stream, 1):
                    try:
                        record = json.loads(line)
                        coverage[category]['parsed'] += 1
                        value = record.get('timestamp', record.get('ts'))
                        if value is None:
                            continue
                        observed = timestamp(value)
                        stamp = observed.isoformat()
                        coverage[category]['timestamped'] += 1
                        old_first, old_last = coverage[category]['first'], coverage[category]['last']
                        coverage[category]['first'] = min(old_first, stamp) if old_first else stamp
                        coverage[category]['last'] = max(old_last, stamp) if old_last else stamp
                        if not recognized_record(category, record):
                            continue
                        coverage[category]['recognized'] += 1
                        coverage[category]['status'] = 'available'
                        if start <= observed < end:
                            yield record
                    except (ValueError, TypeError, AttributeError) as error:
                        errors.append({'path': path, 'line': line_number, 'error': str(error)})

    def ratio(n, d, scale=1):
        return n / d * scale if d else None

    wakes.W0, wakes.W1 = start, end
    parents = {}
    for label, path in config.get('transcripts', {}).items():
        if not re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9_.-]*', label):
            parser.error('Transcript labels must be safe filenames containing letters, digits, dot, underscore or hyphen')
        path = str(Path(path).expanduser())
        ws, _, meta = wakes.analyze(label, path)
        counts = {key: sum(w.get(key, 0) for w in ws) for key in ('outputs', 'drains', 'tools')}
        tokens = sum(sum(w['usage'].values()) for w in ws)
        useful = sum(bool(w['sends'] or w['telegram'] or w['tasklog']) for w in ws)
        parents[label] = dict(wakes=len(ws), tokens=tokens, wakes_per_hour=ratio(len(ws), (end-start).total_seconds()/3600), useful_wakes_proxy=useful, tokens_per_useful_wake_proxy=ratio(tokens,useful), output_calls_per_wake=ratio(counts['outputs'], len(ws)), drain_calls_per_wake=ratio(counts['drains'],len(ws)), **counts)
        if not meta['source_timestamped']:
            parents[label] = {key: None for key in parents[label]}
        coverage['transcript:'+label] = {'status': 'available' if meta['source_timestamped'] else 'unavailable', 'timestamped': meta['source_timestamped'], 'parse_errors': meta['source_errors'], 'first': meta['source_first'], 'last': meta['source_last']}
        manifest.append({'category':'transcript','path':path,'bytes':Path(path).stat().st_size})
        # Private evidence keeps excerpts available for agent review, never automatic filing.
        json.dump({'wakes':ws,'meta':meta}, (out / f'wakes-{label}.json').open('w'), indent=2)
    bus = collections.Counter()
    for record in records('bus'):
        data = record.get('data') or {}
        if record.get('kind') == 'session.transition' and data.get('delivery_result') == 'committed_inbox':
            bus['committed'] += 1
            bus['text'] += bool(data.get('text'))
        if record.get('kind') == 'session.finished' and data.get('delivery_result') == 'committed_inbox':
            bus['finished'] += 1
    journal_keys = collections.Counter()
    journal_count = 0
    journal_unknown_keys = 0
    for record in records('journals'):
        journal_count += 1
        if record.get('child') and record.get('uuid'):
            journal_keys[(record['child'], record['uuid'])] += 1
        else:
            journal_unknown_keys += 1
    duplicates = sum(count-1 for count in journal_keys.values())
    ledger = collections.Counter()
    latency = []
    latency_clock_errors = 0
    remote_origins = set(config.get('remote_origins', []))
    for record in records('ledger'):
        data = record.get('data') or record
        ledger['records'] += 1
        ledger['text'] += bool(data.get('text'))
        if data.get('kind') == 'send':
            ledger['sends'] += 1
            ledger['sends_sender_text'] += bool(data.get('from') and data.get('text'))
        if data.get('origin') in remote_origins and data.get('t_seen') is not None and data.get('t_signal') is not None:
            sample = (timestamp(data['t_seen']) - timestamp(data['t_signal'])).total_seconds() * 1000
            if sample < 0:
                latency_clock_errors += 1
            else:
                latency.append(sample)
    health = collections.Counter()
    for record in records('send_health'):
        if record.get('kind') == 'send':
            health['sends'] += 1
            detail = record.get('detail') or {}
            health['sender_text'] += bool(detail.get('sender') and detail.get('text'))
    auxiliary = {name: sum(1 for _ in records(name)) for name in ('transitions', 'inboxes')}
    snapshots = []
    for pattern in config.get('inbox_stats', []):
        for path in glob.glob(str(Path(pattern).expanduser())):
            snapshots.append({'path': path, 'mtime_ns': Path(path).stat().st_mtime_ns, 'cumulative_counters': json.loads(Path(path).read_text())})
    metrics = dict(
        committed_records=bus['committed'],
        records_with_text=bus['text'],
        records_with_text_pct=ratio(bus['text'], bus['committed'], 100),
        finished_events=bus['finished'],
        records_per_finished=ratio(bus['committed'], bus['finished']),
        journal_entries=journal_count,
        duplicate_journal_entries=duplicates,
        duplicate_journal_pct=ratio(duplicates, journal_count, 100),
        journal_unknown_keys=journal_unknown_keys,
        ledger=dict(ledger),
        cross_host_latency_samples=len(latency),
        cross_host_latency_ms=latency,
        remote_notifier_cpu_minutes_per_day=None,
    )
    metrics['cross_host_clock_errors'] = latency_clock_errors
    metrics['health_send_records'] = health['sends'] if coverage['send_health']['status'] == 'available' else None
    metrics['health_sends_sender_text'] = health['sender_text'] if coverage['send_health']['status'] == 'available' else None
    for category, keys in {'bus': ['committed_records','records_with_text','records_with_text_pct','finished_events','records_per_finished'], 'journals': ['journal_entries','duplicate_journal_entries','duplicate_journal_pct','journal_unknown_keys'], 'ledger':['ledger','cross_host_latency_samples','cross_host_latency_ms']}.items():
        if coverage[category]['status'] != 'available':
            for key in keys:
                metrics[key] = None
    for category in auxiliary:
        if coverage[category]['status'] != 'available':
            auxiliary[category] = None
    result = dict(coverage=coverage, inbox_stats_snapshots=snapshots, window={'start':start.isoformat(),'end_exclusive':end.isoformat()}, parents=parents, metrics=metrics, auxiliary=auxiliary, sources=manifest, parse_errors=errors, caveats=['Legacy bus records and modern ledger records are separate cohorts.', 'Absent/retained source coverage is not proof of zero events.', 'Tokens include cache reads, not monetary cost.', 'Useful wake is only a send, user-message, or task-log proxy.', 'No daily CPU inference from a process snapshot.', 'Human and machine wakes share historical baseline definition.'])
    if args.previous:
        previous = json.loads(Path(args.previous).read_text())
        result['delta'] = numeric_delta(metrics, previous['metrics'])
        result['parent_delta'] = {
            name: numeric_delta(parent, previous['parents'][name])
            for name, parent in parents.items()
            if name in previous.get('parents', {})
        }
    comparisons = []
    for key, target in config.get('targets', {}).items():
        value = metrics.get(key)
        status = 'unknown'
        if isinstance(value, (int, float)):
            passed = value >= target['value'] if target['operator'] == '>=' else value <= target['value']
            status = 'met' if passed else 'not met'
        comparisons.append({'metric': key, 'observed': value, 'operator': target['operator'], 'target': target['value'], 'status': status})
    result['targets'] = comparisons
    (out/'metrics.json').write_text(json.dumps(result,indent=2)+'\n')
    rows=[('Metric','Observed')]+[(key,str(value)) for key,value in metrics.items() if not isinstance(value,(dict,list))]
    md='# Usage retrospective\n\nWindow: '+args.start+' to '+args.end+' (exclusive).\n\n'
    for name,parent in parents.items():
        md+='## '+name+'\n\n'+ '\n'.join('- '+key+': '+str(value) for key,value in parent.items())+'\n\n'
    md+='\n'.join('| '+a+' | '+b+' |' for a,b in rows[:1])+ '\n|---|---|\n'+'\n'.join('| '+a+' | '+b+' |' for a,b in rows[1:])+'\n\n'+ '\n'.join('- '+c for c in result['caveats'])+'\n'
    if comparisons:
        md += '\n## Configured targets\n\n| Metric | Observed | Target | Status |\n|---|---|---|---|\n'
        md += '\n'.join('| {metric} | {observed} | {operator} {target} | {status} |'.format(**item) for item in comparisons) + '\n'
    if args.previous:
        md += '\n## Change from previous window\n\n| Metric | Change |\n|---|---|\n'
        md += '\n'.join('| '+key+' | '+str(value)+' |' for key,value in result['delta'].items()) + '\n'
        md += '\nCompare rates and coverage: unequal-window count deltas are not improvement scores.\n'
    (out/'report.md').write_text(md)
    delta_html = ''
    if args.previous:
        delta_html = '<h2>Change from previous window</h2>' + ''.join('<p>'+html.escape(key.replace('_',' '))+': '+html.escape(str(value))+'</p>' for key,value in result['delta'].items())
    target_html = '<h2>Configured targets</h2>' + ''.join('<p>'+html.escape(item['metric'].replace('_',' '))+': '+item['status']+'</p>' for item in comparisons) if comparisons else ''
    parent_html = ''
    for label, parent in parents.items():
        parent_html += '<h2>'+html.escape(label)+'</h2>'
        parent_html += ''.join('<p>'+html.escape(key.replace('_',' '))+': '+html.escape(str(value))+'</p>' for key, value in parent.items())
    cards=''.join('<section><b>'+html.escape(k.replace('_',' '))+'</b><p>'+html.escape(v)+'</p>'+('<meter min="0" max="100" value="'+v+'"></meter>' if k.endswith('_pct') and v!='None' else '')+'</section>' for k,v in rows[1:])
    (out/'report.html').write_text('<!doctype html><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Weekly deck review</title><style>body{background:white;color:#18212b;font:16px system-ui;max-width:440px;margin:auto;padding:20px}section{border-bottom:1px solid #ddd;padding:12px 0}p{margin:6px 0}meter{width:100%}</style><h1>Weekly deck review</h1><p>'+html.escape(args.start+' to '+args.end)+'</p>'+parent_html+cards+delta_html+target_html+'<p>Coverage gaps remain unknown. Read the Markdown report before drawing conclusions.</p>')
    print(json.dumps({'parents':parents,'metrics':metrics,'parse_errors':len(errors)},indent=2))

if __name__ == '__main__':
    main()
