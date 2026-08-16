package procowner

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReceipt_RoundTrip(t *testing.T) {
	p := newFakeProber()
	leader := p.add(100, 1, "5000", 1000)
	child := p.add(101, 100, "5100", 1000)
	original := liveReceipt("inst-1", memberOf(leader, RoleLeader), memberOf(child, RoleDescendant))

	data, err := Encode(original)
	require.NoError(t, err)
	decoded, err := Decode(data)
	require.NoError(t, err)
	assert.Equal(t, original.Leader, decoded.Leader)
	assert.Equal(t, original.Members, decoded.Members)
	assert.Equal(t, original.BootID, decoded.BootID)
}

// A receipt that cannot be parsed is NOT the same as no receipt. Folding the
// two together is how a truncated file becomes a duplicate process tree.
func TestDecode_RejectsUnusableReceipts(t *testing.T) {
	valid, err := Encode(liveReceipt("inst-1", Member{PID: 100, StartID: "5000", UID: 1000, Role: RoleLeader}))
	require.NoError(t, err)

	cases := map[string][]byte{
		"truncated":       valid[:len(valid)/2],
		"empty":           {},
		"not json":        []byte("this is not a receipt"),
		"json null":       []byte("null"),
		"missing leader":  mutate(t, valid, func(m map[string]any) { delete(m, "leader") }),
		"leader pid zero": mutate(t, valid, func(m map[string]any) { m["leader"] = map[string]any{"start_id": "5000"} }),
		"no start id":     mutate(t, valid, func(m map[string]any) { m["leader"] = map[string]any{"pid": 100} }),
		"missing boot id": mutate(t, valid, func(m map[string]any) { delete(m, "boot_id") }),
		"future version":  mutate(t, valid, func(m map[string]any) { m["version"] = ReceiptVersion + 1 }),
		"unknown state":   mutate(t, valid, func(m map[string]any) { m["state"] = "whatever" }),
		"no instance id":  mutate(t, valid, func(m map[string]any) { delete(m, "instance_id") }),
		"no provider":     mutate(t, valid, func(m map[string]any) { delete(m, "provider") }),
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Decode(data)
			require.Error(t, err)
			assert.True(t, errors.Is(err, ErrCorruptReceipt), "want ErrCorruptReceipt, got %v", err)
		})
	}
}

func mutate(t *testing.T, data []byte, fn func(map[string]any)) []byte {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal(data, &m))
	fn(m)
	out, err := json.Marshal(m)
	require.NoError(t, err)
	return out
}

func TestStore_SaveLoadClear(t *testing.T) {
	store := NewStore(t.TempDir())
	receipt := liveReceipt("inst-1", Member{PID: 100, StartID: "5000", UID: 1000, Role: RoleLeader})

	loaded, err := store.Load("inst-1")
	require.NoError(t, err)
	assert.Nil(t, loaded, "no receipt is not an error")

	require.NoError(t, store.Save(receipt))
	loaded, err = store.Load("inst-1")
	require.NoError(t, err)
	require.NotNil(t, loaded)
	assert.Equal(t, uint64(1), loaded.Generation)

	require.NoError(t, store.Clear(receipt))
	loaded, err = store.Load("inst-1")
	require.NoError(t, err)
	assert.Nil(t, loaded)
}

func TestStore_ReceiptsAreWrittenPrivate(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(filepath.Join(dir, "ownership"))
	require.NoError(t, store.Save(liveReceipt("inst-1", Member{PID: 100, StartID: "5000", UID: 1000, Role: RoleLeader})))

	path, err := store.Path("inst-1")
	require.NoError(t, err)
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

// Two restarts racing: the loser's write must not land on top of the winner's
// receipt, and its late clear must not delete the winner's.
func TestStore_GenerationConflictsAreRefused(t *testing.T) {
	store := NewStore(t.TempDir())
	newer := liveReceipt("inst-1", Member{PID: 200, StartID: "6000", UID: 1000, Role: RoleLeader})
	newer.Generation = 5
	require.NoError(t, store.Save(newer))

	stale := liveReceipt("inst-1", Member{PID: 100, StartID: "5000", UID: 1000, Role: RoleLeader})
	stale.Generation = 4
	err := store.Save(stale)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrGenerationConflict))

	err = store.Clear(stale)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrGenerationConflict))

	// Same generation, different leader: a stale writer whose receipt was
	// already reaped and replaced must not overwrite or delete the live one.
	sameGen := liveReceipt("inst-1", Member{PID: 300, StartID: "7000", UID: 1000, Role: RoleLeader})
	sameGen.Generation = 5
	err = store.Save(sameGen)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrGenerationConflict))
	err = store.Clear(sameGen)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrGenerationConflict))

	loaded, err := store.Load("inst-1")
	require.NoError(t, err)
	require.NotNil(t, loaded)
	assert.Equal(t, 200, loaded.Leader.PID, "the winner's receipt survived both stale writers")
}

func TestStore_SameGenerationMayAddMembers(t *testing.T) {
	store := NewStore(t.TempDir())
	receipt := liveReceipt("inst-1", Member{PID: 100, StartID: "5000", UID: 1000, Role: RoleLeader})
	require.NoError(t, store.Save(receipt))

	receipt.Members = append(receipt.Members, Member{PID: 101, StartID: "5100", UID: 1000, Role: RoleDescendant})
	require.NoError(t, store.Save(receipt))

	loaded, err := store.Load("inst-1")
	require.NoError(t, err)
	require.Len(t, loaded.Members, 1)
}

func TestStore_CorruptReceiptSurfacesAsAnError(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "inst-1.json"), []byte(`{"version":1,"inst`), 0o600))

	_, err := store.Load("inst-1")
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrCorruptReceipt))

	// A fresh spawn still gets a usable generation, and its write replaces the
	// unreadable file rather than being blocked by it.
	gen, err := store.NextGeneration("inst-1")
	require.NoError(t, err)
	assert.Equal(t, uint64(1), gen)
	require.NoError(t, store.Save(liveReceipt("inst-1", Member{PID: 100, StartID: "5000", UID: 1000, Role: RoleLeader})))
	loaded, err := store.Load("inst-1")
	require.NoError(t, err)
	require.NotNil(t, loaded)
}

func TestStore_ReceiptForAnotherInstanceIsRefused(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	data, err := Encode(liveReceipt("inst-2", Member{PID: 100, StartID: "5000", UID: 1000, Role: RoleLeader}))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "inst-1.json"), data, 0o600))

	_, err = store.Load("inst-1")
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrCorruptReceipt))
}

func TestStore_RefusesInstanceIDsThatEscapeTheDirectory(t *testing.T) {
	store := NewStore(t.TempDir())
	for _, id := range []string{"", "..", "../escape", "a/b", `a\b`, "."} {
		_, err := store.Path(id)
		require.Error(t, err, "id %q must be refused", id)
		assert.True(t, errors.Is(err, ErrBadInstanceID))
	}
}

func TestStore_NextGenerationIncrements(t *testing.T) {
	store := NewStore(t.TempDir())
	gen, err := store.NextGeneration("inst-1")
	require.NoError(t, err)
	assert.Equal(t, uint64(1), gen)

	receipt := liveReceipt("inst-1", Member{PID: 100, StartID: "5000", UID: 1000, Role: RoleLeader})
	receipt.Generation = gen
	require.NoError(t, store.Save(receipt))

	gen, err = store.NextGeneration("inst-1")
	require.NoError(t, err)
	assert.Equal(t, uint64(2), gen)
}

func TestClaim_RecordsLeaderIdentityAtSpawn(t *testing.T) {
	p := newFakeProber()
	p.add(100, 1, "5000", 1000)

	receipt, err := Claim(p, ClaimInput{InstanceID: "inst-1", Generation: 3, PanePID: 100, TmuxName: "sess"})
	require.NoError(t, err)
	assert.Equal(t, 100, receipt.Leader.PID)
	assert.Equal(t, "5000", receipt.Leader.StartID)
	assert.Equal(t, RoleLeader, receipt.Leader.Role)
	assert.Equal(t, "boot-1", receipt.BootID)
	assert.Equal(t, StateLive, receipt.State)
	assert.Equal(t, uint64(3), receipt.Generation)
	require.NoError(t, receipt.Validate())
}

func TestClaim_RefusesWhatItCannotProve(t *testing.T) {
	p := newFakeProber()
	p.add(100, 1, "5000", 1000)

	_, err := Claim(p, ClaimInput{InstanceID: "inst-1", PanePID: 0})
	require.Error(t, err)

	_, err = Claim(p, ClaimInput{InstanceID: "inst-1", PanePID: 1})
	require.Error(t, err, "pid 1 is never ours")

	_, err = Claim(p, ClaimInput{InstanceID: "inst-1", PanePID: os.Getpid()})
	require.Error(t, err, "agent-deck must never claim itself")

	// A pane process that exited between tmux start and the probe: no receipt,
	// rather than a receipt naming a pid we cannot substantiate.
	_, err = Claim(p, ClaimInput{InstanceID: "inst-1", PanePID: 999})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrNoProcess))

	p.bootErr = errors.New("no boot id")
	_, err = Claim(p, ClaimInput{InstanceID: "inst-1", PanePID: 100})
	require.Error(t, err, "without a boot id every start identity is ambiguous")
}

func TestAttribute_RecordsLiveDescendants(t *testing.T) {
	p := newFakeProber()
	p.add(100, 1, "5000", 1000)
	p.add(101, 100, "5100", 1000)
	p.add(102, 101, "5200", 1000)

	receipt, err := Claim(p, ClaimInput{InstanceID: "inst-1", Generation: 1, PanePID: 100})
	require.NoError(t, err)
	added, err := Attribute(p, receipt, nil)
	require.NoError(t, err)
	require.Len(t, added, 2)
	assert.Equal(t, "5100", added[0].StartID)
	assert.Equal(t, RoleDescendant, added[0].Role)

	// Idempotent: a second pass adds nothing new.
	added, err = Attribute(p, receipt, nil)
	require.NoError(t, err)
	assert.Empty(t, added)
	assert.Len(t, receipt.Members, 2)
}

// Attribution walks DOWN from a leader that still verifies. Once the leader is
// gone — or its pid has been reused — nothing reachable from that pid is ours.
func TestAttribute_RefusesWhenTheLeaderIsNotOurs(t *testing.T) {
	p := newFakeProber()
	p.add(100, 1, "5000", 1000)
	receipt, err := Claim(p, ClaimInput{InstanceID: "inst-1", Generation: 1, PanePID: 100})
	require.NoError(t, err)

	p.remove(100)
	p.add(100, 1, "9999", 1000)   // reused pid
	p.add(103, 100, "9999", 1000) // the stranger's own child

	added, err := Attribute(p, receipt, nil)
	require.Error(t, err)
	assert.Empty(t, added)
	assert.Empty(t, receipt.Members, "a stranger's children are never recorded as ours")
}

// A "descendant" that started before its ancestor cannot be one. This is the
// cheap invariant that keeps a mid-scan pid recycle from widening a receipt.
func TestAttribute_DropsDescendantsOlderThanTheLeader(t *testing.T) {
	p := newFakeProber()
	p.add(100, 1, "5000", 1000)
	p.add(101, 100, "4000", 1000) // impossible: started before the leader
	p.add(102, 100, "5100", 1000)

	receipt, err := Claim(p, ClaimInput{InstanceID: "inst-1", Generation: 1, PanePID: 100})
	require.NoError(t, err)
	added, err := Attribute(p, receipt, nil)
	require.NoError(t, err)
	require.Len(t, added, 1)
	assert.Equal(t, 102, added[0].PID)
}

func TestAttribute_ProviderMismatchIsRefused(t *testing.T) {
	p := newFakeProber()
	p.add(100, 1, "5000", 1000)
	receipt, err := Claim(p, ClaimInput{InstanceID: "inst-1", Generation: 1, PanePID: 100})
	require.NoError(t, err)
	receipt.Provider = "elsewhere"

	_, err = Attribute(p, receipt, nil)
	require.Error(t, err)
}

func TestUnsupportedProviderClaimsNothing(t *testing.T) {
	p := unsupportedProber{}
	_, err := Claim(p, ClaimInput{InstanceID: "inst-1", Generation: 1, PanePID: 100})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrUnsupported),
		"a platform with no start identity must record no ownership at all")

	// And a receipt written elsewhere is never actioned here.
	receipt := liveReceipt("inst-1", Member{PID: 100, StartID: "5000", UID: 1000, Role: RoleLeader})
	sig := newRecordingSignaler()
	report := Reap(p, sig, receipt, fastReapOptions())
	assert.Empty(t, sig.calls())
	assert.Equal(t, VerdictUnknown, report.Verdict)
}

// unsupportedProber mirrors the real no-provider platform build without needing
// to cross-compile: every call fails with ErrUnsupported.
type unsupportedProber struct{}

func (unsupportedProber) Name() string { return "unsupported" }
func (unsupportedProber) BootID() (string, error) {
	return "", fmt.Errorf("%w: boot id", ErrUnsupported)
}
func (unsupportedProber) Inspect(int) (ProcInfo, error) {
	return ProcInfo{}, fmt.Errorf("%w: inspect", ErrUnsupported)
}
func (unsupportedProber) Descendants(ProcInfo) ([]ProcInfo, error) {
	return nil, fmt.Errorf("%w: descendants", ErrUnsupported)
}
