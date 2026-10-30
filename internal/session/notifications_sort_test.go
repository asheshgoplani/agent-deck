package session

import (
	"testing"
	"time"

	"github.com/BurntSushi/toml"
)

// #2949: [notifications] sort = "waiting" (default) | "last_accessed".

func notifEntryIDs(nm *NotificationManager) []string {
	var ids []string
	for _, e := range nm.GetEntries() {
		ids = append(ids, e.SessionID)
	}
	return ids
}

func assertNotifOrder(t *testing.T, nm *NotificationManager, want ...string) {
	t.Helper()
	got := notifEntryIDs(nm)
	if len(got) != len(want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
	for i, e := range nm.GetEntries() {
		if e.AssignedKey != string(rune('1'+i)) {
			t.Fatalf("entry %s has key %q, want %q", e.SessionID, e.AssignedKey, string(rune('1'+i)))
		}
	}
}

// Waiting order is newest-created first (GetWaitingSince falls back to
// CreatedAt); last-accessed order is the reverse of that here.
func sortFixture(now time.Time) []*Instance {
	return []*Instance{
		{ID: "a", Title: "a", Status: StatusWaiting, CreatedAt: now, LastAccessedAt: now.Add(-3 * time.Minute)},
		{ID: "b", Title: "b", Status: StatusWaiting, CreatedAt: now.Add(-1 * time.Minute), LastAccessedAt: now.Add(-2 * time.Minute)},
		{ID: "c", Title: "c", Status: StatusWaiting, CreatedAt: now.Add(-2 * time.Minute), LastAccessedAt: now.Add(-1 * time.Minute)},
	}
}

func TestNotificationSort_DefaultIsWaitingOrder(t *testing.T) {
	nm := NewNotificationManager(6, false, false)
	nm.SyncFromInstances(sortFixture(time.Now()), "")
	assertNotifOrder(t, nm, "a", "b", "c")
}

func TestNotificationSort_ExplicitWaitingOrder(t *testing.T) {
	nm := NewNotificationManager(6, false, false)
	nm.SetSortMode(NotificationSortWaiting)
	nm.SyncFromInstances(sortFixture(time.Now()), "")
	assertNotifOrder(t, nm, "a", "b", "c")
}

func TestNotificationSort_LastAccessedOrder(t *testing.T) {
	nm := NewNotificationManager(6, true, false)
	nm.SetSortMode(NotificationSortLastAccessed)
	now := time.Now()
	insts := sortFixture(now)
	// A session never attached to goes after every accessed one.
	insts = append(insts, &Instance{ID: "never", Title: "never", Status: StatusIdle, CreatedAt: now.Add(time.Minute)})
	nm.SyncFromInstances(insts, "")
	assertNotifOrder(t, nm, "c", "b", "a", "never")
}

// Entries already on the bar must move when the user attaches to them later,
// not keep the position they had when first added.
func TestNotificationSort_LastAccessedResortsExistingEntries(t *testing.T) {
	nm := NewNotificationManager(6, false, false)
	nm.SetSortMode(NotificationSortLastAccessed)
	now := time.Now()
	insts := sortFixture(now)
	nm.SyncFromInstances(insts, "")
	assertNotifOrder(t, nm, "c", "b", "a")

	insts[0].LastAccessedAt = now // user attached to "a" just now
	nm.SyncFromInstances(insts, "")
	assertNotifOrder(t, nm, "a", "c", "b")
}

// With more sessions than max_shown, last_accessed keeps the most recently
// used ones on the bar.
func TestNotificationSort_LastAccessedTrimKeepsRecent(t *testing.T) {
	nm := NewNotificationManager(2, false, false)
	nm.SetSortMode(NotificationSortLastAccessed)
	nm.SyncFromInstances(sortFixture(time.Now()), "")
	assertNotifOrder(t, nm, "c", "b")
}

func TestNotificationSort_UnknownModeFallsBackToWaiting(t *testing.T) {
	for _, mode := range []string{"", "bogus", "lastaccessed", "recent"} {
		nm := NewNotificationManager(6, false, false)
		nm.SetSortMode(mode)
		nm.SyncFromInstances(sortFixture(time.Now()), "")
		assertNotifOrder(t, nm, "a", "b", "c")

		if got := (NotificationsConfig{Sort: mode}).GetSort(); got != NotificationSortWaiting {
			t.Fatalf("GetSort(%q) = %q, want %q", mode, got, NotificationSortWaiting)
		}
	}
}

func TestNotificationSort_ConfigKeyParses(t *testing.T) {
	var cfg UserConfig
	if _, err := toml.Decode("[notifications]\nsort = \"last_accessed\"\n", &cfg); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got := cfg.Notifications.GetSort(); got != NotificationSortLastAccessed {
		t.Fatalf("GetSort() = %q, want %q", got, NotificationSortLastAccessed)
	}
	var empty UserConfig
	if got := empty.Notifications.GetSort(); got != NotificationSortWaiting {
		t.Fatalf("default GetSort() = %q, want %q", got, NotificationSortWaiting)
	}
}
