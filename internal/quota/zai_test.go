package quota

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// serveZai starts a stub monitor endpoint, points the process environment at it,
// and widens the host allowlist to cover httptest's loopback address for the
// duration of the test. The allowlist is the production guard that the
// destination is a z.ai-family host the user configured; it is relaxed here
// only so a stub can stand in for that host, never in production code.
func serveZai(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	parsed, err := url.Parse(server.URL)
	require.NoError(t, err)
	previous := zaiHostSuffixes
	zaiHostSuffixes = append(append([]string(nil), previous...), parsed.Hostname())
	t.Cleanup(func() { zaiHostSuffixes = previous })

	t.Setenv("ANTHROPIC_BASE_URL", server.URL+"/api/anthropic")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", sentinelToken)
	return server
}

// verifiedPayload is the body observed live from a Pro plan on 2026-09-07,
// anonymised with magnitudes kept. Both windows carry type=CREDIT_LIMIT and the
// weekly one is listed FIRST, so a parser keyed on `type` or on array order
// fails this fixture.
const verifiedPayload = `{
  "code": 200, "msg": "Operation successful", "success": true,
  "data": {
    "level": "pro",
    "limits": [
      {"type":"CREDIT_LIMIT","unit":6,"number":1,"usage":60000,"currentValue":13295,
       "remaining":46704,"percentage":22,"nextResetTime":1789135610964},
      {"type":"CREDIT_LIMIT","unit":3,"number":5,"usage":12000,"currentValue":0,
       "remaining":12000,"percentage":0}
    ]
  }
}`

func TestFetchZaiVerifiedPayload(t *testing.T) {
	var requests atomic.Int32
	var gotPath, gotAuth string
	serveZai(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(verifiedPayload))
	})

	snapshot, err := FetchZai(context.Background(), http.DefaultClient)
	require.NoError(t, err)

	assert.Equal(t, int32(1), requests.Load())
	// The Anthropic-compat path suffix is stripped and the monitor path
	// appended; the host is whatever the user configured.
	assert.Equal(t, "/api/monitor/usage/quota/limit", gotPath)
	// The vendor's own plugin sends the bare token, not "Bearer <token>".
	assert.Equal(t, sentinelToken, gotAuth)

	assert.Equal(t, ProviderZai, snapshot.ID)
	assert.Equal(t, "pro", snapshot.Plan)
	require.Len(t, snapshot.Windows, 2)

	fiveHour := windowByKind(t, snapshot, WindowFiveHour)
	assert.Equal(t, float64(0), fiveHour.UsedPercentage)
	sevenDay := windowByKind(t, snapshot, WindowSevenDay)
	assert.Equal(t, float64(22), sevenDay.UsedPercentage)
}

func TestFetchZaiPercentageIsConsumptionNotBudget(t *testing.T) {
	// `usage` is the BUDGET and `currentValue` the consumption. The field names
	// invite exactly the wrong reading, so pin it: 13295 of 60000 is 22%
	// consumed, and 60000 must never surface as a percentage.
	serveZai(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(verifiedPayload))
	})

	snapshot, err := FetchZai(context.Background(), http.DefaultClient)
	require.NoError(t, err)
	sevenDay := windowByKind(t, snapshot, WindowSevenDay)
	assert.Equal(t, float64(22), sevenDay.UsedPercentage)
	assert.NotEqual(t, float64(60000), sevenDay.UsedPercentage)
}

func TestFetchZaiPercentageDerivedWhenAbsent(t *testing.T) {
	serveZai(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"level":"pro","limits":[
		  {"unit":6,"number":1,"usage":1000,"currentValue":250}]}}`))
	})

	snapshot, err := FetchZai(context.Background(), http.DefaultClient)
	require.NoError(t, err)
	require.Len(t, snapshot.Windows, 1)
	assert.Equal(t, float64(25), snapshot.Windows[0].UsedPercentage)
}

func TestFetchZaiResetTime(t *testing.T) {
	serveZai(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(verifiedPayload))
	})

	snapshot, err := FetchZai(context.Background(), http.DefaultClient)
	require.NoError(t, err)

	sevenDay := windowByKind(t, snapshot, WindowSevenDay)
	require.NotNil(t, sevenDay.ResetsAt)
	// nextResetTime is epoch MILLISECONDS.
	assert.Equal(t, int64(1789135610), *sevenDay.ResetsAt)

	// The 5h window carried no nextResetTime. Nothing may be synthesised for it.
	fiveHour := windowByKind(t, snapshot, WindowFiveHour)
	assert.Nil(t, fiveHour.ResetsAt)
}

func TestFetchZaiUnknownWindowKept(t *testing.T) {
	// Evidence exists for unit=3 (hour) and unit=6 (week) only. An unknown
	// pair is kept and labelled from the provider's own numbers rather than
	// dropped or given a duration it may not have.
	serveZai(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"level":"pro","limits":[
		  {"unit":9,"number":2,"usage":100,"currentValue":50,"percentage":50}]}}`))
	})

	snapshot, err := FetchZai(context.Background(), http.DefaultClient)
	require.NoError(t, err)
	require.Len(t, snapshot.Windows, 1)
	assert.Equal(t, WindowOther, snapshot.Windows[0].Kind)
	assert.Equal(t, "unit9x2", snapshot.Windows[0].Label)
	assert.Equal(t, float64(50), snapshot.Windows[0].UsedPercentage)
}

func TestFetchZaiEmptyLimits(t *testing.T) {
	serveZai(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"level":"pro","limits":[]}}`))
	})

	snapshot, err := FetchZai(context.Background(), http.DefaultClient)
	require.NoError(t, err)
	assert.Empty(t, snapshot.Windows)
	assert.Empty(t, snapshot.Error)
}

func TestFetchZaiHTTPErrorsNeverLeakToken(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusTooManyRequests, http.StatusInternalServerError} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			serveZai(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
				// A server that echoes the credential back must not be able to
				// push it into an error string agent-deck persists and prints.
				_, _ = w.Write([]byte(`{"msg":"` + sentinelToken + `"}`))
			})

			_, err := FetchZai(context.Background(), http.DefaultClient)
			require.Error(t, err)
			assert.Contains(t, err.Error(), http.StatusText(status))
			assert.NotContains(t, err.Error(), sentinelToken)
		})
	}
}

func TestFetchZaiTimeout(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	serveZai(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	})

	client := &http.Client{Timeout: 100 * time.Millisecond}
	start := time.Now()
	_, err := FetchZai(context.Background(), client)
	require.Error(t, err)
	assert.Less(t, time.Since(start), 5*time.Second)
	assert.NotContains(t, err.Error(), sentinelToken)
}

func TestFetchZaiNotConfiguredMakesNoRequest(t *testing.T) {
	var requests atomic.Int32
	serveZai(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = w.Write([]byte(verifiedPayload))
	})

	tests := []struct {
		name    string
		baseURL string
		token   string
	}{
		{"base url unset", "", sentinelToken},
		{"token unset", "https://api.z.ai/api/anthropic", ""},
		{"host not z.ai family", "https://evil.example.com/api/anthropic", sentinelToken},
		{"base url unparseable", "://nonsense", sentinelToken},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("ANTHROPIC_BASE_URL", tt.baseURL)
			t.Setenv("ANTHROPIC_AUTH_TOKEN", tt.token)

			_, err := FetchZai(context.Background(), http.DefaultClient)
			require.Error(t, err)
			assert.True(t, errors.Is(err, ErrNotConfigured), "want ErrNotConfigured, got %v", err)
			assert.NotContains(t, err.Error(), sentinelToken)
		})
	}
	// The point of the assertion: an unconfigured provider reaches no network
	// at all, rather than making a request that happens to fail.
	assert.Equal(t, int32(0), requests.Load())
}

func TestFetchZaiCapsResponseBody(t *testing.T) {
	previous := maxZaiBodyBytes
	maxZaiBodyBytes = 512
	t.Cleanup(func() { maxZaiBodyBytes = previous })

	serveZai(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"level":"` + strings.Repeat("x", 4096) + `","limits":[]}}`))
	})

	_, err := FetchZai(context.Background(), http.DefaultClient)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), sentinelToken)
}

func TestZaiQuotaEndpoint(t *testing.T) {
	tests := []struct {
		name    string
		baseURL string
		want    string
		wantErr bool
	}{
		{"anthropic compat suffix stripped", "https://api.z.ai/api/anthropic", "https://api.z.ai/api/monitor/usage/quota/limit", false},
		{"trailing slash", "https://api.z.ai/api/anthropic/", "https://api.z.ai/api/monitor/usage/quota/limit", false},
		{"bare host", "https://api.z.ai", "https://api.z.ai/api/monitor/usage/quota/limit", false},
		{"regional subdomain", "https://open.bigmodel.z.ai/api/anthropic", "https://open.bigmodel.z.ai/api/monitor/usage/quota/limit", false},
		{"empty", "", "", true},
		{"unrelated host", "https://api.anthropic.com", "", true},
		{"host suffix lookalike", "https://notz.ai/api/anthropic", "", true},
		{"unparseable", "://nonsense", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := zaiQuotaEndpoint(tt.baseURL)
			if tt.wantErr {
				require.Error(t, err)
				assert.True(t, errors.Is(err, ErrNotConfigured))
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}
