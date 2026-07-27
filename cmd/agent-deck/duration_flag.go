package main

import (
	"flag"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// durationValue is a flag.Value for durations that also accepts a bare number,
// read as seconds.
//
// fs.Duration alone requires a unit suffix, so `--heartbeat 300` is a parse
// error. That is a poor failure for a supervision flag: the command exits
// non-zero printing its usage block, and a background-task harness that pipes
// the output (`agent-deck session children --follow --heartbeat 300 | head`)
// sees the pipeline's exit status, not the command's — so a watcher that never
// started reads as one running fine. Bare-seconds is also what every adjacent
// tool does (sleep, timeout, curl --max-time).
//
// Everything time.ParseDuration accepts keeps working unchanged; this only
// widens what parses, never changes an existing meaning.
type durationValue time.Duration

func (d *durationValue) Set(s string) error {
	s = strings.TrimSpace(s)
	if s == "" {
		return fmt.Errorf("empty duration")
	}
	if parsed, err := time.ParseDuration(s); err == nil {
		*d = durationValue(parsed)
		return nil
	}
	// Bare number => seconds. Parsed as a float so "1.5" means 1.5s rather than
	// being rejected for not being an integer.
	secs, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return fmt.Errorf("invalid duration %q (use a number of seconds, or a unit suffix like 30s / 5m)", s)
	}
	*d = durationValue(time.Duration(secs * float64(time.Second)))
	return nil
}

func (d *durationValue) String() string {
	if d == nil {
		return "0s"
	}
	return time.Duration(*d).String()
}

func (d *durationValue) Get() interface{} { return time.Duration(*d) }

// durationFlag registers a duration flag on fs and returns the destination,
// mirroring fs.Duration's signature so call sites read the same.
func durationFlag(fs *flag.FlagSet, name string, value time.Duration, usage string) *time.Duration {
	d := new(time.Duration)
	*d = value
	fs.Var((*durationValue)(d), name, usage)
	return d
}
