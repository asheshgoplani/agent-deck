package session

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/logging"
	"github.com/fsnotify/fsnotify"
	"github.com/sahilm/fuzzy"
	"golang.org/x/time/rate"
)

var searchLog = logging.ForComponent(logging.CompSession)

// SearchTier represents the search strategy tier
type SearchTier int

const (
	TierInstant  SearchTier = iota // < 100MB, full in-memory
	TierBalanced                   // 100MB-500MB, on-demand scan to cap memory
)

// TierThresholdInstant is the max size for instant tier (100MB)
const TierThresholdInstant = 100 * 1024 * 1024

// TierThresholdBalanced is the max size for balanced tier (500MB)
const TierThresholdBalanced = 500 * 1024 * 1024

// SearchEntry represents a searchable Claude session
type SearchEntry struct {
	SessionID string    // Claude session UUID
	FilePath  string    // Path to .jsonl file
	CWD       string    // Project working directory
	Summary   string    // First user message or summary
	ModTime   time.Time // File modification time
	FileSize  int64     // File size in bytes

	content *ContentBuffer
}

// MatchRange represents a match position in content
type MatchRange struct {
	Start int
	End   int
}

// Match searches for query in entry content (case-insensitive)
// Returns match positions for highlighting
func (e *SearchEntry) Match(query string) []MatchRange {
	if e.content == nil || query == "" {
		return nil
	}

	queryLower := []byte(strings.ToLower(query))
	var matches []MatchRange

	e.content.With(func(_, lower []byte) {
		start := 0
		for {
			idx := bytes.Index(lower[start:], queryLower)
			if idx == -1 {
				break
			}
			absIdx := start + idx
			matches = append(matches, MatchRange{
				Start: absIdx,
				End:   absIdx + len(queryLower),
			})
			start = absIdx + len(queryLower)
		}
	})

	return matches
}

// GetSnippet extracts a context window around the first match
// Uses rune-based indexing to safely handle UTF-8 content
// Optimized: Single rune conversion instead of triple conversion
func (e *SearchEntry) GetSnippet(query string, windowSize int) string {
	if e.content == nil {
		if e.Summary != "" {
			return e.Summary
		}
		return ""
	}

	queryLower := []byte(strings.ToLower(query))
	var content string
	var matches []MatchRange

	e.content.With(func(data, lower []byte) {
		if len(data) == 0 {
			return
		}
		content = string(data)
		matches = matchRanges(lower, queryLower)
	})

	if content == "" {
		return ""
	}

	runes := []rune(content)
	if len(matches) == 0 {
		if len(runes) > windowSize*2 {
			return string(runes[:windowSize*2]) + "..."
		}
		return content
	}

	match := matches[0]
	runeStart := byteIndexToRuneIndex(content, match.Start)
	runeEnd := byteIndexToRuneIndex(content, match.End)

	start := runeStart - windowSize
	if start < 0 {
		start = 0
	}
	end := runeEnd + windowSize
	if end > len(runes) {
		end = len(runes)
	}

	for start > 0 && runes[start-1] != ' ' && runes[start-1] != '\n' {
		start--
	}
	for end < len(runes) && runes[end] != ' ' && runes[end] != '\n' {
		end++
	}

	snippet := string(runes[start:end])
	prefix := ""
	suffix := ""
	if start > 0 {
		prefix = "..."
	}
	if end < len(runes) {
		suffix = "..."
	}

	return prefix + strings.TrimSpace(snippet) + suffix
}

// byteIndexToRuneIndex converts a byte index to a rune index efficiently
// This avoids creating substring copies which was causing O(n) allocations
func byteIndexToRuneIndex(s string, byteIdx int) int {
	if byteIdx <= 0 {
		return 0
	}
	if byteIdx >= len(s) {
		return len([]rune(s))
	}
	// Count runes up to the byte index without creating a substring
	runeCount := 0
	for i := range s {
		if i >= byteIdx {
			break
		}
		runeCount++
	}
	return runeCount
}

// ContentBuffer stores full content and a lowercased copy for fast search.
// It is safe for concurrent reads and append-only writes.
type ContentBuffer struct {
	mu    sync.RWMutex
	data  []byte
	lower []byte
}

func newContentBuffer(data []byte) *ContentBuffer {
	if len(data) == 0 {
		return &ContentBuffer{}
	}
	copied := append([]byte(nil), data...)
	lower := []byte(strings.ToLower(string(copied)))
	return &ContentBuffer{
		data:  copied,
		lower: lower,
	}
}

func (b *ContentBuffer) Append(data []byte) {
	if len(data) == 0 {
		return
	}
	lowerChunk := []byte(strings.ToLower(string(data)))
	b.mu.Lock()
	b.data = append(b.data, data...)
	b.lower = append(b.lower, lowerChunk...)
	b.mu.Unlock()
}

func (b *ContentBuffer) With(fn func(data, lower []byte)) {
	b.mu.RLock()
	fn(b.data, b.lower)
	b.mu.RUnlock()
}

// Size returns the total memory used by this buffer (data + lowercased copy)
func (b *ContentBuffer) Size() int64 {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return int64(len(b.data) + len(b.lower))
}

func (b *ContentBuffer) CopyData() []byte {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return append([]byte(nil), b.data...)
}

func (e *SearchEntry) hasContent() bool {
	return e != nil && e.content != nil
}

func (e *SearchEntry) setContent(data []byte) {
	if len(data) == 0 {
		return
	}
	e.content = newContentBuffer(data)
}

func (e *SearchEntry) appendContent(data []byte) {
	if len(data) == 0 {
		return
	}
	if e.content == nil {
		e.setContent(data)
		return
	}
	e.content.Append(data)
}

func (e *SearchEntry) ContentString() string {
	if e.content == nil {
		return ""
	}
	var content string
	e.content.With(func(data, _ []byte) {
		if len(data) > 0 {
			content = string(data)
		}
	})
	return content
}

func (e *SearchEntry) ContentPreview(max int) string {
	if e.content == nil || max <= 0 {
		return ""
	}
	var preview string
	e.content.With(func(data, _ []byte) {
		if len(data) == 0 {
			return
		}
		if len(data) > max {
			preview = string(data[:max])
			return
		}
		preview = string(data)
	})
	return preview
}

func (e *SearchEntry) MatchCount(query string) int {
	if e.content == nil || query == "" {
		return 0
	}
	queryLower := []byte(strings.ToLower(query))
	count := 0
	e.content.With(func(_, lower []byte) {
		count = bytes.Count(lower, queryLower)
	})
	return count
}

// claudeJSONLRecord represents a single line in Claude's JSONL files
type claudeJSONLRecord struct {
	SessionID string          `json:"sessionId"`
	Type      string          `json:"type"`
	Message   json.RawMessage `json:"message"`
	Timestamp string          `json:"timestamp"`
	CWD       string          `json:"cwd"`
	Summary   string          `json:"summary"`
}

// claudeMessage represents the message field in a record
type claudeMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

// parseClaudeJSONL parses a Claude JSONL file into a SearchEntry
func parseClaudeJSONL(filePath string, data []byte, includeContent bool) (*SearchEntry, error) {
	entry := &SearchEntry{
		FilePath: filePath,
	}

	var contentBuilder bytes.Buffer
	scanner := bufio.NewScanner(bytes.NewReader(data))
	// Handle large lines
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 10*1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var record claudeJSONLRecord
		if err := json.Unmarshal(line, &record); err != nil {
			continue // Skip malformed lines
		}

		// Extract session ID from first valid record
		if entry.SessionID == "" && record.SessionID != "" {
			entry.SessionID = record.SessionID
		}

		// Extract CWD
		if entry.CWD == "" && record.CWD != "" {
			entry.CWD = record.CWD
		}

		// Extract summary if available
		if entry.Summary == "" && record.Summary != "" {
			entry.Summary = record.Summary
		}

		// Use first user message as summary if no summary field
		if entry.Summary == "" && record.Type == "user" && len(record.Message) > 0 {
			var msg claudeMessage
			if err := json.Unmarshal(record.Message, &msg); err == nil {
				var contentStr string
				if err := json.Unmarshal(msg.Content, &contentStr); err == nil {
					if len(contentStr) > 200 {
						entry.Summary = contentStr[:200] + "..."
					} else {
						entry.Summary = contentStr
					}
				}
			}
		}

		// For metadata-only mode (TierBalanced): stop once we have all metadata
		if !includeContent && entry.SessionID != "" && entry.CWD != "" && entry.Summary != "" {
			break
		}

		if includeContent && len(record.Message) > 0 {
			var msg claudeMessage
			if err := json.Unmarshal(record.Message, &msg); err == nil {
				if formatted := formatMessageContent(msg); formatted != "" {
					contentBuilder.WriteString(formatted)
					contentBuilder.WriteString("\n")
				}
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return entry, err
	}

	if includeContent && contentBuilder.Len() > 0 {
		entry.setContent(contentBuilder.Bytes())
	}

	return entry, nil
}

// parseClaudeJSONLHead reads only the first 32KB of a JSONL file to extract metadata.
// Used in TierBalanced mode to avoid reading entire files (which can be 100s of MB).
func parseClaudeJSONLHead(filePath string) (*SearchEntry, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	entry := &SearchEntry{FilePath: filePath}
	scanner := bufio.NewScanner(io.LimitReader(f, 32*1024))
	buf := make([]byte, 0, 32*1024)
	scanner.Buffer(buf, 32*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var record claudeJSONLRecord
		if err := json.Unmarshal(line, &record); err != nil {
			continue
		}
		if entry.SessionID == "" && record.SessionID != "" {
			entry.SessionID = record.SessionID
		}
		if entry.CWD == "" && record.CWD != "" {
			entry.CWD = record.CWD
		}
		if entry.Summary == "" && record.Summary != "" {
			entry.Summary = record.Summary
		}
		if entry.Summary == "" && record.Type == "user" && len(record.Message) > 0 {
			var msg claudeMessage
			if err := json.Unmarshal(record.Message, &msg); err == nil {
				var contentStr string
				if err := json.Unmarshal(msg.Content, &contentStr); err == nil {
					if len(contentStr) > 200 {
						entry.Summary = contentStr[:200] + "..."
					} else {
						entry.Summary = contentStr
					}
				}
			}
		}
		if entry.SessionID != "" && entry.Summary != "" {
			break
		}
	}
	return entry, nil
}

// DetectTier determines the appropriate search tier based on data size
func DetectTier(totalSize int64) SearchTier {
	if totalSize < TierThresholdInstant {
		return TierInstant
	}
	return TierBalanced
}

// TierName returns a human-readable name for the tier
func TierName(tier SearchTier) string {
	switch tier {
	case TierInstant:
		return "instant"
	case TierBalanced:
		return "balanced"
	default:
		return "unknown"
	}
}

// SearchResult represents a search result with match info
type SearchResult struct {
	Entry   *SearchEntry
	Matches []MatchRange
	Score   int
	Snippet string
}

// GlobalSearchIndex manages the searchable session index
type GlobalSearchIndex struct {
	// Configuration
	config    GlobalSearchSettings
	claudeDir string

	// Index data (protected by atomic pointer for lock-free reads)
	entries atomic.Pointer[[]SearchEntry]

	// File tracking for incremental updates
	fileTrackers map[string]*FileTracker
	trackerMu    sync.RWMutex

	// File watcher
	watcher *fsnotify.Watcher

	// Rate limiter for background indexing
	limiter *rate.Limiter

	// Tier
	tier SearchTier

	// Loading state
	loading atomic.Bool

	// Lifecycle
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	// Memory tracking for content buffers
	currentMemoryBytes atomic.Int64
	memoryLimitBytes   int64

	// Query cache for balanced tier narrowing
	lastQuery   string
	lastResults []*SearchResult
	lastQueryMu sync.Mutex
}

// FileTracker tracks file state for incremental updates
type FileTracker struct {
	Path       string
	LastOffset int64
	LastSize   int64
	LastMod    time.Time
}

// NewGlobalSearchIndex creates a new search index
func NewGlobalSearchIndex(claudeDir string, config GlobalSearchSettings) (*GlobalSearchIndex, error) {
	if !config.GetEnabled() {
		return nil, nil
	}

	// Apply defaults if not set
	if config.IndexRateLimit == 0 {
		config.IndexRateLimit = 20
	}
	if config.RecentDays == 0 {
		config.RecentDays = 30
	}

	ctx, cancel := context.WithCancel(context.Background())

	// Default memory limit: 100MB (applies to instant tier content buffers)
	memLimitBytes := int64(100 * 1024 * 1024)
	if config.MemoryLimitMB > 0 {
		memLimitBytes = int64(config.MemoryLimitMB) * 1024 * 1024
	}

	idx := &GlobalSearchIndex{
		config:           config,
		claudeDir:        claudeDir,
		fileTrackers:     make(map[string]*FileTracker),
		limiter:          rate.NewLimiter(rate.Limit(config.IndexRateLimit), 5),
		memoryLimitBytes: memLimitBytes,
		ctx:              ctx,
		cancel:           cancel,
	}

	// Initialize empty entries
	emptyEntries := make([]SearchEntry, 0)
	idx.entries.Store(&emptyEntries)

	// Measure data size and determine tier
	projectsDir := filepath.Join(claudeDir, "projects")
	totalSize, err := measureDataSize(projectsDir, config.RecentDays)
	if err != nil {
		// Don't fail if projects dir doesn't exist, just use empty index
		if !os.IsNotExist(err) {
			cancel()
			return nil, err
		}
	}

	// Determine tier (respect config override)
	switch config.Tier {
	case "instant":
		idx.tier = TierInstant
	case "balanced":
		idx.tier = TierBalanced
	case "disabled":
		cancel()
		return nil, nil
	default:
		idx.tier = DetectTier(totalSize)
	}

	// Start file watcher
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		cancel()
		return nil, err
	}
	idx.watcher = watcher

	// Watch the projects directory (create if doesn't exist check)
	if _, err := os.Stat(projectsDir); err == nil {
		if err := watcher.Add(projectsDir); err != nil {
			searchLog.Warn("global_search_watch_failed", slog.String("error", err.Error()))
		}

		// Only watch project-level directories (depth 1 under projects/).
		// Previously watched ALL subdirectories (884 dirs including tool-results/,
		// subagents/, etc.) which leaked ~7000 kqueue file descriptors and caused
		// agent-deck to balloon to 6+ GB RSS until macOS killed it.
		// JSONL files only exist at the project level, not in subdirectories.
		dirEntries, _ := os.ReadDir(projectsDir)
		for _, de := range dirEntries {
			if de.IsDir() {
				_ = watcher.Add(filepath.Join(projectsDir, de.Name()))
			}
		}
	}

	// Set loading state
	idx.loading.Store(true)

	// Start background workers
	idx.wg.Add(2)
	go idx.watcherLoop()
	go idx.initialLoad()

	return idx, nil
}

// measureDataSize calculates total size of JSONL files
func measureDataSize(projectsDir string, recentDays int) (int64, error) {
	var totalSize int64
	cutoff := time.Time{}
	if recentDays > 0 {
		cutoff = time.Now().AddDate(0, 0, -recentDays)
	}

	err := filepath.WalkDir(projectsDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			name := d.Name()
			if name == "tool-results" || name == "subagents" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".jsonl") {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if !cutoff.IsZero() && info.ModTime().Before(cutoff) {
			return nil
		}
		totalSize += info.Size()
		return nil
	})

	return totalSize, err
}

// initialLoad loads all session files on startup
func (idx *GlobalSearchIndex) initialLoad() {
	defer idx.wg.Done()

	projectsDir := filepath.Join(idx.claudeDir, "projects")
	cutoff := time.Time{}
	if idx.config.RecentDays > 0 {
		cutoff = time.Now().AddDate(0, 0, -idx.config.RecentDays)
	}

	var entries []SearchEntry
	includeContent := idx.tier == TierInstant

	_ = filepath.WalkDir(projectsDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			// Skip subdirectories that never contain JSONL files.
			// This avoids traversing thousands of tool-results/ and subagents/ dirs.
			name := d.Name()
			if name == "tool-results" || name == "subagents" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".jsonl") {
			return nil
		}

		// Check cancellation
		select {
		case <-idx.ctx.Done():
			return filepath.SkipAll
		default:
		}

		info, err := d.Info()
		if err != nil {
			return nil
		}

		// Check recency
		if !cutoff.IsZero() && info.ModTime().Before(cutoff) {
			return nil
		}

		// Only UUID-named files (skip agent-*.jsonl)
		baseName := filepath.Base(path)
		if !isUUIDFileName(baseName) {
			return nil
		}

		// Parse file: for metadata-only mode, read just the head (first 32KB)
		var entry *SearchEntry
		if !includeContent {
			entry, err = parseClaudeJSONLHead(path)
		} else {
			var data []byte
			// #nosec G122 -- walk callback over the user's own Claude session
			// directory; not attacker-controlled. TOCTOU symlink races affect
			// only this user's own files.
			data, err = os.ReadFile(path)
			if err != nil {
				return nil
			}
			entry, err = parseClaudeJSONL(path, data, true)
		}
		if err != nil || entry == nil || entry.SessionID == "" {
			return nil
		}

		entry.ModTime = info.ModTime()
		entry.FileSize = info.Size()

		// Track content memory usage
		if entry.hasContent() {
			idx.currentMemoryBytes.Add(entry.content.Size())
		}

		entries = append(entries, *entry)

		// Track file for incremental updates
		idx.trackerMu.Lock()
		idx.fileTrackers[path] = &FileTracker{
			Path:       path,
			LastOffset: info.Size(),
			LastSize:   info.Size(),
			LastMod:    info.ModTime(),
		}
		idx.trackerMu.Unlock()

		return nil
	})

	// Store entries and mark loading complete
	idx.entries.Store(&entries)
	idx.loading.Store(false)

	// Evict oldest entries if over memory limit
	if idx.currentMemoryBytes.Load() > idx.memoryLimitBytes {
		idx.evictOldestEntries()
	}
}

// isUUIDFileName checks if filename matches UUID pattern
var uuidFilePattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\.jsonl$`)

func isUUIDFileName(name string) bool {
	return uuidFilePattern.MatchString(name)
}

// watcherLoop handles file system events
func (idx *GlobalSearchIndex) watcherLoop() {
	defer idx.wg.Done()

	// Debounce map
	debounce := make(map[string]*time.Timer)
	debounceMu := sync.Mutex{}

	for {
		select {
		case <-idx.ctx.Done():
			// Stop all pending debounce timers to prevent goroutine leaks
			debounceMu.Lock()
			for _, timer := range debounce {
				timer.Stop()
			}
			debounceMu.Unlock()
			return
		case event, ok := <-idx.watcher.Events:
			if !ok {
				return
			}

			// Only care about writes and creates for .jsonl files
			if !strings.HasSuffix(event.Name, ".jsonl") {
				continue
			}
			if event.Op&(fsnotify.Write|fsnotify.Create) == 0 {
				continue
			}

			// Debounce: wait 300ms after last event for this file
			debounceMu.Lock()
			if timer, exists := debounce[event.Name]; exists {
				timer.Stop()
			}
			debounce[event.Name] = time.AfterFunc(300*time.Millisecond, func() {
				idx.updateFile(event.Name)
				debounceMu.Lock()
				delete(debounce, event.Name)
				debounceMu.Unlock()
			})
			debounceMu.Unlock()

		case err, ok := <-idx.watcher.Errors:
			if !ok {
				return
			}
			searchLog.Warn("global_search_watcher_error", slog.String("error", err.Error()))
		}
	}
}

// updateFile handles incremental update for a single file
func (idx *GlobalSearchIndex) updateFile(path string) {
	if !isUUIDFileName(filepath.Base(path)) {
		return
	}

	info, err := os.Stat(path)
	if err != nil {
		return // File deleted, ignore for now
	}

	includeContent := idx.tier == TierInstant
	idx.trackerMu.RLock()
	tracker, exists := idx.fileTrackers[path]
	idx.trackerMu.RUnlock()

	if exists && info.Size() < tracker.LastSize {
		// File was truncated/replaced, do full reload of this file
		tracker = nil
	}

	oldEntries := idx.entries.Load()
	newEntries := make([]SearchEntry, 0, len(*oldEntries)+1)
	found := false

	if !includeContent {
		canSkipParse := false
		for _, e := range *oldEntries {
			if e.FilePath == path {
				updated := e
				updated.ModTime = info.ModTime()
				updated.FileSize = info.Size()
				newEntries = append(newEntries, updated)
				found = true
				canSkipParse = e.SessionID != "" && e.CWD != "" && e.Summary != ""
			} else {
				newEntries = append(newEntries, e)
			}
		}

		if found && canSkipParse {
			idx.entries.Store(&newEntries)
			idx.trackerMu.Lock()
			idx.fileTrackers[path] = &FileTracker{
				Path:       path,
				LastOffset: info.Size(),
				LastSize:   info.Size(),
				LastMod:    info.ModTime(),
			}
			idx.trackerMu.Unlock()
			return
		}
	}

	// Read file (or just new portion for append-only)
	var data []byte
	if tracker != nil && info.Size() > tracker.LastOffset {
		// Incremental read
		f, err := os.Open(path)
		if err != nil {
			return
		}
		defer f.Close()
		_, _ = f.Seek(tracker.LastOffset, 0)
		data, _ = io.ReadAll(f)
	} else {
		// Full read
		data, _ = os.ReadFile(path)
	}

	if len(data) == 0 {
		return
	}

	// Parse and update
	entry, err := parseClaudeJSONL(path, data, includeContent)
	if err != nil || entry.SessionID == "" {
		return
	}
	entry.ModTime = info.ModTime()
	entry.FileSize = info.Size()

	// Update entries atomically
	for _, e := range *oldEntries {
		if e.FilePath == path {
			updated := e
			if updated.SessionID == "" && entry.SessionID != "" {
				updated.SessionID = entry.SessionID
			}
			if updated.CWD == "" && entry.CWD != "" {
				updated.CWD = entry.CWD
			}
			if updated.Summary == "" && entry.Summary != "" {
				updated.Summary = entry.Summary
			}
			updated.ModTime = entry.ModTime
			updated.FileSize = entry.FileSize

			if includeContent && entry.hasContent() {
				newData := entry.content.CopyData()
				idx.currentMemoryBytes.Add(int64(len(newData) * 2)) // data + lowered copy
				updated.appendContent(newData)
			}

			newEntries = append(newEntries, updated)
			found = true
		} else {
			newEntries = append(newEntries, e)
		}
	}
	if !found {
		if entry.hasContent() {
			idx.currentMemoryBytes.Add(entry.content.Size())
		}
		newEntries = append(newEntries, *entry)
	}

	idx.entries.Store(&newEntries)

	// Evict if over memory limit
	if idx.currentMemoryBytes.Load() > idx.memoryLimitBytes {
		idx.evictOldestEntries()
	}

	// Update tracker
	idx.trackerMu.Lock()
	idx.fileTrackers[path] = &FileTracker{
		Path:       path,
		LastOffset: info.Size(),
		LastSize:   info.Size(),
		LastMod:    info.ModTime(),
	}
	idx.trackerMu.Unlock()
}

// evictOldestEntries frees memory by nil-ing content on the oldest 25% of entries.
// Evicted entries retain metadata (Summary, CWD, SessionID) so they still appear
// in results; search just falls back to on-disk scanning for them.
func (idx *GlobalSearchIndex) evictOldestEntries() {
	entries := idx.entries.Load()
	if entries == nil || len(*entries) == 0 {
		return
	}

	// Build list of indices that have content, sorted by ModTime (oldest first)
	type indexedEntry struct {
		idx     int
		modTime time.Time
		size    int64
	}
	var withContent []indexedEntry
	for i := range *entries {
		e := &(*entries)[i]
		if e.hasContent() {
			withContent = append(withContent, indexedEntry{
				idx:     i,
				modTime: e.ModTime,
				size:    e.content.Size(),
			})
		}
	}

	if len(withContent) == 0 {
		return
	}

	sort.Slice(withContent, func(i, j int) bool {
		return withContent[i].modTime.Before(withContent[j].modTime)
	})

	// Evict oldest 25%
	evictCount := len(withContent) / 4
	if evictCount == 0 {
		evictCount = 1
	}

	// Make a mutable copy
	newEntries := make([]SearchEntry, len(*entries))
	copy(newEntries, *entries)

	var freedBytes int64
	for i := 0; i < evictCount; i++ {
		e := &newEntries[withContent[i].idx]
		freedBytes += withContent[i].size
		e.content = nil
	}

	idx.entries.Store(&newEntries)
	idx.currentMemoryBytes.Add(-freedBytes)
}

// Search performs a simple substring search
func (idx *GlobalSearchIndex) Search(query string) []*SearchResult {
	if query == "" {
		idx.resetQueryCache()
		return nil
	}

	if idx.tier == TierBalanced {
		return idx.searchOnDisk(query)
	}

	entries := idx.entries.Load()
	if entries == nil {
		return nil
	}

	queryLower := []byte(strings.ToLower(query))
	var results []*SearchResult

	for i := range *entries {
		entry := &(*entries)[i]
		if entry.content == nil {
			continue
		}
		hasMatch := false
		entry.content.With(func(_, lower []byte) {
			hasMatch = bytes.Contains(lower, queryLower)
		})
		if !hasMatch {
			continue
		}
		matches := entry.Match(query)
		results = append(results, &SearchResult{
			Entry:   entry,
			Matches: matches,
			Score:   len(matches) * 10,
			Snippet: entry.GetSnippet(query, 60),
		})
	}

	// Sort by score (more matches = higher score) - O(n log n)
	sort.Slice(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})

	return results
}

// fuzzySearchSource implements fuzzy.Source for our entries
type fuzzySearchSource struct {
	entries *[]SearchEntry
}

func (s fuzzySearchSource) String(i int) string {
	entry := &(*s.entries)[i]
	// Use summary + first part of content for fuzzy matching
	contentPreview := entry.ContentPreview(500)
	if contentPreview == "" {
		return entry.Summary
	}
	return entry.Summary + " " + contentPreview
}

func (s fuzzySearchSource) Len() int {
	return len(*s.entries)
}

// FuzzySearch performs fuzzy matching with typo tolerance
func (idx *GlobalSearchIndex) FuzzySearch(query string) []*SearchResult {
	if query == "" {
		return nil
	}

	entries := idx.entries.Load()
	if entries == nil {
		return nil
	}

	// Create fuzzy source
	source := fuzzySearchSource{entries: entries}

	// Fuzzy match
	matches := fuzzy.FindFrom(query, source)

	var results []*SearchResult
	for _, match := range matches {
		entry := &(*entries)[match.Index]
		results = append(results, &SearchResult{
			Entry:   entry,
			Score:   match.Score,
			Snippet: entry.GetSnippet(query, 60),
		})
	}

	return results
}

// GetTier returns the current search tier
func (idx *GlobalSearchIndex) GetTier() SearchTier {
	return idx.tier
}

// EntryCount returns the number of indexed entries
func (idx *GlobalSearchIndex) EntryCount() int {
	entries := idx.entries.Load()
	if entries == nil {
		return 0
	}
	return len(*entries)
}

// IsLoading returns true if the index is still loading
func (idx *GlobalSearchIndex) IsLoading() bool {
	return idx.loading.Load()
}

// Close shuts down the index and releases all memory
func (idx *GlobalSearchIndex) Close() {
	idx.cancel()
	if idx.watcher != nil {
		idx.watcher.Close()
	}
	idx.wg.Wait()

	// Release all content memory
	emptyEntries := make([]SearchEntry, 0)
	idx.entries.Store(&emptyEntries)
	idx.currentMemoryBytes.Store(0)

	// Clear file trackers
	idx.trackerMu.Lock()
	idx.fileTrackers = make(map[string]*FileTracker)
	idx.trackerMu.Unlock()

	// Clear query cache
	idx.resetQueryCache()
}

func matchRanges(lower []byte, queryLower []byte) []MatchRange {
	if len(lower) == 0 || len(queryLower) == 0 {
		return nil
	}
	var matches []MatchRange
	start := 0
	for {
		idx := bytes.Index(lower[start:], queryLower)
		if idx == -1 {
			break
		}
		absIdx := start + idx
		matches = append(matches, MatchRange{
			Start: absIdx,
			End:   absIdx + len(queryLower),
		})
		start = absIdx + len(queryLower)
	}
	return matches
}

func formatMessageContent(msg claudeMessage) string {
	rolePrefix := ""
	switch msg.Role {
	case "user":
		rolePrefix = "User: "
	case "assistant":
		rolePrefix = "Assistant: "
	}

	contentStr := extractContentText(msg.Content)
	if contentStr == "" {
		return ""
	}
	if rolePrefix != "" {
		return rolePrefix + contentStr
	}
	return contentStr
}

func extractContentText(contentRaw json.RawMessage) string {
	var contentStr string
	if err := json.Unmarshal(contentRaw, &contentStr); err == nil {
		return contentStr
	}

	var blocks []map[string]interface{}
	if err := json.Unmarshal(contentRaw, &blocks); err != nil {
		return ""
	}
	var sb strings.Builder
	for i, block := range blocks {
		text, ok := block["text"].(string)
		if !ok || text == "" {
			continue
		}
		if i > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString(text)
	}
	return sb.String()
}

func (idx *GlobalSearchIndex) searchOnDisk(query string) []*SearchResult {
	entries := idx.entries.Load()
	if entries == nil {
		return nil
	}

	queryLower := strings.ToLower(query)

	candidates := idx.queryCandidates(query, entries)

	// Parallel search with worker pool (cap at 8 workers)
	numWorkers := 8
	if len(candidates) < numWorkers {
		numWorkers = len(candidates)
	}
	if numWorkers == 0 {
		return nil
	}

	type searchHit struct {
		entry   *SearchEntry
		count   int
		snippet string
	}

	jobs := make(chan *SearchEntry, len(candidates))
	hits := make(chan searchHit, len(candidates))

	var wg sync.WaitGroup
	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for entry := range jobs {
				matchCount, snippet := scanFileForQuery(entry.FilePath, queryLower, 60)
				if matchCount > 0 {
					hits <- searchHit{entry: entry, count: matchCount, snippet: snippet}
				}
			}
		}()
	}

	for _, entry := range candidates {
		jobs <- entry
	}
	close(jobs)

	go func() {
		wg.Wait()
		close(hits)
	}()

	var results []*SearchResult
	for hit := range hits {
		results = append(results, &SearchResult{
			Entry:   hit.entry,
			Score:   hit.count * 10,
			Snippet: hit.snippet,
		})
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})

	idx.storeQueryCache(query, results)
	return results
}

func (idx *GlobalSearchIndex) queryCandidates(query string, entries *[]SearchEntry) []*SearchEntry {
	idx.lastQueryMu.Lock()
	usePrev := idx.lastQuery != "" && strings.HasPrefix(query, idx.lastQuery) && len(query) > len(idx.lastQuery)
	if usePrev && len(idx.lastResults) > 0 {
		candidates := make([]*SearchEntry, 0, len(idx.lastResults))
		for _, res := range idx.lastResults {
			if res != nil && res.Entry != nil {
				candidates = append(candidates, res.Entry)
			}
		}
		idx.lastQueryMu.Unlock()
		return candidates
	}
	idx.lastQueryMu.Unlock()

	candidates := make([]*SearchEntry, 0, len(*entries))
	for i := range *entries {
		candidates = append(candidates, &(*entries)[i])
	}
	return candidates
}

func (idx *GlobalSearchIndex) storeQueryCache(query string, results []*SearchResult) {
	idx.lastQueryMu.Lock()
	idx.lastQuery = query
	idx.lastResults = results
	idx.lastQueryMu.Unlock()
}

func (idx *GlobalSearchIndex) resetQueryCache() {
	idx.lastQueryMu.Lock()
	idx.lastQuery = ""
	idx.lastResults = nil
	idx.lastQueryMu.Unlock()
}

func scanFileForQuery(path string, queryLower string, windowSize int) (int, string) {
	file, err := os.Open(path)
	if err != nil {
		return 0, ""
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 10*1024*1024)

	matchCount := 0
	snippet := ""

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var record claudeJSONLRecord
		if err := json.Unmarshal(line, &record); err != nil {
			continue
		}
		if len(record.Message) == 0 {
			continue
		}
		var msg claudeMessage
		if err := json.Unmarshal(record.Message, &msg); err != nil {
			continue
		}
		content := formatMessageContent(msg)
		if content == "" {
			continue
		}
		contentLower := strings.ToLower(content)
		if !strings.Contains(contentLower, queryLower) {
			continue
		}

		matchCount += strings.Count(contentLower, queryLower)
		if snippet == "" {
			snippet = snippetFromText(content, queryLower, windowSize)
		}
	}

	if err := scanner.Err(); err != nil {
		return matchCount, snippet
	}

	return matchCount, snippet
}

func snippetFromText(content string, queryLower string, windowSize int) string {
	if content == "" {
		return ""
	}
	lower := strings.ToLower(content)
	matchIdx := strings.Index(lower, queryLower)
	runes := []rune(content)

	if matchIdx == -1 {
		if len(runes) > windowSize*2 {
			return string(runes[:windowSize*2]) + "..."
		}
		return content
	}

	matchStart := byteIndexToRuneIndex(content, matchIdx)
	matchEnd := byteIndexToRuneIndex(content, matchIdx+len(queryLower))

	start := matchStart - windowSize
	if start < 0 {
		start = 0
	}
	end := matchEnd + windowSize
	if end > len(runes) {
		end = len(runes)
	}

	for start > 0 && runes[start-1] != ' ' && runes[start-1] != '\n' {
		start--
	}
	for end < len(runes) && runes[end] != ' ' && runes[end] != '\n' {
		end++
	}

	snippet := string(runes[start:end])
	prefix := ""
	suffix := ""
	if start > 0 {
		prefix = "..."
	}
	if end < len(runes) {
		suffix = "..."
	}

	return prefix + strings.TrimSpace(snippet) + suffix
}
