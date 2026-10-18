package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/filelinks"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// `agent-deck file read` and `file roots` are the CLI side of the web
// terminal's file links (issue #2676): the same [web] file_link_roots, the
// same containment, size cap and typing as GET /api/files. Forwarded with
// `agent-deck remote <name> file read ...`, they read the remote's files under
// the remote's own roots.
const fileReadUsage = "Usage: agent-deck file read <absolute-path> [--json]"
const fileRootsUsage = "Usage: agent-deck file roots [--json]"

const fileUsage = fileBundleUsage + "\n" + fileReadUsage + "\n" + fileRootsUsage

// fileReadJSON is `file read --json`. Text comes back as-is; anything that
// is not served inline (binary) is base64.
type fileReadJSON struct {
	Path        string `json:"path"`
	RealPath    string `json:"realPath"`
	Size        int64  `json:"size"`
	ContentType string `json:"contentType"`
	Inline      bool   `json:"inline"`
	Encoding    string `json:"encoding"`
	Content     string `json:"content"`
}

type fileRootsJSON struct {
	Enabled bool                `json:"enabled"`
	Roots   []string            `json:"roots"`
	Refused []filelinks.Refused `json:"refused"`
}

// fileErrorJSON keeps a refusal machine-readable under --json.
type fileErrorJSON struct {
	Error string `json:"error"`
	Code  string `json:"code"`
}

func fileLinkErrorCode(err error) string {
	switch {
	case errors.Is(err, filelinks.ErrDisabled):
		return "disabled"
	case errors.Is(err, filelinks.ErrNotAbsolute):
		return "not_absolute"
	case errors.Is(err, filelinks.ErrOutside):
		return "outside_roots"
	case errors.Is(err, filelinks.ErrNotFound):
		return "not_found"
	case errors.Is(err, filelinks.ErrNotRegular):
		return "not_regular"
	case errors.Is(err, filelinks.ErrTooLarge):
		return "too_large"
	}
	return "error"
}

// errFileJSONWritten tells handleFile the refusal was already printed as JSON.
var errFileJSONWritten = errors.New("file: error written as JSON")

func runFileRead(roots []string, args []string, out, diagnostics io.Writer) error {
	flags := flag.NewFlagSet("file read", flag.ContinueOnError)
	flags.SetOutput(helpOutput(args, out, diagnostics))
	jsonOut := flags.Bool("json", false, "print {path, realPath, size, contentType, inline, encoding, content}")
	flags.Usage = func() {
		fmt.Fprintln(flags.Output(), fileReadUsage)
		fmt.Fprintln(flags.Output(), "Prints a file under [web] file_link_roots, with the same rules the web terminal's file links use.")
		flags.PrintDefaults()
	}
	if err := flags.Parse(normalizeArgs(flags, args)); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 1 {
		return errors.New(fileReadUsage)
	}
	f, err := filelinks.Open(roots, flags.Arg(0))
	if err != nil {
		if *jsonOut {
			_ = json.NewEncoder(out).Encode(fileErrorJSON{Error: err.Error(), Code: fileLinkErrorCode(err)})
			return errFileJSONWritten
		}
		return err
	}
	defer f.Close()
	if !*jsonOut {
		_, err := io.Copy(out, f.Reader())
		return err
	}
	body, err := io.ReadAll(f.Reader())
	if err != nil {
		return err
	}
	resp := fileReadJSON{Path: flags.Arg(0), RealPath: f.Path, Size: f.Size, ContentType: f.ContentType, Inline: f.Inline}
	if strings.HasPrefix(f.ContentType, "text/") {
		resp.Encoding, resp.Content = "utf-8", string(body)
	} else {
		resp.Encoding, resp.Content = "base64", base64.StdEncoding.EncodeToString(body)
	}
	return json.NewEncoder(out).Encode(resp)
}

func runFileRoots(roots []string, refused []filelinks.Refused, args []string, out, diagnostics io.Writer) error {
	flags := flag.NewFlagSet("file roots", flag.ContinueOnError)
	flags.SetOutput(helpOutput(args, out, diagnostics))
	jsonOut := flags.Bool("json", false, "print {enabled, roots, refused}")
	flags.Usage = func() {
		fmt.Fprintln(flags.Output(), fileRootsUsage)
		fmt.Fprintln(flags.Output(), "Lists the [web] file_link_roots directories file links may read.")
		flags.PrintDefaults()
	}
	if err := flags.Parse(normalizeArgs(flags, args)); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return errors.New(fileRootsUsage)
	}
	if roots == nil {
		roots = []string{}
	}
	if refused == nil {
		refused = []filelinks.Refused{}
	}
	if *jsonOut {
		return json.NewEncoder(out).Encode(fileRootsJSON{Enabled: len(roots) > 0, Roots: roots, Refused: refused})
	}
	for _, r := range refused {
		fmt.Fprintf(diagnostics, "Warning: [web] file_link_roots entry %q ignored: %s\n", r.Entry, r.Reason)
	}
	if len(roots) == 0 {
		_, err := fmt.Fprintln(out, "File links are off: set [web] file_link_roots in config.toml. Clicking a file link copies its path.")
		return err
	}
	for _, root := range roots {
		if _, err := fmt.Fprintln(out, root); err != nil {
			return err
		}
	}
	return nil
}

// runFileCommand dispatches `agent-deck file <bundle|read|roots>`.
func runFileCommand(profile string, args []string, out, diagnostics io.Writer) error {
	if len(args) > 0 {
		switch args[0] {
		case "read":
			return runFileRead(session.GetWebFileLinkRoots(), args[1:], out, diagnostics)
		case "roots":
			roots, refused := session.GetWebFileLinkRootsChecked()
			return runFileRoots(roots, refused, args[1:], out, diagnostics)
		}
	}
	if len(args) == 1 && (helpRequested(args) || args[0] == "help") {
		fmt.Fprintln(out, fileUsage)
		return nil
	}
	return runFileBundle(profile, args, out, diagnostics)
}
