package logging

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"gopkg.in/natefinch/lumberjack.v2"
)

// Component constants for structured logging.
const (
	CompStatus  = "status"
	CompMCP     = "mcp"
	CompNotif   = "notif"
	CompPerf    = "perf"
	CompUI      = "ui"
	CompSession = "session"
	CompStorage = "storage"
	CompPool    = "pool"
	CompHTTP    = "http"
	CompWeb     = "web"
	CompWatcher = "watcher"
	CompUpdate  = "update"
)

// Config holds logging configuration.
type Config struct {
	// LogDir is the directory for log files (e.g. ~/.agent-deck)
	LogDir string

	// Level is the minimum log level: "debug", "info", "warn", "error"
	Level string

	// Format is "json" (default) or "text"
	Format string

	// MaxSizeMB is the max size in MB before rotation (default: 10)
	MaxSizeMB int

	// MaxBackups is rotated files to keep (default: 5)
	MaxBackups int

	// MaxAgeDays is days to keep rotated files (default: 10)
	MaxAgeDays int

	// Compress rotated files (default: true)
	Compress bool

	// RingBufferSize is the in-memory ring buffer size in bytes (default: 10MB)
	RingBufferSize int

	// AggregateIntervalSecs is the aggregation flush interval (default: 30)
	AggregateIntervalSecs int

	// PprofEnabled starts pprof server on localhost:6060
	PprofEnabled bool

	// Debug indicates whether debug mode is active
	Debug bool
}

var (
	globalLogger  *slog.Logger
	globalRing    *RingBuffer
	globalAgg     *Aggregator
	globalSlowOps *SlowOpDetector
	globalMu      sync.RWMutex
	lumberjackW   *lumberjack.Logger
	debugEnabled  atomic.Bool // Set once during Init, read lock-free thereafter
)

// Init initializes the global logging system.
// When debug is false and no log dir is provided, logs are discarded.
func Init(cfg Config) {
	globalMu.Lock()
	defer globalMu.Unlock()

	// Defaults
	if cfg.MaxSizeMB <= 0 {
		cfg.MaxSizeMB = 10
	}
	if cfg.MaxBackups <= 0 {
		cfg.MaxBackups = 5
	}
	if cfg.MaxAgeDays <= 0 {
		cfg.MaxAgeDays = 10
	}
	if cfg.RingBufferSize <= 0 {
		cfg.RingBufferSize = 10 * 1024 * 1024 // 10MB
	}
	if cfg.AggregateIntervalSecs <= 0 {
		cfg.AggregateIntervalSecs = 30
	}

	// Parse level
	level := slog.LevelInfo
	switch cfg.Level {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}

	// Store debug flag for lock-free reads
	debugEnabled.Store(cfg.Debug)

	// If not in debug mode and no explicit log dir, discard everything
	if !cfg.Debug && cfg.LogDir == "" {
		globalLogger = slog.New(slog.NewJSONHandler(io.Discard, nil))
		globalRing = NewRingBuffer(1024) // minimal
		globalAgg = NewAggregator(nil, cfg.AggregateIntervalSecs)
		return
	}

	// Set up lumberjack for rotation
	logPath := filepath.Join(cfg.LogDir, "debug.log")
	lumberjackW = &lumberjack.Logger{
		Filename:   logPath,
		MaxSize:    cfg.MaxSizeMB,
		MaxBackups: cfg.MaxBackups,
		MaxAge:     cfg.MaxAgeDays,
		Compress:   cfg.Compress,
	}

	// Ring buffer for crash dumps
	globalRing = NewRingBuffer(cfg.RingBufferSize)

	// MultiWriter: lumberjack + ring buffer
	multi := io.MultiWriter(lumberjackW, globalRing)

	// Create handler
	handlerOpts := &slog.HandlerOptions{
		Level: level,
	}

	var handler slog.Handler
	if cfg.Format == "text" {
		handler = slog.NewTextHandler(multi, handlerOpts)
	} else {
		handler = slog.NewJSONHandler(multi, handlerOpts)
	}

	globalLogger = slog.New(handler)

	// Aggregator
	globalAgg = NewAggregator(globalLogger, cfg.AggregateIntervalSecs)
	globalAgg.Start()

	// pprof
	if cfg.PprofEnabled {
		startPprof()
	}

	// Slow operation detector (debug mode only)
	if cfg.Debug {
		globalSlowOps = NewSlowOpDetector(3*time.Second, 1*time.Second)
	}
}

// Logger returns the global logger. Safe to call before Init (returns default).
func Logger() *slog.Logger {
	globalMu.RLock()
	defer globalMu.RUnlock()
	if globalLogger == nil {
		return slog.New(slog.NewJSONHandler(io.Discard, nil))
	}
	return globalLogger
}

// ForComponent returns a sub-logger with the component field set.
// Uses a dynamicHandler so that loggers created before Init() (e.g., as
// package-level vars) will correctly use the real handler once Init() runs.
func ForComponent(name string) *slog.Logger {
	return slog.New(&dynamicHandler{
		component: name,
	})
}

// dynamicHandler implements slog.Handler by delegating to the current global
// handler at log time. This fixes a critical bug where package-level component
// loggers (var uiLog = logging.ForComponent("ui")) were created before
// logging.Init() and permanently captured the discard handler, causing ALL
// component debug/warn/error messages to be silently lost.
type dynamicHandler struct {
	component string
	attrs     []slog.Attr
	group     string
}

func (h *dynamicHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return Logger().Handler().Enabled(ctx, level)
}

func (h *dynamicHandler) Handle(ctx context.Context, r slog.Record) error {
	handler := Logger().Handler()
	// Apply component attribute
	handler = handler.WithAttrs([]slog.Attr{slog.String("component", h.component)})
	// Apply any additional attrs accumulated via WithAttrs()
	if len(h.attrs) > 0 {
		handler = handler.WithAttrs(h.attrs)
	}
	if h.group != "" {
		handler = handler.WithGroup(h.group)
	}
	return handler.Handle(ctx, r)
}

func (h *dynamicHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	newAttrs := make([]slog.Attr, len(h.attrs)+len(attrs))
	copy(newAttrs, h.attrs)
	copy(newAttrs[len(h.attrs):], attrs)
	return &dynamicHandler{component: h.component, attrs: newAttrs, group: h.group}
}

func (h *dynamicHandler) WithGroup(name string) slog.Handler {
	return &dynamicHandler{component: h.component, attrs: h.attrs, group: name}
}

// Aggregate records a high-frequency event for batched logging.
func Aggregate(component, key string, fields ...slog.Attr) {
	globalMu.RLock()
	agg := globalAgg
	globalMu.RUnlock()
	if agg != nil {
		agg.Record(component, key, fields...)
	}
}

// DumpRingBuffer writes the ring buffer contents to a file.
func DumpRingBuffer(path string) error {
	globalMu.RLock()
	ring := globalRing
	globalMu.RUnlock()
	if ring == nil {
		return nil
	}
	return ring.DumpToFile(path)
}

// IsDebugEnabled returns true if debug mode was enabled during Init.
// Lock-free atomic read, safe for hot paths.
func IsDebugEnabled() bool {
	return debugEnabled.Load()
}

// TraceOp starts a timed span for a named operation. Call the returned function
// to finish the span. If elapsed exceeds warnThreshold, logs at Warn level;
// otherwise logs at Debug level. Returns a zero-cost no-op when debug is off.
//
// Usage:
//
//	finish := logging.TraceOp(myLog, "capture_pane", 200*time.Millisecond,
//	    slog.String("session", name))
//	defer finish()
func TraceOp(logger *slog.Logger, op string, warnThreshold time.Duration, attrs ...slog.Attr) func() {
	if !debugEnabled.Load() {
		return func() {}
	}
	start := time.Now()
	return func() {
		elapsed := time.Since(start)
		args := make([]any, 0, len(attrs)*2+2)
		args = append(args, slog.Duration("elapsed", elapsed))
		for _, a := range attrs {
			args = append(args, a)
		}
		if elapsed > warnThreshold {
			logger.Warn("slow_"+op, args...)
		} else {
			logger.Debug(op, args...)
		}
	}
}

// SlowOps returns the global slow-operation detector. Returns nil when debug is off.
func SlowOps() *SlowOpDetector {
	globalMu.RLock()
	defer globalMu.RUnlock()
	return globalSlowOps
}

// Shutdown flushes the aggregator and closes writers.
func Shutdown() {
	globalMu.Lock()
	defer globalMu.Unlock()

	if globalSlowOps != nil {
		globalSlowOps.Stop()
		globalSlowOps = nil
	}
	if globalAgg != nil {
		globalAgg.Stop()
		globalAgg = nil
	}
	if lumberjackW != nil {
		lumberjackW.Close()
		lumberjackW = nil
	}
	globalLogger = nil
	globalRing = nil
}
