# Skill evaluations

`./eval/skills/run.sh --jobs 4` builds the binary in a network-disabled container, then runs every case in its own container. Each gets a throwaway HOME plus read-only binary and skill mounts. The offline deterministic harness in `fake_agent.py` selects commands from task wording and refuses commands absent from the supplied skill; no model access is needed.

Use `./eval/skills/run.sh --remote g14` to submit through the overnight gate-runner. If g14 or the gate fails, the script automatically runs the identical local container path.
