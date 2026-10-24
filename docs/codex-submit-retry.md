# Opt-in Codex submit retry

`AGENTDECK_CODEX_SUBMIT_RETRY=1` enables guarded Enter retries while the Codex arrival verifier still sees this send's own unsent text. It is disabled by default and changes no permission or identity rules.

The normal Codex path checks arrival without using the Claude submit loop. If the first post-paste Enter is swallowed, ten observations can finish without another Enter. The opt-in retries within the existing verification budget, using the existing attribution gate and input-prompt guard. It never clears or retypes input, resolves an approval prompt, deletes a submission marker, or labels a queued receipt as delivered.

Enable it only for the relevant sending process. Queued workers inherit that process's environment. Already typed or ambiguous queue records are not replayed or repaired by this option; preserve their evidence and use the documented operator procedure. Verify a new legitimate request with its native transcript row and accepted turn before treating automation as working.
