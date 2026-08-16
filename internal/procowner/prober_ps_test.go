package procowner

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The BSD `ps` provider cannot be exercised end to end on the machine that runs
// CI, so the part that can hold a bug — the parsing — is tested here against
// real macOS output shapes. The rest of the provider is three exec calls.

func TestParsePSLine_RealDarwinRows(t *testing.T) {
	// `ps -Ao pid=,ppid=,pgid=,uid=,lstart=` on macOS: right-aligned numeric
	// columns, then a date whose day-of-month is space padded.
	info, err := parsePSLine("  4711   4710   4711    501 S+   Sat Aug 16 17:21:38 2026")
	require.NoError(t, err)
	assert.Equal(t, 4711, info.PID)
	assert.Equal(t, 4710, info.PPID)
	assert.Equal(t, 4711, info.PGID)
	assert.Equal(t, 501, info.UID)
	assert.Equal(t, "Sat Aug 16 17:21:38 2026", info.StartID)

	// Single-digit day: ps pads it, and the identity must normalise to the same
	// shape it will be compared against later.
	padded, err := parsePSLine("  4711   4710   4711    501 Ss   Tue Sep  2 07:05:01 2025")
	require.NoError(t, err)
	assert.Equal(t, "Tue Sep 2 07:05:01 2025", padded.StartID)
}

func TestParsePSLine_RejectsUnusableRows(t *testing.T) {
	for _, row := range []string{
		"",
		"4711",
		"4711 4710 4711 501 S", // no lstart
		"nope 4710 4711 501 S Sat Aug 16 17:21:38 2026", // unparseable pid
	} {
		_, err := parsePSLine(row)
		require.Error(t, err, "row %q must be refused", row)
	}
}

func TestParsePSTable_SkipsGarbageRows(t *testing.T) {
	table := parsePSTable(`
  1      0      1      0 Ss   Sat Aug 16 09:00:00 2026
garbage row that ps would never print
  4711   1   4711    501 S+   Sat Aug 16 17:21:38 2026
`)
	require.Len(t, table, 2)
	assert.Equal(t, 1, table[0].PID)
	assert.Equal(t, 4711, table[1].PID)
}

func TestDescendantsFromTable(t *testing.T) {
	table := []ProcInfo{
		{PID: 100, PPID: 1, StartID: "a"},
		{PID: 101, PPID: 100, StartID: "b"},
		{PID: 102, PPID: 101, StartID: "c"},
		{PID: 103, PPID: 100, StartID: "e", State: "Z"},
		{PID: 200, PPID: 1, StartID: "d"},
	}
	kids := descendantsFromTable(ProcInfo{PID: 100}, table)
	require.Len(t, kids, 2, "a zombie descendant is not a live process")
	assert.Equal(t, 101, kids[0].PID)
	assert.Equal(t, 102, kids[1].PID)
}

func TestParseKernBootTime(t *testing.T) {
	id, err := parseKernBootTime("{ sec = 1755300000, usec = 123456 } Sat Aug 16 09:00:00 2026\n")
	require.NoError(t, err)
	assert.Equal(t, "1755300000", id)

	_, err = parseKernBootTime("nothing useful here")
	require.Error(t, err)
}

func TestParseLstartOrdering(t *testing.T) {
	earlier, err := parseLstart("Sat Aug 16 17:21:38 2026")
	require.NoError(t, err)
	later, err := parseLstart("Sat Aug 16 17:21:39 2026")
	require.NoError(t, err)
	assert.True(t, earlier.Before(later))

	_, err = parseLstart("not a date")
	require.Error(t, err)
}

// The `ps` format string and the parser have to agree, and they live in
// different halves of the provider — one behind a darwin build tag, one not.
// This pins the coupling on every platform that runs the suite.
func TestPSFieldOrderMatchesTheParser(t *testing.T) {
	columns := strings.Split(strings.TrimSuffix(psFieldOrder, ","), ",")
	require.Len(t, columns, 6, "parsePSLine consumes four integers, a state, then lstart")
	assert.Equal(t, "lstart=", columns[len(columns)-1],
		"lstart must stay last: it is the only column containing spaces")
	assert.Equal(t, "state=", columns[len(columns)-2],
		"state must stay immediately before lstart: parsePSLine reads it positionally")
}
