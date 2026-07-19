package session

import "testing"

// TestItemIsCreatingPlaceholder verifies the model-layer predicate that gates
// mutations (move/rename/fork) against a still-creating session row. A
// creating placeholder surfaces as Type==ItemTypeSession with a nil Session
// (the #1540 crash shape); every other row must report false.
func TestItemIsCreatingPlaceholder(t *testing.T) {
	tests := []struct {
		name string
		item Item
		want bool
	}{
		{
			name: "creating placeholder (session row, nil instance)",
			item: Item{Type: ItemTypeSession, Session: nil, CreatingID: "tmp-123"},
			want: true,
		},
		{
			name: "real session row",
			item: Item{Type: ItemTypeSession, Session: &Instance{ID: "s1"}},
			want: false,
		},
		{
			name: "group row",
			item: Item{Type: ItemTypeGroup, Group: &Group{Name: "g"}},
			want: false,
		},
		{
			name: "remote session row",
			item: Item{Type: ItemTypeRemoteSession},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.item.IsCreatingPlaceholder(); got != tt.want {
				t.Fatalf("IsCreatingPlaceholder() = %v, want %v", got, tt.want)
			}
		})
	}
}
