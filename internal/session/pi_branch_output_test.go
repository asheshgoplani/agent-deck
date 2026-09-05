package session

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// These fixtures follow Pi session-manager.ts at 2b768ba42cdbd7336474d6986a3ef1efbb0a44f7:
// persisted entries carry id/parentId; the last persisted entry is the leaf.
const piBranchHistory = `{"type":"session","version":3,"id":"pi-branches"}
{"type":"message","id":"u1","parentId":null,"message":{"role":"user","content":"first question"}}
{"type":"message","id":"a1","parentId":"u1","timestamp":"2026-09-05T01:00:00Z","message":{"role":"assistant","content":[{"type":"text","text":"ACTIVE ANSWER"},{"type":"text","text":"second block"}]}}
{"type":"message","id":"u2","parentId":"a1","message":{"role":"user","content":"abandoned question"}}
{"type":"message","id":"a2","parentId":"u2","timestamp":"2026-09-05T02:00:00Z","message":{"role":"assistant","content":[{"type":"text","text":"INACTIVE ANSWER"}]}}
`

func TestPiResponseUsesPersistedBranch(t *testing.T) {
	for _, tc := range []struct {
		name    string
		append  string
		content string
		stamp   string
		wantErr bool
	}{
		{
			name:    "unchanged persisted leaf remains authoritative",
			content: "INACTIVE ANSWER", stamp: "2026-09-05T02:00:00Z",
		},
		{
			name:    "user leaf selects earlier assistant",
			append:  `{"type":"message","id":"u3","parentId":"a1","message":{"role":"user","content":"new branch"}}`,
			content: "ACTIVE ANSWER\nsecond block", stamp: "2026-09-05T01:00:00Z",
		},
		{
			name:    "summary uses parent not abandoned fromId",
			append:  `{"type":"branch_summary","id":"s1","parentId":"a1","fromId":"a2","summary":"abandoned context"}`,
			content: "ACTIVE ANSWER\nsecond block", stamp: "2026-09-05T01:00:00Z",
		},
		{
			name:    "metadata leaf follows the same branch",
			append:  `{"type":"session_info","id":"n1","parentId":"a1","name":"renamed branch"}`,
			content: "ACTIVE ANSWER\nsecond block", stamp: "2026-09-05T01:00:00Z",
		},
		{
			name: "nonmessage ancestor remains in chain",
			append: `{"type":"model_change","id":"m1","parentId":"a1","provider":"fixture","modelId":"fixture"}
{"type":"message","id":"u3","parentId":"m1","message":{"role":"user","content":"new branch"}}`,
			content: "ACTIVE ANSWER\nsecond block", stamp: "2026-09-05T01:00:00Z",
		},
		{
			name:    "tool only assistant falls back within branch",
			append:  `{"type":"message","id":"a3","parentId":"a1","message":{"role":"assistant","content":[{"type":"thinking","thinking":"hidden"},{"type":"toolCall","name":"read"}]}}`,
			content: "ACTIVE ANSWER\nsecond block", stamp: "2026-09-05T01:00:00Z",
		},
		{
			name:    "new active assistant wins regardless of timestamp order",
			append:  `{"type":"message","id":"a3","parentId":"a1","timestamp":"2026-09-05T00:00:00Z","message":{"role":"assistant","content":[{"type":"text","text":"NEW ACTIVE ANSWER"}]}}`,
			content: "NEW ACTIVE ANSWER", stamp: "2026-09-05T00:00:00Z",
		},
		{
			name: "partial tree append cannot revive previous branch",
			append: `{"type":"message","id":"u3","parentId":"a1","message":{"role":"user","content":"new branch"}}
{"type":"message","id":"partial",`,
			wantErr: true,
		},
		{
			name:    "reset to new root does not borrow old assistant",
			append:  `{"type":"message","id":"u3","parentId":null,"message":{"role":"user","content":"fresh root"}}`,
			wantErr: true,
		},
		{
			name:    "summary at root does not expose abandoned assistant",
			append:  `{"type":"branch_summary","id":"s1","parentId":null,"fromId":"a2","summary":"abandoned context"}`,
			wantErr: true,
		},
		{
			name:    "dangling parent cannot borrow file last assistant",
			append:  `{"type":"message","id":"u3","parentId":"missing","message":{"role":"user","content":"new branch"}}`,
			wantErr: true,
		},
		{
			name:    "assistant leaf still validates its parent",
			append:  `{"type":"message","id":"a3","parentId":"missing","message":{"role":"assistant","content":[{"type":"text","text":"UNPROVEN ANSWER"}]}}`,
			wantErr: true,
		},
		{
			name:    "self cycle is not a valid ancestry",
			append:  `{"type":"message","id":"a3","parentId":"a3","message":{"role":"assistant","content":[{"type":"text","text":"CYCLIC ANSWER"}]}}`,
			wantErr: true,
		},
		{
			name: "two node cycle is rejected",
			append: `{"type":"message","id":"u3","parentId":"a3","message":{"role":"user","content":"cycle"}}
{"type":"message","id":"a3","parentId":"u3","message":{"role":"assistant","content":[{"type":"text","text":"CYCLIC ANSWER"}]}}`,
			wantErr: true,
		},
		{
			name: "duplicate ID cannot replace an ancestor",
			append: `{"type":"message","id":"a1","parentId":"a2","message":{"role":"assistant","content":[{"type":"text","text":"AMBIGUOUS ANSWER"}]}}
{"type":"message","id":"u3","parentId":"a1","message":{"role":"user","content":"new branch"}}`,
			wantErr: true,
		},
		{
			name:    "missing parent field is not null root",
			append:  `{"type":"message","id":"u3","message":{"role":"user","content":"unknown parent"}}`,
			wantErr: true,
		},
		{
			name:    "nonstring parent is invalid",
			append:  `{"type":"message","id":"u3","parentId":42,"message":{"role":"user","content":"unknown parent"}}`,
			wantErr: true,
		},
		{
			name:    "missing leaf ID cannot select an old branch",
			append:  `{"type":"message","parentId":"a1","message":{"role":"user","content":"unknown leaf"}}`,
			wantErr: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parsePiLastAssistantMessage([]byte(piBranchHistory + tc.append))
			if tc.wantErr {
				if err == nil || got != nil {
					t.Fatalf("unproven branch returned a response: %+v, %v", got, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want := ResponseOutput{Tool: "pi", Role: "assistant", Content: tc.content, Timestamp: tc.stamp, SessionID: "pi-branches"}
			if *got != want {
				t.Fatalf("response from wrong persisted ancestry: got %+v, want %+v", got, want)
			}
		})
	}
}

func TestPiResponsePreservesLegacyLinearSessions(t *testing.T) {
	for _, header := range []string{"", `{"type":"session","id":"legacy"}`, `{"type":"session","id":"legacy","version":1}`} {
		t.Run("header"+header, func(t *testing.T) {
			data := fmt.Sprintf(`%s
{"type":"message","timestamp":"old","message":{"role":"assistant","content":[{"type":"text","text":"first"}]}}
{"type":"message","timestamp":"last","message":{"role":"assistant","content":[{"type":"text","text":"legacy last"}]}}
{"type":"message","message":{"role":"assistant","content":[{"type":"toolCall","name":"read"}]}}
{"type":"message",`, header)
			got, err := parsePiLastAssistantMessage([]byte(data))
			if err != nil {
				t.Fatal(err)
			}
			want := ResponseOutput{Tool: "pi", Role: "assistant", Content: "legacy last", Timestamp: "last"}
			if header != "" {
				want.SessionID = "legacy"
			}
			if *got != want {
				t.Fatalf("legacy linear behavior changed: got %+v, want %+v", got, want)
			}
		})
	}
}

func TestPiLastResponseReadsPersistedBranchFromDisk(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".pi", "agent-deck", "branch-output")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	data := piBranchHistory + `{"type":"message","id":"u3","parentId":"a1","message":{"role":"user","content":"new branch"}}`
	if err := os.WriteFile(filepath.Join(dir, "session.jsonl"), []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := (&Instance{ID: "branch-output", Tool: "pi"}).GetLastResponse()
	if err != nil {
		t.Fatal(err)
	}
	want := ResponseOutput{Tool: "pi", Role: "assistant", Content: "ACTIVE ANSWER\nsecond block", Timestamp: "2026-09-05T01:00:00Z", SessionID: "pi-branches"}
	if *got != want {
		t.Fatalf("public response API selected an inactive branch: got %+v, want %+v", got, want)
	}
}

func TestPiResponseTreeFormatCannotDowngradeToLinear(t *testing.T) {
	leaf := `{"type":"message","id":"u3","parentId":"a1","message":{"role":"user","content":"new branch"}}`
	for _, tc := range []struct {
		name string
		data string
	}{
		{"tree without header", strings.SplitN(piBranchHistory, "\n", 2)[1] + leaf},
		{"tree with missing version", strings.Replace(piBranchHistory, `"version":3,`, "", 1) + leaf},
		{"tree with legacy version", strings.Replace(piBranchHistory, `"version":3`, `"version":1`, 1) + leaf},
		{"empty header identity", strings.Replace(piBranchHistory, `"id":"pi-branches"`, `"id":""`, 1)},
		{"nonstring header identity", strings.Replace(piBranchHistory, `"id":"pi-branches"`, `"id":42`, 1)},
		{"duplicate header", piBranchHistory + `{"type":"session","version":3,"id":"different-session"}` + "\n" + leaf},
		{"empty leaf identity", piBranchHistory + strings.Replace(leaf, `"id":"u3"`, `"id":""`, 1)},
		{"nonstring leaf identity", piBranchHistory + strings.Replace(leaf, `"id":"u3"`, `"id":42`, 1)},
		{"empty parent identity", piBranchHistory + strings.Replace(leaf, `"parentId":"a1"`, `"parentId":""`, 1)},
		{"corrupt ancestor below text assistant", strings.Replace(piBranchHistory, `"id":"u1","parentId":null`, `"id":"u1","parentId":"missing"`, 1) + leaf},
		{"version two without tree identities", `{"type":"session","version":2,"id":"malformed"}
{"type":"message","message":{"role":"assistant","content":[{"type":"text","text":"UNPROVEN ANSWER"}]}}`},
		{"complete malformed tree record", piBranchHistory + "not-json\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, err := parsePiLastAssistantMessage([]byte(tc.data)); err == nil || got != nil {
				t.Fatalf("malformed tree fell back to linear response: %+v, %v", got, err)
			}
		})
	}
}

func TestPiResponseVersionTwoAndMetadataLeaves(t *testing.T) {
	for _, kind := range []string{"custom", "label", "compaction"} {
		t.Run(kind, func(t *testing.T) {
			data := strings.Replace(piBranchHistory, `"version":3`, `"version":2`, 1) + fmt.Sprintf(`{"type":%q,"id":"leaf","parentId":"a1"}`, kind)
			got, err := parsePiLastAssistantMessage([]byte(data))
			if err != nil {
				t.Fatal(err)
			}
			want := ResponseOutput{Tool: "pi", Role: "assistant", Content: "ACTIVE ANSWER\nsecond block", Timestamp: "2026-09-05T01:00:00Z", SessionID: "pi-branches"}
			if *got != want {
				t.Fatalf("metadata lost version-two ancestry: got %+v, want %+v", got, want)
			}
		})
	}
}
