# Opt-in Codex submit retry

`AGENTDECK_CODEX_SUBMIT_RETRY=1` enables one guarded Enter retry while the Codex arrival verifier still sees this send's own unsent text. It is disabled by default and changes no permission or identity rules.

The normal Codex path checks arrival without using the Claude submit loop. If the first post-paste Enter is swallowed, ten observations can finish without another Enter. The opt-in presses at most one more Enter per send, within the existing verification budget, using the existing attribution gate and input-prompt guard.

The retry fires only when the submit was really swallowed: the send's own text must still be unsent in two captures taken one check delay apart, and the capture must not already show the turn starting. The first capture comes right after the paste and its Enter, when Codex often still shows the text only because it has not processed that Enter yet, so it never triggers a retry on its own. No retry is pressed on the last check. It never clears or retypes input, resolves an approval prompt, deletes a submission marker, or labels a queued receipt as delivered.

Enable it only for the relevant sending process. Queued workers inherit that process's environment. Already typed or ambiguous queue records are not replayed or repaired by this option; preserve their evidence and use the documented operator procedure. Verify a new legitimate request with its native transcript row and accepted turn before treating automation as working.
