# Definitions and coverage

`measure.py` reuses and generalizes the original waste-analysis `analyze_conductors.py` wake parser. Its bus cohorts come from `a5_bus_transitions.py`; journal duplicate keys come from `a3_latency_journal.py`. Paths, owner accounts, fixed dates and child naming assumptions were removed. The original sources remain unchanged.

A Claude wake begins at a non-tool-result user record. Meta expansions join the current wake, except actual Stop-hook and channel triggers; consecutive user records before an assistant turn merge. A pre-window continuation can create one unknown wake. API usage takes the maximum per field per message ID within a wake to avoid repeated content-block counts. Tokens include input, output, cached reads and cache creation. Cached read tokens are not newly generated tokens or billing cost. Superset account copies must be selected or UUID-deduplicated before analysis, never added together.

The historical 1,428 denominator counts all these wakes, including human triggers. Do not relabel it as strictly machine wakes. For strict machine rate, retain a separate trigger cohort and unknown count.

Legacy text coverage = committed `session.transition` frames with nonempty text / all committed transition frames. Records per finished = those committed transitions / separately committed `session.finished` frames. Journal duplication = excess records with the same `(child, uuid)` / total journal rows. Unknown keys remain separate. Modern ledger counts are a different cohort, never substituted silently into historical ratios. Send coverage requires both sender and nonempty text. A text hash alone is not text. Cross-host latency requires remote provenance and matched signal/seen clocks; negative latency is a clock problem, not fast delivery.

Useful wake proxy = at least one send, user-channel message or task-log write. This is not proof the action was useful. Output/drain counts are observed command calls, not API completion or delivery. Worker and Codex findings still require manual reading; the bundled Claude parser does not claim to parse every harness.

Absent sources, zero denominators, unsupported formats and rotated history remain unknown. Include first/last source times and cohort retention when comparing runs. File inventories are coverage evidence, not full data analysis.
