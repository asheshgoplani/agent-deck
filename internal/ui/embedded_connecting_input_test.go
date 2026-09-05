package ui

import (
	"bytes"
	"context"
	"io"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
	tea "github.com/charmbracelet/bubbletea"
)

// These regressions deliberately use only pre-existing concrete types so the
// same file can run on the original PR and independently corrected candidates.
// The optional Install seam selects a generation-aware candidate's installation;
// other candidates retain their existing Activate contract.
type connectingInstallMsg struct{}
type connectingStopMsg struct{ done chan struct{} }

type connectingCapture struct {
	mu     sync.Mutex
	data   bytes.Buffer
	writes chan struct{}
	closed chan struct{}
	once   sync.Once
}

func newConnectingCapture() *connectingCapture {
	return &connectingCapture{writes: make(chan struct{}, 1), closed: make(chan struct{})}
}

func (w *connectingCapture) Write(p []byte) (int, error) {
	w.mu.Lock()
	n, err := w.data.Write(p)
	w.mu.Unlock()
	select {
	case w.writes <- struct{}{}:
	default:
	}
	return n, err
}

func (w *connectingCapture) Close() error {
	w.once.Do(func() { close(w.closed) })
	return nil
}

func (w *connectingCapture) snapshot() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.data.String()
}

func installConnectingCapture(r *SessionInputRouter, generation uint64, w io.WriteCloser) bool {
	if installer, ok := any(r).(interface {
		Install(uint64, io.WriteCloser) bool
	}); ok {
		return installer.Install(generation, w)
	}
	r.Activate(w, terminalCellRect{Width: 80, Height: 24}, 0)
	return true
}

type connectingReadProbe struct {
	*SessionInputRouter
	returned     chan []byte
	terminalRead chan struct{}
	terminalOnce sync.Once
}

func (r *connectingReadProbe) Read(p []byte) (int, error) {
	n, err := r.SessionInputRouter.Read(p)
	if err != nil {
		// readAnsiInputs returns immediately on a Read error, and the
		// cancelreader makes no more fd calls after its delegated Read.
		r.terminalOnce.Do(func() { close(r.terminalRead) })
	}
	if n > 0 {
		select {
		case r.returned <- append([]byte(nil), p[:n]...):
		default:
		}
	}
	return n, err
}

type connectingDecoderModel struct {
	home           *Home
	router         *SessionInputRouter
	writer         *connectingCapture
	enterSeen      chan struct{}
	continueEnter  chan struct{}
	connecting     chan struct{}
	queueObserved  chan struct{}
	allowInstall   chan struct{}
	installed      chan bool
	detached       chan struct{}
	started        bool
	generation     uint64
	dashboardInput []tea.KeyMsg
}

func (m *connectingDecoderModel) Init() tea.Cmd { return nil }
func (m *connectingDecoderModel) View() string  { return "" }

func (m *connectingDecoderModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch key := msg.(type) {
	case connectingStopMsg:
		m.home.exitInsertMode()
		close(key.done)
		return m, nil
	case connectingInstallMsg:
		m.installed <- installConnectingCapture(m.router, m.generation, m.writer)
		return m, nil
	case tea.KeyMsg:
		if !m.started && key.Type == tea.KeyEnter {
			m.started = true
			close(m.enterSeen)
			<-m.continueEnter
			// Exercise the actual Home dispatcher and its input acknowledgement.
			// Only execution of the asynchronous PTY command is held at this seam.
			_, _ = m.home.Update(msg)
			m.generation = m.home.embeddedGeneration
			close(m.connecting)
			return m, func() tea.Msg {
				<-m.allowInstall
				return connectingInstallMsg{}
			}
		}
		m.dashboardInput = append(m.dashboardInput, key)
		_, _ = m.home.Update(msg)
		if key.Type == tea.KeyCtrlG && m.queueObserved != nil {
			// Only the raw router emits this private signal for Ctrl+Alt+B.
			// Its preceding flush proves all earlier pane bytes were accepted.
			close(m.queueObserved)
			m.queueObserved = nil
		}
		if key.Type == tea.KeyCtrlQ && m.detached != nil {
			close(m.detached)
			m.detached = nil
		}
	}
	return m, nil
}

func newConnectingDecoder(t *testing.T) (*connectingDecoderModel, *tea.Program, *os.File, <-chan []byte) {
	t.Helper()
	setIsolatedAgentDeckDir(t)
	home, _, _ := armHomeWithOneSession(t)
	home.embeddedLayout = true
	home.sessionExists = func(*session.Instance) bool { return true }
	readFile, writeFile, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	router := NewSessionInputRouter(readFile)
	home.sessionInput = router
	ctx, cancel := context.WithCancel(context.Background())
	home.ctx = ctx
	model := &connectingDecoderModel{
		home: home, router: router, writer: newConnectingCapture(),
		enterSeen: make(chan struct{}), continueEnter: make(chan struct{}),
		connecting: make(chan struct{}), queueObserved: make(chan struct{}),
		allowInstall: make(chan struct{}), installed: make(chan bool, 1),
		detached: make(chan struct{}),
	}
	probe := &connectingReadProbe{SessionInputRouter: router, returned: make(chan []byte, 8), terminalRead: make(chan struct{})}
	program := tea.NewProgram(model, tea.WithInput(probe), tea.WithOutput(io.Discard),
		tea.WithoutRenderer(), tea.WithoutSignalHandler(), tea.WithContext(ctx))
	done := make(chan struct{})
	go func() {
		_, _ = program.Run()
		close(done)
	}()
	t.Cleanup(func() {
		// Release every test gate even after an assertion fails on the baseline.
		for _, ch := range []chan struct{}{model.continueEnter, model.allowInstall} {
			select {
			case <-ch:
			default:
				close(ch)
			}
		}
		defer cancel()
		defer writeFile.Close()
		// Stop Home's session on its own update goroutine before releasing
		// stdin. EOF then proves the input loop cannot call Fd again. Run
		// alone is insufficient: Bubble Tea skips joining on context kill
		// and its graceful read-loop wait has a 500 ms timeout.
		stopped := make(chan struct{})
		go program.Send(connectingStopMsg{done: stopped})
		select {
		case <-stopped:
		case <-done:
			t.Error("decoder stopped before input cleanup could be joined")
			return // retain the read fd rather than race an unjoined reader
		case <-time.After(5 * time.Second):
			t.Error("decoder did not process session cleanup")
			return
		}
		_ = writeFile.Close()
		select {
		case <-probe.terminalRead:
		case <-time.After(5 * time.Second):
			t.Error("decoder reader did not finish at EOF")
			return // keep the read fd alive if reader completion is unknown
		}
		go program.Quit()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("decoder program did not stop")
		}
		_ = readFile.Close()
	})
	return model, program, writeFile, probe.returned
}

func awaitConnectingSignal(t *testing.T, ch <-chan struct{}, description string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal(description)
	}
}

func TestEmbeddedConnectingInputRealDecoderSameRead(t *testing.T) {
	for name, enter := range map[string]string{
		"legacy": "\r", "CSI-u": "\x1b[13u", "modifyOtherKeys": "\x1b[27;1;13~",
	} {
		t.Run(name, func(t *testing.T) {
			model, _, input, returned := newConnectingDecoder(t)
			queued := model.queueObserved
			want := "界🙂e\u0301\x1bOP\x1b[1;5A\x1b[13;2u" + bracketedPasteStart + "raw\x11\x1b[13;2u\r" + bracketedPasteEnd + "\r"
			// A single OS write is critical: Enter and its suffix are available
			// before Home can begin the asynchronous connection.
			if _, err := io.WriteString(input, enter+want+"\x1b[98;7u"); err != nil {
				t.Fatal(err)
			}
			awaitConnectingSignal(t, model.enterSeen, "real decoder did not deliver Enter")
			select {
			case first := <-returned:
				if string(first) != "\r" {
					t.Fatalf("activation read exposed its raw suffix before Home.Update: %q", first)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("missing activation read")
			}
			close(model.continueEnter)
			awaitConnectingSignal(t, model.connecting, "Home did not begin connecting")
			awaitConnectingSignal(t, queued, "raw connecting input never reached its ordered flush marker")
			if got := model.writer.snapshot(); got != "" {
				t.Fatalf("bytes delivered before installation: %q", got)
			}
			close(model.allowInstall)
			select {
			case ok := <-model.installed:
				if !ok {
					t.Fatal("current generation was not installed")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("installation did not run")
			}
			deadline := time.After(5 * time.Second)
			for model.writer.snapshot() != want {
				select {
				case <-model.writer.writes:
				case <-deadline:
					t.Fatalf("PTY bytes = %q, want exact suffix once %q", model.writer.snapshot(), want)
				}
			}
			// The writer's queue marker above also gives a happens-before edge
			// for these dispatcher observations. Only the private signal belongs
			// to Bubble Tea; none of the pane text may have been decoded there.
			if len(model.dashboardInput) != 1 || model.dashboardInput[0].Type != tea.KeyCtrlG {
				t.Fatalf("pane suffix escaped into dashboard messages: %#v", model.dashboardInput)
			}
		})
	}
}

func TestEmbeddedConnectingInputCancelledGenerationDoesNotReplay(t *testing.T) {
	model, _, input, _ := newConnectingDecoder(t)
	queued, detached := model.queueObserved, model.detached
	close(model.continueEnter)
	if _, err := io.WriteString(input, "\rdo-not-submit\r\x1b[98;7u"); err != nil {
		t.Fatal(err)
	}
	awaitConnectingSignal(t, queued, "connecting prefix was not queued")
	if _, err := io.WriteString(input, "\x11"); err != nil {
		t.Fatal(err)
	}
	awaitConnectingSignal(t, detached, "Ctrl+Q did not cancel connecting input")
	if got := model.writer.snapshot(); got != "" {
		t.Fatalf("cancelled input reached a writer: %q", got)
	}
	close(model.allowInstall)
	select {
	case installed := <-model.installed:
		if installed {
			t.Fatal("cancelled generation accepted a late writer")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("late installation did not finish")
	}
}

func TestEmbeddedConnectingInputSwitcherRequiresExplicitCommit(t *testing.T) {
	for _, embedded := range []bool{false, true} {
		home := &Home{sessionSwitcher: NewSessionSwitcher()}
		home.sessionSwitcher.embeddedOnAttach = embedded
		cmd := home.armSwitcherCommit()
		if embedded && cmd != nil {
			t.Fatal("embedded switcher still arms an asynchronous input handoff")
		}
		if !embedded && cmd == nil {
			t.Fatal("classic switcher lost its existing idle-commit timer")
		}
	}
}

// Consuming Enter without dispatching it deliberately leaves its receipt
// outstanding. Shutdown must not depend on Home eventually acknowledging it.
type connectingUnacknowledgedModel struct{ enter chan struct{} }

func (m connectingUnacknowledgedModel) Init() tea.Cmd { return nil }
func (m connectingUnacknowledgedModel) View() string  { return "" }
func (m connectingUnacknowledgedModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if key, ok := msg.(tea.KeyMsg); ok && key.Type == tea.KeyEnter {
		close(m.enter)
	}
	return m, nil
}

type connectingShutdownProbe struct {
	*SessionInputRouter
	reads          int
	secondRead     chan struct{}
	secondReturned chan struct{}
	allowReturn    chan struct{}
	returned       chan struct{}
}

func (p *connectingShutdownProbe) Read(buf []byte) (int, error) {
	p.reads++
	if p.reads == 2 {
		close(p.secondRead)
		n, err := p.SessionInputRouter.Read(buf)
		close(p.secondReturned)
		// Hold only the fixture's return, after the production reader has
		// demonstrably yielded. Shutdown can now mark cancelreader cancelled
		// before it can start another fd-using read.
		<-p.allowReturn
		close(p.returned)
		return n, err
	}
	return p.SessionInputRouter.Read(buf)
}

func TestEmbeddedConnectingInputProgramShutdownWithPendingReceipt(t *testing.T) {
	readFile, writeFile, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	router := NewSessionInputRouter(readFile)
	defer router.Deactivate()
	probe := &connectingShutdownProbe{SessionInputRouter: router, secondRead: make(chan struct{}), secondReturned: make(chan struct{}), allowReturn: make(chan struct{}), returned: make(chan struct{})}
	model := connectingUnacknowledgedModel{enter: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	program := tea.NewProgram(model, tea.WithInput(probe), tea.WithOutput(io.Discard), tea.WithoutRenderer(), tea.WithoutSignalHandler(), tea.WithContext(ctx))
	done := make(chan struct{})
	go func() { _, _ = program.Run(); close(done) }()
	var releaseOnce sync.Once
	t.Cleanup(func() {
		cancel()
		router.Deactivate()
		_ = writeFile.Close()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("cancelled program did not stop during cleanup")
			return
		}
		releaseOnce.Do(func() { close(probe.allowReturn) })
		select {
		case <-probe.returned:
			// The held read was the last possible fd-using operation:
			// cancelreader is cancelled before its next Read can begin.
			_ = readFile.Close()
		case <-time.After(5 * time.Second):
			t.Error("reader completion unknown; retaining its read fd")
		}
	})
	if _, err := io.WriteString(writeFile, "\rx"); err != nil {
		t.Fatal(err)
	}
	awaitConnectingSignal(t, model.enter, "decoder did not consume Enter")
	awaitConnectingSignal(t, probe.secondRead, "decoder did not reach pending receipt")
	cancel()
	// No Deactivate, receipt acknowledgement or file close may be needed to
	// release the internal reader before Program.Run returns.
	awaitConnectingSignal(t, probe.secondReturned, "internal receipt wait survived program cancellation")
	awaitConnectingSignal(t, done, "program shutdown did not complete")
	releaseOnce.Do(func() { close(probe.allowReturn) })
	awaitConnectingSignal(t, probe.returned, "last fixture read did not return")
}

// Portable diagnostic, not a performance assertion: run this same file on
// contributor and corrected heads and retain elapsed times with the gate logs.
func TestEmbeddedConnectingInputPasteThroughput(t *testing.T) {
	readFile, writeFile, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer readFile.Close()
	defer writeFile.Close()
	router := NewSessionInputRouter(readFile)
	defer router.Deactivate()
	w := newConnectingCapture()
	router.Activate(w, terminalCellRect{Width: 80, Height: 24}, 0)
	want := bracketedPasteStart + string(bytes.Repeat([]byte("abcdefgh"), 1024)) + bracketedPasteEnd
	start := time.Now()
	writeDone := make(chan error, 1)
	go func() { _, err := io.WriteString(writeFile, want+"\x1b[98;7u"); writeDone <- err }()
	event := make(chan []byte, 1)
	go func() {
		buf := make([]byte, 256)
		for {
			n, err := router.Read(buf)
			if n > 0 || err != nil {
				event <- append([]byte(nil), buf[:n]...)
				return
			}
		}
	}()
	select {
	case got := <-event:
		if !bytes.Equal(got, []byte{0x07}) {
			t.Fatalf("flush signal = %q", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("8 KiB paste did not finish")
	}
	if err := <-writeDone; err != nil {
		t.Fatal(err)
	}
	deadline := time.After(5 * time.Second)
	for w.snapshot() != want {
		select {
		case <-w.writes:
		case <-deadline:
			t.Fatalf("paste bytes = %d, want %d", len(w.snapshot()), len(want))
		}
	}
	t.Logf("8 KiB bracketed paste pipe-to-writer elapsed: %s", time.Since(start))
}
