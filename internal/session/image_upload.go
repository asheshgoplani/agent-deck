package session

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"golang.org/x/sys/unix"
)

const MaxImageUploadBytes int64 = 20 << 20
const imageUploadMaxAge = 7 * 24 * time.Hour

// ImageUpload is a host-local attachment staged for session send --image.
type ImageUpload struct {
	Path  string `json:"path"`
	Bytes int64  `json:"bytes"`
}

func uploadComponent(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, "/\\\x00")
}

func validateUploadName(name string) error {
	if !uploadComponent(name) || strings.HasPrefix(name, ".") || len(name) > 200 {
		return fmt.Errorf("invalid upload name: use a plain filename without separators or traversal")
	}
	switch strings.ToLower(filepath.Ext(name)) {
	case ".png", ".jpg", ".gif", ".webp", ".pdf":
		return nil
	default:
		return fmt.Errorf("unsupported upload extension: use png, jpg, gif, webp or pdf")
	}
}

// openUploadDir pins each directory with O_NOFOLLOW. All subsequent operations
// are relative to these descriptors, including cleanup, so renaming a parent or
// replacing it with a symlink cannot redirect writes or deletions.
func openUploadDir(parent *os.File, name string, create bool) (*os.File, error) {
	if !uploadComponent(name) {
		return nil, fmt.Errorf("invalid upload directory")
	}
	if create {
		if err := unix.Mkdirat(int(parent.Fd()), name, 0700); err != nil && !errors.Is(err, unix.EEXIST) {
			return nil, err
		}
	}
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open upload directory (symlinks refused): %w", err)
	}
	f := os.NewFile(uintptr(fd), name)
	if err := f.Chmod(0700); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

func openUploads(profileDir string, create bool) (*os.File, error) {
	fd, err := unix.Open(profileDir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	parent := os.NewFile(uintptr(fd), profileDir)
	defer parent.Close()
	return openUploadDir(parent, "macapp-uploads", create)
}

// UploadImage resolves storage from this open profile, never from the caller's
// SSH HOME. Stopped sessions may stage attachments; unknown sessions may not.
func (s *Storage) UploadImage(id, name string, input io.Reader) (ImageUpload, error) {
	var result ImageUpload
	if err := validateUploadName(name); err != nil {
		return result, err
	}
	if !uploadComponent(id) {
		return result, fmt.Errorf("invalid session id")
	}
	exists, err := s.InstanceExists(id)
	if err != nil {
		return result, err
	}
	if !exists {
		return result, fmt.Errorf("unknown session %q", id)
	}
	profileDir, err := filepath.Abs(filepath.Dir(s.Path()))
	if err != nil {
		return result, err
	}
	root, err := openUploads(profileDir, true)
	if err != nil {
		return result, err
	}
	defer root.Close()
	dir, err := openUploadDir(root, id, true)
	if err != nil {
		return result, err
	}
	defer dir.Close()
	// UUID names are single-use. Refusing all existing targets also refuses
	// symlinks without ever opening their referents.
	var stat unix.Stat_t
	if err := unix.Fstatat(int(dir.Fd()), name, &stat, unix.AT_SYMLINK_NOFOLLOW); !errors.Is(err, unix.ENOENT) {
		return result, fmt.Errorf("upload target already exists or cannot be inspected")
	}
	temp := ".upload-" + uuid.NewString()
	fd, err := unix.Openat(int(dir.Fd()), temp, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return result, err
	}
	out := os.NewFile(uintptr(fd), temp)
	complete := false
	defer func() {
		_ = unix.Unlinkat(int(dir.Fd()), temp, 0)
		if !complete {
			_ = unix.Unlinkat(int(root.Fd()), id, unix.AT_REMOVEDIR)
		}
	}()
	if err := out.Chmod(0600); err != nil {
		out.Close()
		return result, err
	}
	n, copyErr := io.Copy(out, io.LimitReader(input, MaxImageUploadBytes+1))
	if copyErr == nil && n > MaxImageUploadBytes {
		copyErr = fmt.Errorf("image upload exceeds 20 MiB limit")
	}
	if copyErr == nil && n == 0 {
		copyErr = fmt.Errorf("image upload is empty")
	}
	if copyErr == nil {
		copyErr = out.Sync()
	}
	closeErr := out.Close()
	if copyErr != nil {
		return result, copyErr
	}
	if closeErr != nil {
		return result, closeErr
	}
	// Exclusive rename publishes only complete files and refuses targets that
	// appeared after the check above, including symlinks.
	if err := publishImageUpload(int(dir.Fd()), temp, name); err != nil {
		return result, fmt.Errorf("publish image upload: %w", err)
	}
	exists, err = s.InstanceExists(id)
	if err != nil || !exists {
		_ = unix.Unlinkat(int(dir.Fd()), name, 0)
		return result, fmt.Errorf("session removed during upload")
	}
	if err := verifyUploadDirectory(profileDir, id, dir); err != nil {
		_ = unix.Unlinkat(int(dir.Fd()), name, 0)
		return result, err
	}
	complete = true
	return ImageUpload{Path: filepath.Join(profileDir, "macapp-uploads", id, name), Bytes: n}, nil
}

// Verify the published path still names the pinned directory before reporting it.
func verifyUploadDirectory(profileDir, id string, pinned *os.File) error {
	root, err := openUploads(profileDir, false)
	if err != nil {
		return err
	}
	defer root.Close()
	current, err := openUploadDir(root, id, false)
	if err != nil {
		return err
	}
	defer current.Close()
	before, err := pinned.Stat()
	if err != nil {
		return err
	}
	after, err := current.Stat()
	if err != nil {
		return err
	}
	if !os.SameFile(before, after) {
		return fmt.Errorf("upload directory changed during upload")
	}
	return nil
}

// cleanUploadSession never follows links or recurses into unexpected folders.
// A zero cutoff removes every staged file for a deleted session.
func cleanUploadSession(root *os.File, id string, cutoff time.Time) error {
	dir, err := openUploadDir(root, id, false)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer dir.Close()
	names, err := dir.Readdirnames(-1)
	if err != nil {
		return err
	}
	for _, name := range names {
		var info unix.Stat_t
		if err := unix.Fstatat(int(dir.Fd()), name, &info, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			if errors.Is(err, unix.ENOENT) {
				continue
			}
			return err
		}
		if info.Mode&unix.S_IFMT == unix.S_IFDIR {
			continue
		}
		if !cutoff.IsZero() && !time.Unix(info.Mtim.Sec, info.Mtim.Nsec).Before(cutoff) {
			continue
		}
		if err := unix.Unlinkat(int(dir.Fd()), name, 0); err != nil && !errors.Is(err, unix.ENOENT) {
			return err
		}
	}
	err = unix.Unlinkat(int(root.Fd()), id, unix.AT_REMOVEDIR)
	if errors.Is(err, unix.ENOTEMPTY) || errors.Is(err, unix.ENOENT) {
		return nil
	}
	return err
}

func cleanupImageUploads(profileDir, id string) error {
	if !uploadComponent(id) {
		return fmt.Errorf("invalid session id")
	}
	root, err := openUploads(profileDir, false)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer root.Close()
	return cleanUploadSession(root, id, time.Time{})
}

func pruneImageUploads(profileDir string, now time.Time) error {
	root, err := openUploads(profileDir, false)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer root.Close()
	names, err := root.Readdirnames(-1)
	if err != nil {
		return err
	}
	for _, name := range names {
		var info unix.Stat_t
		if err := unix.Fstatat(int(root.Fd()), name, &info, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			if errors.Is(err, unix.ENOENT) {
				continue
			}
			return err
		}
		if info.Mode&unix.S_IFMT != unix.S_IFDIR {
			continue
		}
		if err := cleanUploadSession(root, name, now.Add(-imageUploadMaxAge)); err != nil {
			return err
		}
	}
	return nil
}

var uploadStartupCleanup = struct {
	sync.Mutex
	completed map[string]bool
}{completed: make(map[string]bool)}

func pruneImageUploadsOnStartup(profileDir string) error {
	uploadStartupCleanup.Lock()
	defer uploadStartupCleanup.Unlock()
	if uploadStartupCleanup.completed[profileDir] {
		return nil
	}
	if err := pruneImageUploads(profileDir, time.Now()); err != nil {
		return err
	}
	uploadStartupCleanup.completed[profileDir] = true
	return nil
}
