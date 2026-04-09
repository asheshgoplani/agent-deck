package ui

import (
	"os"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/testutil"
)

func TestMain(m *testing.M) {
	restoreHome, err := testutil.SetupTestHome()
	if err != nil {
		panic(err)
	}
	defer restoreHome()

	session.ClearUserConfigCache()
	os.Setenv("AGENTDECK_PROFILE", "_test")

	os.Exit(m.Run())
}
