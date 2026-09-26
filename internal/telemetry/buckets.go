package telemetry

import (
	"slices"
	"strings"
	"time"
)

// Bucket names a fixed, published set of edges (TELEMETRY.md, "Bucket edges").
// Lower edge inclusive, upper edge exclusive.
type Bucket string

const (
	BucketN      Bucket = "n"
	BucketDur    Bucket = "dur"
	BucketSince  Bucket = "since"
	BucketLen    Bucket = "len"
	BucketMS     Bucket = "ms"
	BucketRating Bucket = "rating"
	BucketAge    Bucket = "install_age"
)

// bucketLabels lists every label of each bucket, in ascending order.
var bucketLabels = map[Bucket][]string{
	BucketN:      {"0", "1", "2-3", "4-7", "8-15", "16-31", "32-63", "64+"},
	BucketDur:    {"<1m", "1-5m", "5-15m", "15-60m", "1-4h", "4-24h", "1-7d", "7d+"},
	BucketSince:  {"<1m", "1-5m", "5-30m", "30m-1d", "1-7d", "7d+"},
	BucketLen:    {"<50", "50-200", "200-1k", "1k+"},
	BucketMS:     {"<50", "50-100", "100-250", "250-500", "500-1000", "1-2s", "2-5s", "5s+"},
	BucketRating: {"1-2", "3", "4-5"},
	BucketAge:    {"d0", "d1", "d2-7", "d8-30", "d31-90", "d91+"},
}

// bucketOrder fixes the order buckets are published in.
var bucketOrder = []Bucket{BucketN, BucketDur, BucketSince, BucketLen, BucketMS, BucketRating, BucketAge}

// pick returns labels[i] for the first upper edge v is below, else the last label.
func pick[T int | time.Duration](labels []string, v T, uppers ...T) string {
	for i, u := range uppers {
		if v < u {
			return labels[i]
		}
	}
	return labels[len(labels)-1]
}

// CountBucket buckets a count; negative counts are "0".
func CountBucket(n int) string {
	return pick(bucketLabels[BucketN], n, 1, 2, 4, 8, 16, 32, 64)
}

// DurBucket buckets a duration; negative durations are "<1m".
func DurBucket(d time.Duration) string {
	return pick(bucketLabels[BucketDur], d, time.Minute, 5*time.Minute, 15*time.Minute,
		time.Hour, 4*time.Hour, 24*time.Hour, 7*24*time.Hour)
}

// SinceBucket buckets the time since an install was first seen.
func SinceBucket(d time.Duration) string {
	return pick(bucketLabels[BucketSince], d, time.Minute, 5*time.Minute, 30*time.Minute,
		24*time.Hour, 7*24*time.Hour)
}

// LenBucket buckets a character count; the characters themselves are never kept.
func LenBucket(chars int) string {
	return pick(bucketLabels[BucketLen], chars, 50, 200, 1000)
}

// MSBucket buckets a latency.
func MSBucket(d time.Duration) string {
	ms := time.Millisecond
	return pick(bucketLabels[BucketMS], d, 50*ms, 100*ms, 250*ms, 500*ms, 1000*ms, 2000*ms, 5000*ms)
}

// RatingBucket buckets a 1-5 rating.
func RatingBucket(r int) string {
	return pick(bucketLabels[BucketRating], r, 3, 4)
}

// AgeBucket buckets whole local days since first seen.
func AgeBucket(days int) string {
	return pick(bucketLabels[BucketAge], days, 1, 2, 8, 31, 91)
}

func validBucket(b Bucket, v string) bool {
	return contains(bucketLabels[b], v)
}

// toolBits is the fixed tool bitmask order; new tools append, "other" is bit 31.
var toolBits = []string{
	"claude", "codex", "gemini", "opencode", "pi", "copilot", "crush", "cursor",
	"hermes", "deepseek", "aider", "shell",
}

const otherToolBit = 31

// NormalizeTool maps a tool name to the built-in allow-list, else "other".
func NormalizeTool(tool string) string {
	tool = strings.ToLower(strings.TrimSpace(tool))
	if contains(toolBits, tool) {
		return tool
	}
	return toolOther
}

// ToolBit returns the bitmask bit for a tool name (normalised first).
func ToolBit(tool string) uint32 {
	if i := slices.Index(toolBits, NormalizeTool(tool)); i >= 0 {
		return 1 << uint(i)
	}
	return 1 << otherToolBit
}

// ToolMask ORs the bits of every tool name given.
func ToolMask(tools ...string) uint32 {
	var m uint32
	for _, t := range tools {
		m |= ToolBit(t)
	}
	return m
}

// HourBit returns the 24-bit mask bit of a local hour.
func HourBit(hour int) uint32 {
	if hour < 0 || hour > 23 {
		return 0
	}
	return 1 << uint(hour)
}

// ConfigSections is the fixed bit order of config.toml sections reported in
// env.snapshot.config_sections; any other section sets bit 31.
var ConfigSections = []string{
	"claude", "codex", "gemini", "opencode", "cursor", "copilot", "crush", "hermes",
	"deepseek", "tools", "mcps", "plugins", "profiles", "groups", "conductors", "conductor",
	"worktree", "tmux", "docker", "remotes", "notifications", "updates", "theme", "web",
	"telemetry", "feedback", "experiments", "harnesses", "shell", "hotkeys", "recall",
}

// ConfigSectionMask ORs the bits of the given section names.
func ConfigSectionMask(names ...string) uint32 {
	var m uint32
	for _, n := range names {
		if i := slices.Index(ConfigSections, n); i >= 0 {
			m |= 1 << uint(i)
		} else {
			m |= 1 << 31
		}
	}
	return m
}
