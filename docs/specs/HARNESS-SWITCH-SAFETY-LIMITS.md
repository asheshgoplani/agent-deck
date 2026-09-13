# Harness switch safety limits

Cross-harness switching creates a distinct fresh row and transfers only the bounded
portable context. It does not copy credentials, conductor roles, watcher routing,
or service ownership.

A cross-harness request requires authoritative ownership validation and is refused
when its source is a managed conductor, a known watcher bridge target, or has
dependent children. The registry graph is re-read before payload staging and again
before final commit; the SQLite supersession transaction independently rejects a
current conductor source or newly-added dependent child. Ordinary child workers may
switch: their existing parent link is retained. Same-harness native switching is
unchanged.

Watcher routing is external configuration, not part of the SQLite transaction. Its
read is repeated at both validation points and unreadable routing fails closed, but
a watcher route written after the final read can still race the DB commit. This
unclosed external-config race is reported here rather than represented as an atomic
ownership proof; closing it requires coordinated watcher ownership/storage work.

Remote sessions are unsupported locally because their transcripts and lifecycle are
owned by the remote host. Configure and test a remote switch directly on that host
with an updated Agent Deck binary; the macOS remote TUI does not provide this flow.
