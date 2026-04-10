package web

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/logging"
)

func (s *Server) handleCostsSummary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
		return
	}
	if !s.authorizeRequest(r) {
		writeAPIError(w, http.StatusUnauthorized, "UNAUTHORIZED", "unauthorized")
		return
	}
	if s.costStore == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "UNAVAILABLE", "cost tracking not enabled")
		return
	}

	today, err := s.costStore.TotalToday()
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to query today costs")
		return
	}
	week, err := s.costStore.TotalThisWeek()
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to query week costs")
		return
	}
	month, err := s.costStore.TotalThisMonth()
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to query month costs")
		return
	}
	projected, err := s.costStore.ProjectedMonthly()
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to calculate projection")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"today_usd":     microToUSD(today.TotalCostMicrodollars),
		"week_usd":      microToUSD(week.TotalCostMicrodollars),
		"month_usd":     microToUSD(month.TotalCostMicrodollars),
		"projected_usd": microToUSD(projected),
		"today_events":  today.EventCount,
		"week_events":   week.EventCount,
		"month_events":  month.EventCount,
	})
}

func (s *Server) handleCostsDaily(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
		return
	}
	if !s.authorizeRequest(r) {
		writeAPIError(w, http.StatusUnauthorized, "UNAUTHORIZED", "unauthorized")
		return
	}
	if s.costStore == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "UNAVAILABLE", "cost tracking not enabled")
		return
	}

	days := 30
	if d := r.URL.Query().Get("days"); d != "" {
		if v, err := strconv.Atoi(d); err == nil && v > 0 && v <= 365 {
			days = v
		}
	}

	now := time.Now().UTC()
	from := now.AddDate(0, 0, -days).Truncate(24 * time.Hour)
	to := now.AddDate(0, 0, 1).Truncate(24 * time.Hour)

	dailyCosts, err := s.costStore.TotalByDateRange(from, to)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to query daily costs")
		return
	}

	type dailyEntry struct {
		Date    string  `json:"date"`
		CostUSD float64 `json:"cost_usd"`
	}

	result := make([]dailyEntry, 0, len(dailyCosts))
	for _, dc := range dailyCosts {
		result = append(result, dailyEntry{
			Date:    dc.Date.Format("2006-01-02"),
			CostUSD: microToUSD(dc.CostMicrodollars),
		})
	}

	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleCostsSessions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
		return
	}
	if !s.authorizeRequest(r) {
		writeAPIError(w, http.StatusUnauthorized, "UNAUTHORIZED", "unauthorized")
		return
	}
	if s.costStore == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "UNAVAILABLE", "cost tracking not enabled")
		return
	}

	sessions, err := s.costStore.TopSessionsByCost(100)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to query session costs")
		return
	}

	type sessionEntry struct {
		SessionID string  `json:"session_id"`
		Title     string  `json:"title"`
		Group     string  `json:"group"`
		CostUSD   float64 `json:"cost_usd"`
		Events    int     `json:"events"`
	}

	result := make([]sessionEntry, 0, len(sessions))
	for _, sc := range sessions {
		result = append(result, sessionEntry{
			SessionID: sc.SessionID,
			Title:     sc.SessionTitle,
			Group:     sc.Group,
			CostUSD:   microToUSD(sc.CostMicrodollars),
			Events:    sc.EventCount,
		})
	}

	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleCostsModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
		return
	}
	if !s.authorizeRequest(r) {
		writeAPIError(w, http.StatusUnauthorized, "UNAUTHORIZED", "unauthorized")
		return
	}
	if s.costStore == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "UNAVAILABLE", "cost tracking not enabled")
		return
	}

	models, err := s.costStore.CostByModel()
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to query model costs")
		return
	}

	result := make(map[string]float64, len(models))
	for model, micro := range models {
		result[model] = microToUSD(micro)
	}

	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleCostsExport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
		return
	}
	if !s.authorizeRequest(r) {
		writeAPIError(w, http.StatusUnauthorized, "UNAUTHORIZED", "unauthorized")
		return
	}
	if s.costStore == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "UNAVAILABLE", "cost tracking not enabled")
		return
	}

	format := r.URL.Query().Get("format")
	if format == "" {
		format = "json"
	}
	if format != "csv" && format != "json" {
		writeAPIError(w, http.StatusBadRequest, "INVALID_REQUEST", "format must be csv or json")
		return
	}

	now := time.Now().UTC()
	var from, to time.Time

	fromStr := r.URL.Query().Get("from")
	toStr := r.URL.Query().Get("to")

	if fromStr != "" && toStr != "" {
		var err error
		from, err = time.Parse("2006-01-02", fromStr)
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid from date format, use YYYY-MM-DD")
			return
		}
		to, err = time.Parse("2006-01-02", toStr)
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid to date format, use YYYY-MM-DD")
			return
		}
		to = to.AddDate(0, 0, 1) // include the end date
	} else {
		days := 30
		if d := r.URL.Query().Get("days"); d != "" {
			if v, err := strconv.Atoi(d); err == nil && v > 0 && v <= 365 {
				days = v
			}
		}
		from = now.AddDate(0, 0, -days).Truncate(24 * time.Hour)
		to = now.AddDate(0, 0, 1).Truncate(24 * time.Hour)
	}

	dailyCosts, err := s.costStore.TotalByDateRange(from, to)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to query daily costs")
		return
	}

	type dailyEntry struct {
		Date    string  `json:"date"`
		CostUSD float64 `json:"cost_usd"`
	}

	entries := make([]dailyEntry, 0, len(dailyCosts))
	for _, dc := range dailyCosts {
		entries = append(entries, dailyEntry{
			Date:    dc.Date.Format("2006-01-02"),
			CostUSD: microToUSD(dc.CostMicrodollars),
		})
	}

	if format == "csv" {
		w.Header().Set("Content-Type", "text/csv")
		w.Header().Set("Content-Disposition", "attachment; filename=costs.csv")
		w.WriteHeader(http.StatusOK)

		cw := csv.NewWriter(w)
		_ = cw.Write([]string{"date", "cost_usd"})
		for _, e := range entries {
			_ = cw.Write([]string{e.Date, fmt.Sprintf("%.6f", e.CostUSD)})
		}
		cw.Flush()
		return
	}

	w.Header().Set("Content-Disposition", "attachment; filename=costs.json")
	writeJSON(w, http.StatusOK, entries)
}

var (
	costStreamPollInterval      = 5 * time.Second
	costStreamHeartbeatInterval = 30 * time.Second
)

func (s *Server) handleCostsStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
		return
	}
	if !s.authorizeRequest(r) {
		writeAPIError(w, http.StatusUnauthorized, "UNAUTHORIZED", "unauthorized")
		return
	}
	if s.costStore == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "UNAVAILABLE", "cost tracking not enabled")
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "stream unavailable")
		return
	}

	summary, err := s.buildCostSummary()
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to load cost data")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	lastToday := summary.TodayMicro
	lastWeek := summary.WeekMicro
	lastMonth := summary.MonthMicro

	if err := writeSSEEvent(w, flusher, "cost_summary", summary); err != nil {
		return
	}

	pollTicker := time.NewTicker(costStreamPollInterval)
	defer pollTicker.Stop()

	heartbeatTicker := time.NewTicker(costStreamHeartbeatInterval)
	defer heartbeatTicker.Stop()

	ctx := r.Context()
	emitIfChanged := func() error {
		next, err := s.buildCostSummary()
		if err != nil {
			logging.ForComponent(logging.CompWeb).Error("cost_stream_refresh_failed",
				slog.String("error", err.Error()))
			return nil
		}
		if next.TodayMicro == lastToday && next.WeekMicro == lastWeek && next.MonthMicro == lastMonth {
			return nil
		}
		if err := writeSSEEvent(w, flusher, "cost_summary", next); err != nil {
			return err
		}
		lastToday = next.TodayMicro
		lastWeek = next.WeekMicro
		lastMonth = next.MonthMicro
		return nil
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-heartbeatTicker.C:
			if err := writeSSEComment(w, flusher, "keepalive"); err != nil {
				return
			}
		case <-pollTicker.C:
			if err := emitIfChanged(); err != nil {
				return
			}
		}
	}
}

type costSummarySSE struct {
	TodayUSD   float64 `json:"today_usd"`
	WeekUSD    float64 `json:"week_usd"`
	MonthUSD   float64 `json:"month_usd"`
	TodayMicro int64   `json:"-"`
	WeekMicro  int64   `json:"-"`
	MonthMicro int64   `json:"-"`
}

func (s *Server) buildCostSummary() (*costSummarySSE, error) {
	today, err := s.costStore.TotalToday()
	if err != nil {
		return nil, err
	}
	week, err := s.costStore.TotalThisWeek()
	if err != nil {
		return nil, err
	}
	month, err := s.costStore.TotalThisMonth()
	if err != nil {
		return nil, err
	}
	return &costSummarySSE{
		TodayUSD:   microToUSD(today.TotalCostMicrodollars),
		WeekUSD:    microToUSD(week.TotalCostMicrodollars),
		MonthUSD:   microToUSD(month.TotalCostMicrodollars),
		TodayMicro: today.TotalCostMicrodollars,
		WeekMicro:  week.TotalCostMicrodollars,
		MonthMicro: month.TotalCostMicrodollars,
	}, nil
}

// MarshalJSON implements custom JSON serialization for costSummarySSE,
// excluding the internal micro fields.
func (c costSummarySSE) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		TodayUSD float64 `json:"today_usd"`
		WeekUSD  float64 `json:"week_usd"`
		MonthUSD float64 `json:"month_usd"`
	}{
		TodayUSD: c.TodayUSD,
		WeekUSD:  c.WeekUSD,
		MonthUSD: c.MonthUSD,
	})
}

func (s *Server) handleCostsGroups(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
		return
	}
	if !s.authorizeRequest(r) {
		writeAPIError(w, http.StatusUnauthorized, "UNAUTHORIZED", "unauthorized")
		return
	}
	if s.costStore == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "UNAVAILABLE", "cost tracking not enabled")
		return
	}

	sessions, err := s.costStore.TopSessionsByCost(1000)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to query costs")
		return
	}

	type groupEntry struct {
		Group    string  `json:"group"`
		CostUSD  float64 `json:"cost_usd"`
		Events   int     `json:"events"`
		Sessions int     `json:"sessions"`
	}

	groups := make(map[string]*groupEntry)
	for _, sc := range sessions {
		g := sc.Group
		if g == "" {
			g = "(ungrouped)"
		}
		entry, ok := groups[g]
		if !ok {
			entry = &groupEntry{Group: g}
			groups[g] = entry
		}
		entry.CostUSD += microToUSD(sc.CostMicrodollars)
		entry.Events += sc.EventCount
		entry.Sessions++
	}

	result := make([]groupEntry, 0, len(groups))
	for _, entry := range groups {
		result = append(result, *entry)
	}

	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleCostsSessionDetail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
		return
	}
	if !s.authorizeRequest(r) {
		writeAPIError(w, http.StatusUnauthorized, "UNAUTHORIZED", "unauthorized")
		return
	}
	if s.costStore == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "UNAVAILABLE", "cost tracking not enabled")
		return
	}

	sessionID := r.URL.Query().Get("id")
	if sessionID == "" {
		writeAPIError(w, http.StatusBadRequest, "INVALID_REQUEST", "missing id parameter")
		return
	}

	summary, err := s.costStore.TotalBySession(sessionID)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to query session")
		return
	}

	// Get daily breakdown for this session
	now := time.Now().UTC()
	from := now.AddDate(0, 0, -30).Truncate(24 * time.Hour)
	to := now.AddDate(0, 0, 1).Truncate(24 * time.Hour)
	daily, err := s.costStore.DailyBySession(sessionID, from, to)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to query session daily costs")
		return
	}

	// Get model breakdown for this session
	models, err := s.costStore.CostByModelForSession(sessionID)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to query session model costs")
		return
	}

	type dailyEntry struct {
		Date    string  `json:"date"`
		CostUSD float64 `json:"cost_usd"`
	}
	dailyResult := make([]dailyEntry, 0, len(daily))
	for _, dc := range daily {
		dailyResult = append(dailyResult, dailyEntry{
			Date:    dc.Date.Format("2006-01-02"),
			CostUSD: microToUSD(dc.CostMicrodollars),
		})
	}

	modelResult := make(map[string]float64, len(models))
	for model, micro := range models {
		modelResult[model] = microToUSD(micro)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"session_id":    sessionID,
		"cost_usd":      microToUSD(summary.TotalCostMicrodollars),
		"input_tokens":  summary.TotalInputTokens,
		"output_tokens": summary.TotalOutputTokens,
		"cache_read":    summary.TotalCacheReadTokens,
		"cache_write":   summary.TotalCacheWriteTokens,
		"events":        summary.EventCount,
		"daily":         dailyResult,
		"models":        modelResult,
	})
}

func (s *Server) handleCostsBatch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
		return
	}
	if !s.authorizeRequest(r) {
		writeAPIError(w, http.StatusUnauthorized, "UNAUTHORIZED", "unauthorized")
		return
	}
	// Graceful: no cost store means empty costs, not an error
	if s.costStore == nil {
		writeJSON(w, http.StatusOK, map[string]any{"costs": map[string]float64{}})
		return
	}

	rawIDs := r.URL.Query().Get("ids")
	if rawIDs == "" {
		writeJSON(w, http.StatusOK, map[string]any{"costs": map[string]float64{}})
		return
	}

	ids := strings.Split(rawIDs, ",")
	const maxBatch = 200
	if len(ids) > maxBatch {
		ids = ids[:maxBatch]
	}

	sessionCosts := make(map[string]float64, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		summary, err := s.costStore.TotalBySession(id)
		if err != nil {
			continue
		}
		sessionCosts[id] = microToUSD(summary.TotalCostMicrodollars)
	}

	writeJSON(w, http.StatusOK, map[string]any{"costs": sessionCosts})
}

func microToUSD(microdollars int64) float64 {
	return float64(microdollars) / 1_000_000
}
