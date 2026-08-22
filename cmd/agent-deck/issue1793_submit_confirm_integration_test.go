package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// TestIssue1793SubmitConfirmBusyMatrix is a pane-level fixture, not an argv
// test. The fake composers run in raw mode, render Claude/Codex prompt lines,
// deliberately consume the first CR after every body, and append only accepted
// turns to an inbox. This deterministically models the observed TUI race.
func TestIssue1793SubmitConfirmBusyMatrix(t *testing.T) {
	skipUnlessIntegration(t)
	const fixture = `import os,sys,termios,tty
glyph,out=sys.argv[1:3]
fd=sys.stdin.fileno(); old=termios.tcgetattr(fd); tty.setraw(fd)
buf=bytearray(); swallowed=False
def draw():
 os.write(1,("\x1b[999B\r\x1b[K"+glyph+" "+buf.decode(errors="replace")+"\x1b[K").encode())
os.write(1,b"BUSY mid-turn\n")
draw()
try:
 while True:
  c=os.read(fd,1)
  if not c: break
  if c==b"\r":
   if not swallowed:
    swallowed=True; draw(); continue
   with open(out,"ab") as f: f.write(bytes(buf)+b"\n")
   os.write(1,("\rGOT: "+buf.decode(errors="replace")+"\x1b[K\n").encode())
   buf.clear(); swallowed=False; draw(); continue
  buf.extend(c); draw()
finally: termios.tcsetattr(fd,termios.TCSADRAIN,old)
`

	for _, tc := range []struct{ name, glyph string }{{"claude", "❯"}, {"codex", "›"}} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			script := filepath.Join(dir, "composer.py")
			inbox := filepath.Join(dir, "inbox")
			if err := os.WriteFile(script, []byte(fixture), 0o600); err != nil {
				t.Fatal(err)
			}
			sess := tmux.NewSession("submit-confirm-"+tc.name, dir)
			if err := sess.Start(fmt.Sprintf("python3 %q %q %q", script, tc.glyph, inbox)); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = sess.Kill() })
			time.Sleep(300 * time.Millisecond)

			const sends = 12
			for i := 0; i < sends; i++ {
				msg := fmt.Sprintf("NONCE-%02d-submit-confirm", i)
				delivery, err := sendWithRetryTarget(sess, msg, true, sendRetryOptions{maxRetries: 10, checkDelay: 200 * time.Millisecond, verifyDelivery: true})
				if err != nil || delivery != deliverySubmitted {
					if pane, captureErr := sess.CapturePaneFresh(); captureErr == nil {
						t.Logf("pane on failure:\n%s", pane)
					}
					t.Fatalf("send %d: delivery=%q err=%v", i, delivery, err)
				}
			}
			got, err := os.ReadFile(inbox)
			if err != nil {
				t.Fatal(err)
			}
			var want strings.Builder
			for i := 0; i < sends; i++ {
				fmt.Fprintf(&want, "NONCE-%02d-submit-confirm\n", i)
			}
			if string(got) != want.String() {
				t.Fatalf("inbox is not exactly-once/in-order\nwant:\n%s\ngot:\n%s", want.String(), got)
			}
		})
	}
}
