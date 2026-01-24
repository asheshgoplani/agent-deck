package desktop

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestGetVersion(t *testing.T) {
	app := NewApp()
	version := app.GetVersion()
	assert.NotEmpty(t, version)
	assert.True(t, strings.Contains(version, "."), "Version should contain a dot")
}

func TestAppContextInitialization(t *testing.T) {
	app := NewApp()
	ctx := context.Background()
	app.Startup(ctx)
	// Verify app was created successfully
	assert.NotNil(t, app)
}

func TestNewApp(t *testing.T) {
	app := NewApp()
	assert.NotNil(t, app)
}
