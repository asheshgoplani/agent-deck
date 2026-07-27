package session

import (
	"testing"
)

// A launch that lands on a group at its max_concurrent cap is parked as
// StatusQueued instead of started. Its --message-file prompt was already
// resolved by then (the file read, or stdin drained) and had nowhere to live,
// so it was dropped: whenever the session finally started it came up on an
// empty composer reporting `waiting`, and the prompt had to be re-typed.

func TestQueuedMessage_RoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	const id = "abc12345-1785145042"
	const prompt = "Implement S2: JobsAutofillService + extractJobFromText.\n\nGate on gate:backend."

	if _, ok := TakeQueuedMessage(id); ok {
		t.Fatal("precondition: no prompt should be pending yet")
	}
	if err := SaveQueuedMessage(id, prompt); err != nil {
		t.Fatalf("save: %v", err)
	}

	got, ok := TakeQueuedMessage(id)
	if !ok {
		t.Fatal("a queued prompt must survive until the session starts")
	}
	if got != prompt {
		t.Errorf("prompt round-trip:\n got %q\nwant %q", got, prompt)
	}
}

// Delivery is exactly-once: a restart, or a second drain after the first
// already sent the prompt, must not replay it into a live conversation.
func TestQueuedMessage_TakeConsumes(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	const id = "def67890-1785145042"
	if err := SaveQueuedMessage(id, "do the thing"); err != nil {
		t.Fatalf("save: %v", err)
	}
	if _, ok := TakeQueuedMessage(id); !ok {
		t.Fatal("first take must return the prompt")
	}
	if got, ok := TakeQueuedMessage(id); ok {
		t.Fatalf("second take must find nothing, got %q", got)
	}
}

func TestQueuedMessage_PeekDoesNotConsume(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	const id = "aaa11111-1785145042"
	if err := SaveQueuedMessage(id, "still pending"); err != nil {
		t.Fatalf("save: %v", err)
	}
	if got, ok := PeekQueuedMessage(id); !ok || got != "still pending" {
		t.Fatalf("peek: got %q/%v", got, ok)
	}
	if got, ok := TakeQueuedMessage(id); !ok || got != "still pending" {
		t.Fatalf("peek must not consume; take got %q/%v", got, ok)
	}
}

// Re-queuing without a prompt must clear any older one rather than leave it to
// be delivered as if it belonged to the new launch.
func TestQueuedMessage_EmptySaveClears(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	const id = "bbb22222-1785145042"
	if err := SaveQueuedMessage(id, "old prompt"); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := SaveQueuedMessage(id, ""); err != nil {
		t.Fatalf("clearing save: %v", err)
	}
	if got, ok := TakeQueuedMessage(id); ok {
		t.Fatalf("an empty save must clear the pending prompt, got %q", got)
	}
}

func TestQueuedMessage_DiscardRemoves(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	const id = "ccc33333-1785145042"
	if err := SaveQueuedMessage(id, "to be discarded"); err != nil {
		t.Fatalf("save: %v", err)
	}
	DiscardQueuedMessage(id)
	if _, ok := TakeQueuedMessage(id); ok {
		t.Fatal("a discarded prompt must not be delivered")
	}
}

func TestQueuedMessage_RejectsEmptyID(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := SaveQueuedMessage("  ", "x"); err == nil {
		t.Error("an empty instance id must be rejected, not written to a stray path")
	}
}

// Prompts are long and multi-line by construction (--message-file exists
// precisely to avoid shell quoting); nothing may be trimmed or re-wrapped.
func TestQueuedMessage_PreservesExactBytes(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	const id = "ddd44444-1785145042"
	prompt := "  leading spaces\n\ttab\n\nblank line above\ntrailing newline\n"
	if err := SaveQueuedMessage(id, prompt); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, ok := TakeQueuedMessage(id)
	if !ok || got != prompt {
		t.Errorf("bytes must round-trip exactly:\n got %q\nwant %q", got, prompt)
	}
}
