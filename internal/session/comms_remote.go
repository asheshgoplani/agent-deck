package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/comms"
	"github.com/asheshgoplani/agent-deck/internal/events"
)

// Remote records into the local ledger (P3, docs/comms.md "Remote"). A
// remote's ledger records travel on the talkback round trip the daemon
// already makes (`inbox export --after -`): the cursor carries a `_comms`
// position on the remote's ledger, the remote answers the records past it
// that are addressed to sessions it does not host (the cross-host parent)
// and the local daemon imports them with the remote as their origin. An
// older peer on either side simply omits the key and nothing changes.
//
// Durable hand-off: the pulled batch is written to a local import spool
// (tmp, fsync, rename) before the position advances, and the daemon, the
// only ledger writer, imports it (idempotent on origin store + key), so a
// crash anywhere repeats a batch and never loses one.
//
// Clocks: a record's own times are the origin host's. The puller measures
// the offset between the hosts on the round trip that fetched the batch
// (the remote's now against the midpoint of the local send and receive)
// with an uncertainty of half that round trip, and stores the cross-host
// latency (origin signal to local import) only when the estimate is larger
// than its uncertainty; otherwise the latency is unknown, never a
// precise-looking wrong (or negative) number.

// remoteCommsBatch bounds the records one round trip ships; the next tick
// continues from the position the batch reached.
const remoteCommsBatch = 500

// remoteCommsBytes bounds the records one round trip ships by size.
const remoteCommsBytes = 1 << 20

// RemoteCommsCursor is the `_comms` part of a talkback cursor: the position
// on the remote's ledger (its store, epoch and the last cursor covered).
type RemoteCommsCursor struct {
	Store string `json:"store,omitempty"`
	Epoch int64  `json:"epoch,omitempty"`
	After uint64 `json:"after"`
}

// RemoteCommsExport is the `comms` part of a talkback export.
type RemoteCommsExport struct {
	Ledger  bool             `json:"ledger"` // false: the remote has no ledger (switch off there)
	Store   string           `json:"store,omitempty"`
	Epoch   int64            `json:"epoch,omitempty"`
	NowMS   int64            `json:"now_ms"` // the remote's clock when it answered
	After   uint64           `json:"after"`
	Through uint64           `json:"through"`
	More    bool             `json:"more,omitempty"`
	Reset   bool             `json:"reset,omitempty"` // the position named another store or epoch; restarted
	Records []comms.Exported `json:"records"`
}

// exportCommsAfter answers the `_comms` position of a talkback cursor on
// this host: the ledger records after it that are addressed to a session
// this host does not have (scan progress moves over everything else).
func exportCommsAfter(profile string, c RemoteCommsCursor, now time.Time) RemoteCommsExport {
	out, ok := exportCommsBatch(profile, c, now)
	if !ok {
		// Anything uncertain answers "no ledger": the puller keeps its
		// position and asks again, never saving one ahead of what it holds.
		return RemoteCommsExport{NowMS: time.Now().UnixMilli(), After: c.After, Through: c.After, Records: []comms.Exported{}}
	}
	return out
}

func exportCommsBatch(profile string, c RemoteCommsCursor, now time.Time) (RemoteCommsExport, bool) {
	out := RemoteCommsExport{NowMS: now.UnixMilli(), After: c.After, Through: c.After, Records: []comms.Exported{}}
	if !CommsLedgerEnabled() {
		return out, false // the switch is off on this host: nothing is exported
	}
	dir, err := comms.Dir(profile) // the export's profile (#2539), not the process's
	if err != nil {
		return out, false
	}
	r, err := comms.OpenReaderAt(dir)
	if err != nil {
		return out, false
	}
	defer r.Close()
	out.Ledger, out.Store, out.Epoch = true, r.Store.ID, r.Store.Epoch
	after := events.Cursor(c.After)
	if c.Store != r.Store.ID || c.Epoch != r.Store.Epoch {
		// First pull, or the remote ledger was reset or restored: start at
		// the talkback horizon (what the receiver could still use).
		out.Reset = c.Store != ""
		if after, err = r.Bus.CursorBefore(now.Add(-remoteTalkbackHorizon)); err != nil {
			return out, false
		}
		out.After, out.Through = uint64(after), uint64(after)
	}
	// Which sessions live here decides what crosses: an unreadable registry
	// exports nothing (fail closed), never everything.
	local, ok := localSessionIDs(profile)
	if !ok {
		return out, false
	}
	read, through, err := comms.Export(r.Bus, after, remoteCommsBatch)
	if errors.Is(err, events.ErrCursorTooOld) {
		oldest, oerr := r.Bus.Oldest()
		if oerr != nil {
			return out, false
		}
		out.Reset, after = true, oldest-1
		out.After = uint64(after)
		read, through, err = comms.Export(r.Bus, after, remoteCommsBatch)
	}
	if err != nil {
		return out, false
	}
	size := 0
	for _, e := range read {
		if size > remoteCommsBytes {
			through = e.Cursor - 1 // the next round trip continues here
			break
		}
		if crossesHost(e.Record, local) {
			out.Records = append(out.Records, e)
			size += len(e.Record.Text) + 512
		}
	}
	out.Through = uint64(through)
	if end, _, err := r.Bus.Ends(); err == nil {
		out.More = through < end
	}
	out.NowMS = time.Now().UnixMilli()
	return out, true
}

// crossesHost reports whether a record is news for another host: a message
// kind (never a wake or call measurement row), not pulled from yet another
// host, and addressed to a session this host does not have.
func crossesHost(r comms.Record, local map[string]bool) bool {
	switch r.Kind {
	case comms.KindTurn, comms.KindStatus, comms.KindSend, comms.KindDelivery, comms.KindHuman, comms.KindError:
	default:
		return false
	}
	if r.Origin != "" || r.Tier == comms.TierNoise {
		return false
	}
	for _, to := range r.To {
		if to != "" && to != UnownedInboxID && !local[to] {
			return true
		}
	}
	return false
}

func localSessionIDs(profile string) (map[string]bool, bool) {
	out := map[string]bool{}
	storage, err := NewStorageWithProfile(profile)
	if err != nil {
		return nil, false
	}
	defer storage.Close()
	instances, _, err := storage.LoadWithGroups()
	if err != nil {
		return nil, false
	}
	for _, inst := range instances {
		if inst != nil {
			out[inst.ID] = true
		}
	}
	return out, true
}

// --- puller side -------------------------------------------------------------

func remoteCommsCursorPath(remote, profile string) string {
	return filepath.Join(RemoteCursorDir(), sanitizeInboxName(remote)+"."+sanitizeInboxName(profile)+"._comms.json")
}

// loadRemoteCommsCursor returns the position on remote's ledger the local
// profile's ledger has durably accepted (zero when none).
func loadRemoteCommsCursor(remote, profile string) RemoteCommsCursor {
	var c RemoteCommsCursor
	data, err := os.ReadFile(remoteCommsCursorPath(remote, profile))
	if err == nil {
		_ = json.Unmarshal(data, &c)
	}
	return c
}

func saveRemoteCommsCursor(remote, profile string, c RemoteCommsCursor) error {
	data, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return writeFileDurable(remoteCommsCursorPath(remote, profile), data, 0o600)
}

// commsImport is one pulled batch waiting in the import spool.
type commsImport struct {
	Remote   string           `json:"remote"`
	T0       int64            `json:"t0"`        // local ms before the round trip
	T1       int64            `json:"t1"`        // local ms after it
	RemoteMS int64            `json:"remote_ms"` // the remote's clock when it answered
	Records  []comms.Exported `json:"records"`
	path     string
}

// commsImportMaxBytes bounds one import spool file.
const commsImportMaxBytes = 16 << 20

func commsImportDir(profile string) string {
	return filepath.Join(runtimeDirOrTemp(filepath.Join("comms", "import")), sanitizeInboxName(profile))
}

// acceptRemoteComms makes a pulled batch durable locally (the import spool)
// and then advances the position on the remote's ledger. Errors leave the
// position where it was, so the next round trip repeats the batch.
func acceptRemoteComms(remote, profile string, exp *RemoteCommsExport, t0, t1 time.Time) error {
	if exp == nil || !exp.Ledger || !CommsLedgerEnabled() {
		return nil
	}
	if len(exp.Records) > 0 {
		data, err := json.Marshal(commsImport{Remote: remote, T0: t0.UnixMilli(), T1: t1.UnixMilli(), RemoteMS: exp.NowMS, Records: exp.Records})
		if err != nil {
			return err
		}
		dir := commsImportDir(profile)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
		if err := writeFileDurable(filepath.Join(dir, comms.NewID(t1)+".json"), data, 0o600); err != nil {
			return err
		}
	}
	return saveRemoteCommsCursor(remote, profile, RemoteCommsCursor{Store: exp.Store, Epoch: exp.Epoch, After: exp.Through})
}

// crossHostLatency estimates origin-signal-to-local-import across two
// clocks. offset = remote clock minus local clock, measured at the
// midpoint of the round trip [t0, t1]; uncertainty = half the round trip
// plus 1 ms of clock granularity. ok is false when the estimate is not
// larger than its uncertainty (latency unknown).
func crossHostLatency(signalRemoteMS, importLocalMS, remoteNowMS, t0, t1 int64) (latency, uncertainty int64, ok bool) {
	if signalRemoteMS <= 0 || remoteNowMS <= 0 || t1 < t0 {
		return 0, 0, false
	}
	offset := remoteNowMS - (t0+t1)/2
	uncertainty = (t1-t0)/2 + 1
	latency = importLocalMS - (signalRemoteMS - offset)
	if latency <= uncertainty {
		return 0, 0, false
	}
	return latency, uncertainty, true
}

// hasCommsImports reports whether pulled batches wait for this profile.
func hasCommsImports(profile string) bool {
	return len(commsImportFiles(commsImportDir(profile))) > 0
}

// importCommsSpool commits one pulled batch waiting for this profile into
// its ledger per pass (a batch is up to 500 synchronous commits; a backlog
// drains over the next polls instead of stalling one). Only records
// addressed to one of this profile's own sessions are kept: a remote is
// pulled per parent, and two local profiles may pull the same remote. A
// batch that cannot be read is set aside as .rejected; one whose import
// fails (the ledger cannot write) is kept and retried after the reopen.
func (d *TransitionDaemon) importCommsSpool(l *comms.Ledger, profile string, byID map[string]*Instance) bool {
	dir := commsImportDir(profile)
	names := commsImportFiles(dir)
	if len(names) == 0 {
		return true
	}
	path := filepath.Join(dir, names[0])
	batch, err := readCommsImport(path)
	if err != nil {
		commsLog.Warn("comms_import_rejected", slog.String("path", path), slog.String("error", err.Error()))
		_ = os.Rename(path, path+".rejected")
		return true
	}
	now := time.Now().UnixMilli()
	kept := batch.Records[:0]
	for _, e := range batch.Records {
		r := &e.Record
		if !addressedToAny(r.To, byID) {
			continue
		}
		r.TImport = now
		signal := r.TSignal
		if signal == 0 {
			signal = r.TRecord
		}
		if lat, unc, ok := crossHostLatency(signal, now, batch.RemoteMS, batch.T0, batch.T1); ok {
			r.XLatencyMS, r.XErrMS = lat, unc
		}
		kept = append(kept, e)
	}
	if _, err := l.Import(batch.Remote, kept); err != nil {
		// Import already skips what can never commit (invalid records,
		// conflicts, echoes): what is left is the ledger failing to write,
		// which is transient. The batch is kept (its position on the remote
		// already moved past it, so it is the only copy).
		commsLog.Warn("comms_import_failed", slog.String("remote", batch.Remote), slog.String("error", err.Error()))
		return false
	}
	_ = os.Remove(path)
	return true
}

func addressedToAny(to []string, byID map[string]*Instance) bool {
	for _, id := range to {
		if byID[id] != nil {
			return true
		}
	}
	return false
}

// commsImportFiles lists the waiting batches, oldest first.
func commsImportFiles(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names
}

// pruneCommsImports removes rejected batches older than a week and temp
// files a crashed writer left (older than an hour).
func pruneCommsImports(profile string) {
	dir := commsImportDir(profile)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if info, err := e.Info(); err == nil && !e.IsDir() &&
			((strings.HasSuffix(e.Name(), ".rejected") && time.Since(info.ModTime()) > 7*24*time.Hour) ||
				(strings.HasSuffix(e.Name(), ".tmp") && time.Since(info.ModTime()) > time.Hour)) {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

func readCommsImport(path string) (commsImport, error) {
	var batch commsImport
	f, err := os.Open(path) // #nosec G304 -- our own runtime dir
	if err != nil {
		return batch, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, commsImportMaxBytes+1))
	if err != nil {
		return batch, err
	}
	if len(data) > commsImportMaxBytes {
		return batch, fmt.Errorf("import batch over %d bytes", commsImportMaxBytes)
	}
	if err := json.Unmarshal(data, &batch); err != nil {
		return batch, err
	}
	if strings.TrimSpace(batch.Remote) == "" {
		return batch, errors.New("import batch without a remote")
	}
	batch.path = path
	return batch, nil
}
