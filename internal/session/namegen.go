package session

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"time"
)

// adjectives for auto-generated session names (nature/weather themed)
var adjectives = []string{
	"amber", "ancient", "arctic", "autumn", "azure",
	"blazing", "bold", "bright", "bronze", "calm",
	"cedar", "clear", "coastal", "cool", "coral",
	"cosmic", "crimson", "crystal", "dappled", "dawn",
	"deep", "desert", "drifting", "dusky", "eager",
	"emerald", "fading", "fern", "fierce", "floral",
	"foggy", "forest", "frosty", "gentle", "gilded",
	"glacial", "gleaming", "golden", "granite", "hazy",
	"hidden", "hollow", "hushed", "icy", "indigo",
	"iron", "ivory", "jade", "keen", "lapis",
	"leafy", "light", "lively", "lunar", "marble",
	"meadow", "misty", "molten", "mossy", "nimble",
	"noble", "northern", "obsidian", "opal", "pale",
	"pearly", "pine", "polar", "prairie", "quartz",
	"quiet", "radiant", "rapid", "risen", "rocky",
	"rosy", "ruby", "rustic", "sandy", "scarlet",
	"shadow", "shining", "silent", "silver", "slate",
	"smoky", "solar", "spring", "starry", "steady",
	"stone", "stormy", "sunlit", "swift", "tawny",
	"thorny", "tidal", "topaz", "twilight", "verdant",
	"violet", "vivid", "wandering", "warm", "wild",
	"windy", "woven", "young", "zephyr",
}

// nouns for auto-generated session names (animals/nature themed)
var nouns = []string{
	"badger", "bear", "birch", "bison", "brook",
	"canyon", "cedar", "cliff", "cloud", "condor",
	"coral", "cougar", "crane", "creek", "crow",
	"delta", "dove", "dune", "eagle", "elm",
	"falcon", "fern", "finch", "fjord", "flower",
	"forest", "fox", "frost", "garden", "glacier",
	"grove", "gull", "harbor", "hawk", "heron",
	"hill", "hollow", "horizon", "island", "ivy",
	"jay", "juniper", "lake", "lark", "leaf",
	"lily", "lotus", "lynx", "maple", "marsh",
	"meadow", "mesa", "moon", "moss", "oak",
	"ocean", "orchid", "osprey", "otter", "owl",
	"palm", "panther", "peak", "pebble", "pine",
	"plover", "pond", "quail", "rain", "raven",
	"reef", "ridge", "river", "robin", "sage",
	"salmon", "shore", "sky", "sparrow", "spruce",
	"star", "stone", "storm", "stream", "summit",
	"swallow", "thistle", "thorn", "tide", "trail",
	"tulip", "valley", "vine", "wave", "willow",
	"wolf", "wren", "yarrow", "yew",
}

// GenerateSessionName returns a random "adjective-noun" name.
func GenerateSessionName() string {
	adj := adjectives[cryptoRandInt(len(adjectives))]
	noun := nouns[cryptoRandInt(len(nouns))]
	return adj + "-" + noun
}

// GenerateUniqueSessionName generates a name that doesn't collide with
// existing session titles in the given group. Retries up to 10 times,
// then falls back to appending a timestamp.
func GenerateUniqueSessionName(instances []*Instance, groupPath string) string {
	existing := make(map[string]bool)
	for _, inst := range instances {
		if inst.GroupPath == groupPath {
			existing[inst.Title] = true
		}
	}

	for range 10 {
		name := GenerateSessionName()
		if !existing[name] {
			return name
		}
	}

	// Fallback: append timestamp to guarantee uniqueness
	name := GenerateSessionName()
	return fmt.Sprintf("%s-%d", name, time.Now().Unix())
}

// cryptoRandInt returns a cryptographically random int in [0, max).
func cryptoRandInt(max int) int {
	n, err := rand.Int(rand.Reader, big.NewInt(int64(max)))
	if err != nil {
		// Fallback to timestamp-based selection if crypto/rand fails
		return int(time.Now().UnixNano() % int64(max))
	}
	return int(n.Int64())
}
