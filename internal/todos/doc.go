// Package todos is the owner's to-do list: one small list per host, shared
// by every profile, filled from the CLI (`agent-deck todo`), the web API
// (/api/todos), the TUI and voice (docs/todos.md).
//
// The list lives in <agent-deck data dir>/todos.json (0600), written by
// rename (internal/atomicfile) under the repo's config-file lock
// (session.AcquireConfigFileLockTimeout on todos.json.lock), the same house
// style as the assistant token store. Readers take no lock: writers replace
// the file by rename, so a reader sees the old or the new list, never half.
//
// Every change is published as a todo.changed frame (KindChanged) on the
// events bus of every profile on the host, so `agent-deck events follow`,
// the web server, the Mac app and the phone gateway can refresh the list
// whichever profile they follow.
//
// IDs are short decimal strings ("1", "2", ...) that are never reused, so
// "done 3" said aloud or typed always means the same item.
package todos
