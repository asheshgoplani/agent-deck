package hub

import (
	"testing"
)

func TestRouteExactKeywordMatch(t *testing.T) {
	projects := []*Project{
		{Name: "api-service", Keywords: []string{"api", "backend", "auth"}},
		{Name: "web-app", Keywords: []string{"frontend", "ui", "react"}},
	}

	result := Route("Fix the auth endpoint in the API", projects)

	if result == nil {
		t.Fatal("expected a route result")
	}
	if result.Project != "api-service" {
		t.Fatalf("expected api-service, got %s", result.Project)
	}
	if result.Confidence <= 0 {
		t.Fatalf("expected positive confidence, got %f", result.Confidence)
	}
	if len(result.MatchedKeywords) == 0 {
		t.Fatal("expected matched keywords")
	}
}

func TestRouteMultipleKeywordsIncreasesConfidence(t *testing.T) {
	projects := []*Project{
		{Name: "api-service", Keywords: []string{"api", "backend", "auth"}},
		{Name: "web-app", Keywords: []string{"frontend", "ui", "react"}},
	}

	single := Route("Fix the API", projects)
	multi := Route("Fix the auth endpoint in the API backend", projects)

	if single == nil || multi == nil {
		t.Fatal("expected route results")
	}
	if multi.Confidence <= single.Confidence {
		t.Fatalf("more keywords should increase confidence: single=%f multi=%f",
			single.Confidence, multi.Confidence)
	}
}

func TestRouteNoMatch(t *testing.T) {
	projects := []*Project{
		{Name: "api-service", Keywords: []string{"api", "backend", "auth"}},
	}

	result := Route("Update the documentation for kubernetes", projects)

	if result != nil {
		t.Fatalf("expected nil for no match, got project=%s confidence=%f",
			result.Project, result.Confidence)
	}
}

func TestRouteCaseInsensitive(t *testing.T) {
	projects := []*Project{
		{Name: "api-service", Keywords: []string{"api", "backend"}},
	}

	result := Route("Fix the API Backend", projects)

	if result == nil {
		t.Fatal("expected case-insensitive match")
	}
	if result.Project != "api-service" {
		t.Fatalf("expected api-service, got %s", result.Project)
	}
}

func TestRouteBestMatchWins(t *testing.T) {
	projects := []*Project{
		{Name: "api-service", Keywords: []string{"api", "backend", "auth"}},
		{Name: "web-app", Keywords: []string{"frontend", "ui", "react", "api"}},
	}

	// "api" matches both, but "auth" and "backend" tip toward api-service.
	result := Route("Fix the auth in the API backend", projects)

	if result == nil {
		t.Fatal("expected a route result")
	}
	if result.Project != "api-service" {
		t.Fatalf("expected api-service (3 matches), got %s", result.Project)
	}
}

func TestRouteEmptyProjects(t *testing.T) {
	result := Route("Fix something", nil)
	if result != nil {
		t.Fatal("expected nil for empty projects")
	}
}

func TestRouteEmptyMessage(t *testing.T) {
	projects := []*Project{
		{Name: "api-service", Keywords: []string{"api"}},
	}
	result := Route("", projects)
	if result != nil {
		t.Fatal("expected nil for empty message")
	}
}

func TestRouteNoSubstringFalsePositive(t *testing.T) {
	projects := []*Project{
		{Name: "ui-app", Keywords: []string{"ui", "design"}},
	}

	// "build" contains "ui" as a substring — should NOT match.
	result := Route("build the new service", projects)

	if result != nil {
		t.Fatalf("expected nil for substring-only match, got project=%s", result.Project)
	}
}

func TestRouteTieBreakByConfidence(t *testing.T) {
	projects := []*Project{
		{Name: "big-project", Keywords: []string{"api", "backend", "auth", "deploy", "infra"}},
		{Name: "small-project", Keywords: []string{"api"}},
	}

	// Both match "api" (1 keyword each), but small-project has higher confidence (1/1 vs 1/5).
	result := Route("Fix the api", projects)

	if result == nil {
		t.Fatal("expected a route result")
	}
	if result.Project != "small-project" {
		t.Fatalf("expected small-project (higher confidence), got %s", result.Project)
	}
}
