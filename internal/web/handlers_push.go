package web

import (
	"encoding/json"
	"net/http"
	"strings"
)

type pushConfigResponse struct {
	Enabled           bool   `json:"enabled"`
	VAPIDPublicKey    string `json:"vapidPublicKey,omitempty"`
	Subject           string `json:"subject,omitempty"`
	SubscriptionCount int    `json:"subscriptionCount,omitempty"`
}

type pushResultResponse struct {
	OK      bool   `json:"ok"`
	Message string `json:"message,omitempty"`
}

type pushUnsubscribeRequest struct {
	Endpoint string `json:"endpoint"`
}

type pushPresenceRequest struct {
	Endpoint string `json:"endpoint"`
	Focused  *bool  `json:"focused"`
}

func (s *Server) handlePushConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
		return
	}
	if !s.authorizeRequest(r) {
		writeAPIError(w, http.StatusUnauthorized, "UNAUTHORIZED", "unauthorized")
		return
	}

	resp := pushConfigResponse{
		Enabled: s.push != nil && s.push.Enabled(),
	}
	if !resp.Enabled {
		writeJSON(w, http.StatusOK, resp)
		return
	}

	resp.VAPIDPublicKey = s.push.PublicKey()
	resp.Subject = s.push.Subject()
	if count, err := s.push.SubscriptionCount(r.Context()); err == nil {
		resp.SubscriptionCount = count
	}

	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handlePushSubscribe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
		return
	}
	if !s.authorizeRequest(r) {
		writeAPIError(w, http.StatusUnauthorized, "UNAUTHORIZED", "unauthorized")
		return
	}
	if s.push == nil || !s.push.Enabled() {
		writeAPIError(w, http.StatusServiceUnavailable, "PUSH_NOT_CONFIGURED", "push notifications are not configured")
		return
	}

	var sub pushSubscription
	if err := json.NewDecoder(r.Body).Decode(&sub); err != nil {
		writeAPIError(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid subscription payload")
		return
	}
	sub = sub.normalize()
	if err := sub.validate(); err != nil {
		writeAPIError(w, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
		return
	}

	if err := s.push.UpsertSubscription(r.Context(), sub); err != nil {
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to save push subscription")
		return
	}

	writeJSON(w, http.StatusOK, pushResultResponse{
		OK:      true,
		Message: "subscription saved",
	})
}

func (s *Server) handlePushUnsubscribe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
		return
	}
	if !s.authorizeRequest(r) {
		writeAPIError(w, http.StatusUnauthorized, "UNAUTHORIZED", "unauthorized")
		return
	}
	if s.push == nil || !s.push.Enabled() {
		writeAPIError(w, http.StatusServiceUnavailable, "PUSH_NOT_CONFIGURED", "push notifications are not configured")
		return
	}

	var req pushUnsubscribeRequest
	_ = json.NewDecoder(r.Body).Decode(&req)
	req.Endpoint = strings.TrimSpace(req.Endpoint)
	if req.Endpoint == "" {
		writeAPIError(w, http.StatusBadRequest, "INVALID_REQUEST", "endpoint is required")
		return
	}

	if err := s.push.RemoveSubscriptionByEndpoint(r.Context(), req.Endpoint); err != nil {
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to remove push subscription")
		return
	}

	writeJSON(w, http.StatusOK, pushResultResponse{
		OK:      true,
		Message: "subscription removed",
	})
}

func (s *Server) handlePushPresence(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
		return
	}
	if !s.authorizeRequest(r) {
		writeAPIError(w, http.StatusUnauthorized, "UNAUTHORIZED", "unauthorized")
		return
	}
	if s.push == nil || !s.push.Enabled() {
		writeAPIError(w, http.StatusServiceUnavailable, "PUSH_NOT_CONFIGURED", "push notifications are not configured")
		return
	}

	var req pushPresenceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid presence payload")
		return
	}
	req.Endpoint = strings.TrimSpace(req.Endpoint)
	if req.Endpoint == "" || req.Focused == nil {
		writeAPIError(w, http.StatusBadRequest, "INVALID_REQUEST", "endpoint and focused are required")
		return
	}

	if err := s.push.UpdateSubscriptionFocus(r.Context(), req.Endpoint, *req.Focused); err != nil {
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to update push presence")
		return
	}

	writeJSON(w, http.StatusOK, pushResultResponse{
		OK:      true,
		Message: "push presence updated",
	})
}
