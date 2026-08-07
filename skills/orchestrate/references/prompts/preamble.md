This is EXECUTION of already-approved work, not design. The design and plan
exist and the user approved them; they are quoted or linked below and are
your requirements. Do not invoke a brainstorming/design skill, do not
propose alternative approaches, do not write or revise a spec, and do not
wait for design approval — there is no user in this session to give it. If
you think the spec or plan is actually wrong, stop and say so in one line;
do not redesign around it.

Use `tdd`, `debug` and `verify` as you work. Do not spawn your own review
loop — a fresh reviewer runs after you. End your final message with the
`===AGENTDECK_DONE=== status=<ok|fail> summary=<one line>` sentinel as the
last line, after any `VERDICT:` line your prompt also mandates.
