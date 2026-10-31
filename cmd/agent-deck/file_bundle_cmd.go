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
	"path"
	"path/filepath"
	"sort"
	"strings"
)

const fileBundleMaxBytes int64 = 20 << 20
const fileBundleUsage = "Usage: agent-deck file bundle <dir|file> --session <id>"

func handleFile(profile string, args []string) {
	if err := runFileCommand(profile, args, os.Stdout, os.Stderr); err != nil {
		if !errors.Is(err, errFileJSONWritten) {
			fmt.Fprintln(os.Stderr, err)
		}
		os.Exit(1)
	}
}

func runFileBundle(_ string, args []string, out, diagnostics io.Writer) error {
	if len(args) == 1 && (helpRequested(args) || args[0] == "help") {
		fmt.Fprintln(out, fileBundleUsage)
		return nil
	}
	if len(args) == 0 || args[0] != "bundle" {
		return errors.New(fileUsage)
	}
	flags := flag.NewFlagSet("file bundle", flag.ContinueOnError)
	flags.SetOutput(helpOutput(args[1:], out, diagnostics))
	sessionID := flags.String("session", "", "session routing context (required)")
	flags.Usage = func() { fmt.Fprintln(flags.Output(), fileBundleUsage) }
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

// writeFileBundle emits an uncompressed tar of the directory, or of the file's
// parent directory so relative assets retain their paths. When a file's folder
// is over the cap, the bundle holds the page, the files it references (HTML
// and CSS, followed transitively) and as many other files of the folder as
// still fit, smallest first. All symlinks and special files are rejected. No
// temporary files or remote writes occur. Buffering up to the cap also keeps
// rejected requests off the binary stream.
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
	page := ""
	if !info.IsDir() {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("bundle refuses special file: %s", target)
		}
		page = filepath.Base(absolute)
		absolute = filepath.Dir(absolute)
	}
	root, err := os.OpenRoot(absolute)
	if err != nil {
		return err
	}
	defer root.Close()
	entries, err := collectBundleEntries(root)
	if err != nil {
		return err
	}
	selected := entries
	if total := bundleArchiveSize(entries); total > maxBytes {
		if page == "" {
			return bundleTooLargeError(target, total, maxBytes, entries, "; open one page file in it to bundle only what that page uses")
		}
		selected, err = selectPageEntries(root, page, entries, maxBytes)
		if err != nil {
			return fmt.Errorf("%w (%s)", err, target)
		}
	}
	var buffer bytes.Buffer
	limited := &bundleLimitWriter{out: &buffer, remaining: maxBytes}
	archive := tar.NewWriter(limited)
	for _, entry := range selected {
		if err := writeBundleEntry(root, archive, entry); err != nil {
			return err
		}
	}
	if err := archive.Close(); err != nil {
		return err
	}
	_, err = io.Copy(out, &buffer)
	return err
}

// bundleEntry is one validated folder entry with its exact archive cost.
type bundleEntry struct {
	name   string
	info   fs.FileInfo
	header *tar.Header
	bytes  int64 // header (with any PAX records) plus padded content
}

const tarBlock = 512

// tarTrailerBytes is the two zero blocks that end a tar.
const tarTrailerBytes = 2 * tarBlock

// collectBundleEntries walks the folder in lexical order, refusing every
// symlink and special file, and prices each entry exactly as tar writes it.
func collectBundleEntries(root *os.Root) ([]bundleEntry, error) {
	var entries []bundleEntry
	err := fs.WalkDir(root.FS(), ".", func(name string, _ fs.DirEntry, walkErr error) error {
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
		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		header.Name = name
		if info.IsDir() {
			header.Name += "/"
		}
		size, err := tarHeaderBytes(header)
		if err != nil {
			return err
		}
		if !info.IsDir() {
			size += (info.Size() + tarBlock - 1) / tarBlock * tarBlock
		}
		entries = append(entries, bundleEntry{name: name, info: info, header: header, bytes: size})
		return nil
	})
	return entries, err
}

// tarHeaderBytes reports the bytes tar.Writer emits for the header alone.
func tarHeaderBytes(header *tar.Header) (int64, error) {
	counter := &byteCounter{}
	h := *header
	h.Size = 0
	if err := tar.NewWriter(counter).WriteHeader(&h); err != nil {
		return 0, err
	}
	return counter.n, nil
}

type byteCounter struct{ n int64 }

func (c *byteCounter) Write(p []byte) (int, error) {
	c.n += int64(len(p))
	return len(p), nil
}

func bundleArchiveSize(entries []bundleEntry) int64 {
	total := int64(tarTrailerBytes)
	for _, entry := range entries {
		total += entry.bytes
	}
	return total
}

// selectPageEntries keeps the page, every file it references (followed
// through HTML and CSS) and then other files, smallest first, while the
// archive fits. Folders are kept only as parents of kept files. It fails,
// naming the biggest needed files, when the needed files alone do not fit.
func selectPageEntries(root *os.Root, page string, entries []bundleEntry, maxBytes int64) ([]bundleEntry, error) {
	index := map[string]int{}
	for i, entry := range entries {
		index[entry.name] = i
	}
	keep := make([]bool, len(entries))
	total := int64(tarTrailerBytes)
	add := func(i int) {
		for dir := path.Dir(entries[i].name); dir != "."; dir = path.Dir(dir) {
			if j, ok := index[dir]; ok && !keep[j] {
				keep[j] = true
				total += entries[j].bytes
			}
		}
		keep[i] = true
		total += entries[i].bytes
	}
	cost := func(i int) int64 {
		extra := entries[i].bytes
		for dir := path.Dir(entries[i].name); dir != "."; dir = path.Dir(dir) {
			if j, ok := index[dir]; ok && !keep[j] {
				extra += entries[j].bytes
			}
		}
		return extra
	}
	var needed []bundleEntry
	queue := []string{page}
	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]
		i, ok := index[name]
		if !ok || keep[i] || entries[i].info.IsDir() {
			continue
		}
		add(i)
		needed = append(needed, entries[i])
		if entries[i].info.Size() > maxBytes {
			continue
		}
		switch strings.ToLower(path.Ext(name)) {
		case ".html", ".htm", ".xhtml":
			data, err := readBundleFile(root, entries[i])
			if err != nil {
				return nil, err
			}
			queue = append(queue, pageReferences(name, data)...)
		case ".css":
			data, err := readBundleFile(root, entries[i])
			if err != nil {
				return nil, err
			}
			queue = append(queue, cssReferences(name, string(data))...)
		}
	}
	if total > maxBytes {
		return nil, bundleTooLargeError(page+" and the files it uses", total, maxBytes, needed, "")
	}
	rest := make([]int, 0, len(entries))
	for i, entry := range entries {
		if !keep[i] && !entry.info.IsDir() {
			rest = append(rest, i)
		}
	}
	sort.SliceStable(rest, func(a, b int) bool { return entries[rest[a]].bytes < entries[rest[b]].bytes })
	for _, i := range rest {
		if total+cost(i) <= maxBytes {
			add(i)
		}
	}
	selected := make([]bundleEntry, 0, len(entries))
	for i, entry := range entries {
		if keep[i] {
			selected = append(selected, entry)
		}
	}
	return selected, nil
}

func readBundleFile(root *os.Root, entry bundleEntry) ([]byte, error) {
	file, err := root.Open(entry.name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return io.ReadAll(io.LimitReader(file, entry.info.Size()))
}

// bundleTooLargeError names the cap and the biggest files, so the owner sees
// what to move out of the folder.
func bundleTooLargeError(what string, total, maxBytes int64, entries []bundleEntry, hint string) error {
	files := make([]bundleEntry, 0, len(entries))
	for _, entry := range entries {
		if !entry.info.IsDir() {
			files = append(files, entry)
		}
	}
	sort.SliceStable(files, func(a, b int) bool { return files[a].info.Size() > files[b].info.Size() })
	if len(files) > 5 {
		files = files[:5]
	}
	biggest := make([]string, 0, len(files))
	for _, entry := range files {
		biggest = append(biggest, fmt.Sprintf("%s (%s)", entry.name, formatBundleBytes(entry.info.Size())))
	}
	return fmt.Errorf("bundle of %s is %s, over the %s archive byte limit; biggest files: %s%s",
		what, formatBundleBytes(total), formatBundleBytes(maxBytes), strings.Join(biggest, ", "), hint)
}

func formatBundleBytes(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

func writeBundleEntry(root *os.Root, archive *tar.Writer, entry bundleEntry) error {
	if err := archive.WriteHeader(entry.header); err != nil {
		return err
	}
	if entry.info.IsDir() {
		return nil
	}
	// Root.Open enforces containment even if an entry is replaced by a symlink
	// after Lstat. SameFile additionally rejects replacement within the root.
	file, err := root.Open(entry.name)
	if err != nil {
		return err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return err
	}
	if !opened.Mode().IsRegular() || !os.SameFile(entry.info, opened) {
		return fmt.Errorf("bundle file changed during read: %s", entry.name)
	}
	_, err = io.CopyN(archive, file, entry.info.Size())
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
