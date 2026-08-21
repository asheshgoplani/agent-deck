package script

import (
	"bufio"
	"bytes"
	"fmt"
	"strings"
)

// PlaceholderToken marks the parts of a scaffolded script an author must
// replace. It exists so `sixgate scaffold` can emit a schema-shaped skeleton
// without that skeleton accidentally counting as a written journey — which
// would defeat the entire ordering rule.
const PlaceholderToken = "REPLACE-ME"

// CheckPlaceholders reports every line of a script that still carries the
// scaffold's placeholder. Run alongside Validate: a script that parses cleanly
// but still says REPLACE-ME is not a script, it is a form nobody filled in.
func CheckPlaceholders(raw []byte) Problems {
	var ps Problems
	sc := bufio.NewScanner(bytes.NewReader(raw))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for n := 1; sc.Scan(); n++ {
		line := sc.Text()
		if strings.Contains(line, PlaceholderToken) {
			ps = append(ps, Problem{
				Path: fmt.Sprintf("line %d", n),
				Msg:  "still contains " + PlaceholderToken + ": write the journey before building the feature",
			})
		}
	}
	if err := sc.Err(); err != nil {
		ps = append(ps, Problem{Path: "file", Msg: "read: " + err.Error()})
	}
	if len(ps) == 0 {
		return nil
	}
	return ps
}
