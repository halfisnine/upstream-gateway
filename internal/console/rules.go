package console

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/halfisnine/upstream-gateway/internal/config"
	"github.com/halfisnine/upstream-gateway/internal/errorrules"
	"github.com/halfisnine/upstream-gateway/internal/store"
)

func (c *Console) presets(w http.ResponseWriter, r *http.Request) { send(w, 200, errorrules.Presets()) }
func (c *Console) testRule(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Config config.Config     `json:"config"`
		Sample errorrules.Sample `json:"sample"`
	}
	if !read(w, r, &req) {
		return
	}
	if e := config.Validate(req.Config); e != nil {
		failure(w, 422, e.Error())
		return
	}
	s := req.Sample
	if !config.IsProtocol(s.Protocol) || !slices.ContainsFunc(req.Config.Channels, func(ch config.Channel) bool { _, ok := ch.Protocols[s.Protocol]; return ch.ID == s.Channel && ok }) || !slices.Contains([]string{"http", "json", "sse", "normal"}, s.Source) || s.Status < 100 || s.Status > 599 || len(s.Error.Message) > 64<<10 || len(s.Error.Code) > 1024 || len(s.Error.Type) > 1024 || (s.Source == "json" && s.Status != 200) || (s.Source == "http" && s.Status == 200) || (s.Committed && (s.Source != "sse" || s.Status != 200)) {
		failure(w, 422, "invalid_error_sample")
		return
	}
	if s.Source == "normal" {
		send(w, 200, map[string]any{"intercepted": false, "reason": "normal_response"})
		return
	}
	if s.Status == 200 && !req.Config.InspectEmbeddedErrors {
		send(w, 200, map[string]any{"intercepted": false, "reason": "inspection_disabled"})
		return
	}
	d := errorrules.Match(req.Config.ErrorRules, s)
	actual := d.Status
	if s.Committed {
		actual = 200
	}
	send(w, 200, map[string]any{"intercepted": true, "decision": d, "actual_status": actual, "status_override_skipped": s.Committed})
}
func (c *Console) interceptions(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	now := time.Now().UTC()
	query := store.EventQuery{Since: now.Add(-24 * time.Hour), Until: now, Limit: 50, Channel: q.Get("channel"), Rule: q.Get("rule"), Node: q.Get("node"), RequestID: q.Get("request_id"), Cursor: q.Get("cursor")}
	for key, dst := range map[string]*time.Time{"since": &query.Since, "until": &query.Until} {
		if s := q.Get(key); s != "" {
			v, e := time.Parse(time.RFC3339Nano, s)
			if e != nil {
				failure(w, 400, "invalid_log_query")
				return
			}
			*dst = v
		}
	}
	for key, dst := range map[string]*int{"limit": &query.Limit, "status": &query.Status} {
		if s := q.Get(key); s != "" {
			v, e := strconv.Atoi(s)
			if e != nil {
				failure(w, 400, "invalid_log_query")
				return
			}
			*dst = v
		}
	}
	if query.Limit < 1 || query.Limit > 100 || query.Since.IsZero() || query.Until.Before(query.Since) || query.Until.Sub(query.Since) > 72*time.Hour || query.Until.After(now.Add(time.Minute)) || len(query.Cursor) > 512 || len(query.Channel) > 64 || len(query.Rule) > 64 || len(query.Node) > 64 || len(query.RequestID) > 64 || (query.Status != 0 && (query.Status < 100 || query.Status > 599)) {
		failure(w, 400, "invalid_log_query")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	page, e := c.Store.Events(ctx, query)
	if e != nil {
		if e.Error() == "invalid_log_cursor" {
			failure(w, 400, "invalid_log_cursor")
		} else {
			failure(w, 503, "log_database_unavailable")
		}
		return
	}
	send(w, 200, page)
}

func (c *Console) originalError(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !config.ValidID(id) {
		failure(w, 400, "invalid_event_id")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	event, err := c.Store.EventOriginal(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		failure(w, 404, "interception_expired_or_missing")
		return
	}
	if err != nil {
		failure(w, 503, "log_database_unavailable")
		return
	}
	original, err := event.OpenOriginal(c.Key)
	if err != nil {
		failure(w, 422, "original_decryption_failed")
		return
	}
	state := event.OriginalState
	if state == "" {
		state = "legacy"
	}
	send(w, 200, map[string]any{"original": original, "state": state, "truncated": event.OriginalTruncated, "redacted": event.OriginalRedacted, "limit_bytes": 2048})
}
