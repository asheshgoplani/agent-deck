package main

import (
	"os"
	"strconv"
	"sync"
)

// defaultMaxParallelLaunch caps in-flight `agent-deck launch` spawns when no
// AGENT_DECK_MAX_PARALLEL_LAUNCH override is set. 3 matches the safe-launch
// convention and keeps total claude+toolchain RAM under ~6-9GB on typical
// dev hosts. See issue #964 for the swap-thrash incidents that motivated it.
const defaultMaxParallelLaunch = 3

// launchThrottle is a buffered-channel semaphore that bounds the number of
// concurrent launch spawns. Used by handleLaunch to gate the Start /
// StartWithMessage call so a burst of parallel `agent-deck launch` invocations
// cannot cascade into swap thrash + fork:ENOMEM.
type launchThrottle struct {
	sem chan struct{}
}

func newLaunchThrottle(cap int) *launchThrottle {
	if cap < 1 {
		cap = 1
	}
	return &launchThrottle{sem: make(chan struct{}, cap)}
}

func (t *launchThrottle) Acquire() { t.sem <- struct{}{} }
func (t *launchThrottle) Release() { <-t.sem }

// launchThrottleCap resolves the effective cap, honouring
// AGENT_DECK_MAX_PARALLEL_LAUNCH when it parses to a positive int.
func launchThrottleCap() int {
	if v := os.Getenv("AGENT_DECK_MAX_PARALLEL_LAUNCH"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return defaultMaxParallelLaunch
}

var (
	globalLaunchThrottle     *launchThrottle
	globalLaunchThrottleOnce sync.Once
)

func defaultLaunchThrottle() *launchThrottle {
	globalLaunchThrottleOnce.Do(func() {
		globalLaunchThrottle = newLaunchThrottle(launchThrottleCap())
	})
	return globalLaunchThrottle
}
