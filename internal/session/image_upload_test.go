package session

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func uploadFixture(t *testing.T) (*Storage, string) {
	t.Helper()
	s := newTestStorage(t)
	id := "upload-session"
	require.NoError(t, s.Save([]*Instance{{ID: id, Title: "upload", ProjectPath: t.TempDir(), Tool: "claude", CreatedAt: time.Now()}}))
	return s, id
}

func TestImageUploadBoundsAndPermissions(t *testing.T) {
	s, id := uploadFixture(t)
	data := []byte("\x89PNG\r\n\x1a\nfixture")
	got, err := s.UploadImage(id, "shot.png", bytes.NewReader(data))
	require.NoError(t, err)
	require.Equal(t, int64(len(data)), got.Bytes)
	require.Equal(t, filepath.Join(filepath.Dir(s.Path()), "macapp-uploads", id, "shot.png"), got.Path)
	body, err := os.ReadFile(got.Path)
	require.NoError(t, err)
	require.Equal(t, data, body)
	for path, mode := range map[string]os.FileMode{got.Path: 0600, filepath.Dir(got.Path): 0700, filepath.Dir(filepath.Dir(got.Path)): 0700} {
		info, err := os.Stat(path)
		require.NoError(t, err)
		require.Equal(t, mode, info.Mode().Perm())
	}
	_, err = s.UploadImage(id, "shot.png", strings.NewReader("replacement"))
	require.Error(t, err)
	for _, name := range []string{"", "../shot.png", "nested/shot.png", `nested\shot.png`, ".png", "shot.txt", "shot", "x\x00.png", strings.Repeat("x", 201) + ".png"} {
		_, err := s.UploadImage(id, name, strings.NewReader("x"))
		require.Error(t, err, name)
	}
	for _, ext := range []string{"jpg", "gif", "webp", "pdf"} {
		_, err := s.UploadImage(id, "shot."+ext, strings.NewReader("x"))
		require.NoError(t, err)
	}
	_, err = s.UploadImage("missing", "shot.png", strings.NewReader("x"))
	require.ErrorContains(t, err, "unknown session")
	_, err = s.UploadImage(id, "empty.png", strings.NewReader(""))
	require.ErrorContains(t, err, "empty")
	_, err = s.UploadImage(id, "large.png", io.LimitReader(zeroReader{}, MaxImageUploadBytes+1))
	require.ErrorContains(t, err, "20 MiB")
	_, err = os.Stat(filepath.Join(filepath.Dir(got.Path), "large.png"))
	require.ErrorIs(t, err, os.ErrNotExist)
	exact, err := s.UploadImage(id, "exact.png", io.LimitReader(zeroReader{}, MaxImageUploadBytes))
	require.NoError(t, err)
	require.Equal(t, MaxImageUploadBytes, exact.Bytes)
	entries, err := os.ReadDir(filepath.Dir(got.Path))
	require.NoError(t, err)
	for _, e := range entries {
		require.False(t, strings.HasPrefix(e.Name(), ".upload-"))
	}
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) { clear(p); return len(p), nil }

type brokenUploadReader struct{}

func (brokenUploadReader) Read(p []byte) (int, error) {
	return copy(p, "partial"), errors.New("broken input")
}

func TestImageUploadSymlinkRefusal(t *testing.T) {
	for _, component := range []string{"root", "session", "target"} {
		t.Run(component, func(t *testing.T) {
			s, id := uploadFixture(t)
			root := filepath.Join(filepath.Dir(s.Path()), "macapp-uploads")
			outside := t.TempDir()
			marker := filepath.Join(outside, "marker.png")
			require.NoError(t, os.WriteFile(marker, []byte("untouched"), 0600))
			switch component {
			case "root":
				require.NoError(t, os.Symlink(outside, root))
			case "session":
				require.NoError(t, os.Mkdir(root, 0700))
				require.NoError(t, os.Symlink(outside, filepath.Join(root, id)))
			case "target":
				require.NoError(t, os.MkdirAll(filepath.Join(root, id), 0700))
				require.NoError(t, os.Symlink(marker, filepath.Join(root, id, "shot.png")))
			}
			_, err := s.UploadImage(id, "shot.png", strings.NewReader("replacement"))
			require.Error(t, err)
			_ = cleanupImageUploads(filepath.Dir(s.Path()), id)
			_ = pruneImageUploads(filepath.Dir(s.Path()), time.Now().Add(30*24*time.Hour))
			body, err := os.ReadFile(marker)
			require.NoError(t, err)
			require.Equal(t, "untouched", string(body))
			entries, err := os.ReadDir(outside)
			require.NoError(t, err)
			require.Len(t, entries, 1)
		})
	}
}

func TestImageUploadLifecycle(t *testing.T) {
	s, id := uploadFixture(t)
	recent, err := s.UploadImage(id, "recent.png", strings.NewReader("recent"))
	require.NoError(t, err)
	old, err := s.UploadImage(id, "old.png", strings.NewReader("old"))
	require.NoError(t, err)
	past := time.Now().Add(-8 * 24 * time.Hour)
	require.NoError(t, os.Chtimes(old.Path, past, past))
	require.NoError(t, pruneImageUploads(filepath.Dir(s.Path()), time.Now()))
	_, err = os.Stat(old.Path)
	require.ErrorIs(t, err, os.ErrNotExist)
	_, err = os.Stat(recent.Path)
	require.NoError(t, err)
	_, err = s.UploadImage(id, "partial.png", brokenUploadReader{})
	require.ErrorContains(t, err, "broken input")
	entries, err := os.ReadDir(filepath.Dir(recent.Path))
	require.NoError(t, err)
	require.Len(t, entries, 1)
	cleanup, err := s.DeleteInstanceDeferredCleanup(id)
	require.NoError(t, err)
	_, err = os.Stat(recent.Path)
	require.NoError(t, err, "cleanup is deferred for the TUI")
	cleanup()
	_, err = os.Stat(filepath.Dir(recent.Path))
	require.ErrorIs(t, err, os.ErrNotExist)
}

// A concurrent namespace swap cannot redirect a write through a symlink.
func TestImageUploadDirectorySwap(t *testing.T) {
	s, id := uploadFixture(t)
	root := filepath.Join(filepath.Dir(s.Path()), "macapp-uploads")
	outside := t.TempDir()
	reader := &uploadCallbackReader{callback: func() {
		require.NoError(t, os.Rename(filepath.Join(root, id), filepath.Join(root, "moved")))
		require.NoError(t, os.Symlink(outside, filepath.Join(root, id)))
	}}
	_, uploadErr := s.UploadImage(id, "shot.png", reader)
	require.Error(t, uploadErr)
	entries, err := os.ReadDir(outside)
	require.NoError(t, err)
	require.Empty(t, entries)
}

type uploadCallbackReader struct {
	callback func()
	done     bool
}

func (r *uploadCallbackReader) Read(p []byte) (int, error) {
	if r.done {
		return 0, io.EOF
	}
	r.done = true
	r.callback()
	return copy(p, "image"), nil
}

func TestImageUploadConcurrentRemoval(t *testing.T) {
	s, id := uploadFixture(t)
	reader := &uploadCallbackReader{callback: func() { require.NoError(t, s.DeleteInstance(id)) }}
	_, err := s.UploadImage(id, "removed.png", reader)
	require.Error(t, err)
	_, err = os.Stat(filepath.Join(filepath.Dir(s.Path()), "macapp-uploads", id))
	require.ErrorIs(t, err, os.ErrNotExist)
}
