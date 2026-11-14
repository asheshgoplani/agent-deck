# demo project

A tiny project that every new VM from the example azure-vm provider gets at
`~/demo` when no project is configured, so the first agent session has
something to work on.

`./wordcount.sh FILE` prints the number of lines, words and characters in
FILE. It has a bug: it counts one word too many. Ask the agent to find and
fix it, and to add a test.
