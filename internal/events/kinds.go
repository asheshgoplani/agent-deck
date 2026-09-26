package events

import (
	"bufio"
	"encoding/json"
	"os"
)

// KindCounts counts the retained frames per kind across every sealed
// segment and the active one (`events stats --json` "kinds").
func (b *Bus) KindCounts() (map[string]uint64, error) {
	out := map[string]uint64{}
	if b == nil || !b.enabled {
		return out, nil
	}
	segs, err := b.listAllSegments()
	if err != nil {
		return nil, err
	}
	for _, seg := range segs {
		f, err := os.Open(seg.path)
		if os.IsNotExist(err) {
			continue // compacted or sealed since the listing
		}
		if err != nil {
			return nil, err
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 64<<10), 16<<20)
		for sc.Scan() {
			var k struct {
				Kind string `json:"kind"`
			}
			if json.Unmarshal(sc.Bytes(), &k) == nil && k.Kind != "" {
				out[k.Kind]++
			}
		}
		err = sc.Err()
		f.Close()
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}
