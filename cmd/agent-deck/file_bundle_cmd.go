package main

import (
	"archive/tar"
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

const fileBundleMaxBytes int64 = 20 << 20
const fileBundleUsage = "Usage: agent-deck file bundle <dir|file> --session <id>"

func handleFile(profile string, args []string) {
	if err := runFileBundle(profile, args, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func runFileBundle(_ string, args []string, out, diagnostics io.Writer) error {
	if len(args) == 1 && helpRequested(args) {
		fmt.Fprintln(out, fileBundleUsage)
		return nil
	}
	if len(args) == 0 || args[0] != "bundle" {
		return errors.New(fileBundleUsage)
	}
	flags := flag.NewFlagSet("file bundle", flag.ContinueOnError)
	flags.SetOutput(diagnostics)
	sessionID := flags.String("session", "", "session routing context (required)")
	flags.Usage = func() { fmt.Fprintln(diagnostics, fileBundleUsage) }
	if err := flags.Parse(normalizeArgs(flags, args[1:])); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 1 || *sessionID == "" {
		return errors.New(fileBundleUsage)
	}
	// The session is routing context, not an access-control boundary. Avoid
	// opening the catalog: live SQLite reads can create coordination files,
	// while immutable reads can miss newly committed sessions in the WAL.
	return writeFileBundle(out, flags.Arg(0), fileBundleMaxBytes)
}

// writeFileBundle emits an uncompressed tar of the directory, or the file's
// entire parent directory so relative assets retain their paths. All symlinks
// and special files are rejected. No temporary files or remote writes occur.
// Buffering up to the cap also keeps rejected requests off the binary stream.
func writeFileBundle(out io.Writer, target string, maxBytes int64) error {
	absolute, err := filepath.Abs(target)
	if err != nil {
		return err
	}
	info, err := os.Lstat(absolute)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("bundle refuses symlink: %s", target)
	}
	if !info.IsDir() {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("bundle refuses special file: %s", target)
		}
		absolute = filepath.Dir(absolute)
	}
	root, err := os.OpenRoot(absolute)
	if err != nil {
		return err
	}
	defer root.Close()
	var buffer bytes.Buffer
	limited := &bundleLimitWriter{out: &buffer, remaining: maxBytes}
	archive := tar.NewWriter(limited)
	err = fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if name == "." {
			return nil
		}
		info, err := root.Lstat(name)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("bundle refuses symlink: %s", name)
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return fmt.Errorf("bundle refuses special file: %s", name)
		}
		if info.Size() > maxBytes && !info.IsDir() {
			return fmt.Errorf("bundle exceeds %d byte limit", maxBytes)
		}
		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		header.Name = name
		if info.IsDir() {
			header.Name += "/"
		}
		if err := archive.WriteHeader(header); err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		// Root.Open enforces containment even if an entry is replaced by a symlink
		// after Lstat. SameFile additionally rejects replacement within the root.
		file, err := root.Open(name)
		if err != nil {
			return err
		}
		defer file.Close()
		opened, err := file.Stat()
		if err != nil {
			return err
		}
		if !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
			return fmt.Errorf("bundle file changed during read: %s", name)
		}
		_, err = io.CopyN(archive, file, info.Size())
		return err
	})
	if err != nil {
		return err
	}
	if err := archive.Close(); err != nil {
		return err
	}
	_, err = io.Copy(out, &buffer)
	return err
}

type bundleLimitWriter struct {
	out       io.Writer
	remaining int64
}

func (w *bundleLimitWriter) Write(data []byte) (int, error) {
	if int64(len(data)) > w.remaining {
		return 0, errors.New("bundle exceeds archive byte limit")
	}
	n, err := w.out.Write(data)
	w.remaining -= int64(n)
	return n, err
}
