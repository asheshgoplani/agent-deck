package web

import "net/http"

// featureRoutes collects per-feature route registrations. A handlers_<feature>.go
// file appends its own routes from an init() so the wiring for a feature lives
// next to its handlers instead of in newServer. Core routes (index, static,
// sessions, groups, settings, push, events, ws) stay in server.go.
var featureRoutes []func(s *Server, mux *http.ServeMux)

func registerFeatureRoutes(fn func(s *Server, mux *http.ServeMux)) {
	featureRoutes = append(featureRoutes, fn)
}

func (s *Server) mountFeatureRoutes(mux *http.ServeMux) {
	for _, fn := range featureRoutes {
		fn(s, mux)
	}
}
