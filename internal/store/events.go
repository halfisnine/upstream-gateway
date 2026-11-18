package store

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/halfisnine/upstream-gateway/internal/interceptlog"
)

func (s *Store) WriteEvents(ctx context.Context, events []interceptlog.Event) error {
	if len(events) == 0 {
		return nil
	}
	raw, e := json.Marshal(events)
	if e != nil {
		return e
	}
	_, e = s.LogDB.ExecContext(ctx, `INSERT INTO gateway_interceptions(id,occurred_at,request_id,channel,rule_id,node,actual_status,event)
 SELECT item->>'id',(item->>'time')::timestamptz,item->>'request_id',item->>'channel',item->>'rule_id',item->>'node',(item->>'actual_status')::integer,item
 FROM jsonb_array_elements($1::jsonb) item ON CONFLICT(id) DO NOTHING`, string(raw))
	return e
}
func (s *Store) PruneEvents(ctx context.Context) error {
	tx, e := s.LogDB.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var locked bool
	if e = tx.QueryRowContext(ctx, `SELECT pg_try_advisory_xact_lock(726318013)`).Scan(&locked); e != nil || !locked {
		return e
	}
	// All nodes share a maintenance lease, including the minute after commit.
	result, e := tx.ExecContext(ctx, `UPDATE gateway_log_maintenance SET last_run=now() WHERE id=1 AND last_run<now()-interval '1 minute'`)
	if e != nil {
		return e
	}
	n, e := result.RowsAffected()
	if e != nil || n == 0 {
		return e
	}
	// Use the ordering index to find a soft count boundary without COUNT(*).
	_, e = tx.ExecContext(ctx, `DELETE FROM gateway_interceptions WHERE id IN (
 SELECT id FROM gateway_interceptions WHERE occurred_at<now()-interval '3 days'
 OR (occurred_at,id) <= (SELECT occurred_at,id FROM gateway_interceptions ORDER BY occurred_at DESC,id DESC OFFSET 50000 LIMIT 1)
 ORDER BY occurred_at,id LIMIT 10000)`)
	if e != nil {
		return e
	}
	return tx.Commit()
}

type EventQuery struct {
	Since, Until                   time.Time
	Channel, Rule, Node, RequestID string
	Status                         int
	Cursor                         string
	Limit                          int
}
type EventPage struct {
	Events []interceptlog.Event `json:"events"`
	Next   string               `json:"next_cursor"`
}
type eventCursor struct {
	Time time.Time `json:"time"`
	ID   string    `json:"id"`
}

func (s *Store) Events(ctx context.Context, q EventQuery) (EventPage, error) {
	result := EventPage{Events: []interceptlog.Event{}}
	if q.Limit < 1 || q.Limit > 100 || q.Since.IsZero() || q.Until.Before(q.Since) || q.Until.Sub(q.Since) > 72*time.Hour {
		return result, errors.New("invalid_log_query")
	}
	var cursor eventCursor
	if q.Cursor != "" {
		raw, e := base64.RawURLEncoding.DecodeString(q.Cursor)
		if e != nil || len(raw) > 256 || json.Unmarshal(raw, &cursor) != nil || cursor.Time.IsZero() || len(cursor.ID) > 64 {
			return result, errors.New("invalid_log_cursor")
		}
	}
	args := []any{q.Since, q.Until}
	query := `SELECT event - 'original_cipher',occurred_at,id FROM gateway_interceptions WHERE occurred_at >= $1 AND occurred_at <= $2`
	if q.Cursor != "" {
		args = append(args, cursor.Time, cursor.ID)
		query += ` AND (occurred_at,id)<($3,$4)`
	}
	for _, f := range []struct{ column, value string }{{"channel", q.Channel}, {"rule_id", q.Rule}, {"node", q.Node}, {"request_id", q.RequestID}} {
		if f.value != "" {
			args = append(args, f.value)
			query += " AND " + f.column + "=$" + strconv.Itoa(len(args))
		}
	}
	if q.Status != 0 {
		args = append(args, q.Status)
		query += " AND actual_status=$" + strconv.Itoa(len(args))
	}
	args = append(args, q.Limit+1)
	query += " ORDER BY occurred_at DESC,id DESC LIMIT $" + strconv.Itoa(len(args))
	rows, e := s.LogDB.QueryContext(ctx, query, args...)
	if e != nil {
		return result, e
	}
	defer rows.Close()
	for rows.Next() {
		var raw []byte
		var event interceptlog.Event
		var occurred time.Time
		var id string
		if e = rows.Scan(&raw, &occurred, &id); e != nil {
			return result, e
		}
		if e = json.Unmarshal(raw, &event); e != nil {
			return result, e
		}
		event.Time = occurred
		event.ID = id
		result.Events = append(result.Events, event)
	}
	if e = rows.Err(); e != nil {
		return result, e
	}
	if len(result.Events) > q.Limit {
		result.Events = result.Events[:q.Limit]
		last := result.Events[q.Limit-1]
		raw, _ := json.Marshal(eventCursor{last.Time, last.ID})
		result.Next = base64.RawURLEncoding.EncodeToString(raw)
	}
	return result, nil
}

// EventOriginal is separate from list queries: ciphertext is never included in
// list responses and plaintext is decrypted only for an authenticated detail call.
func (s *Store) EventOriginal(ctx context.Context, id string) (interceptlog.Event, error) {
	var event interceptlog.Event
	var raw []byte
	err := s.LogDB.QueryRowContext(ctx, `SELECT event FROM gateway_interceptions WHERE id=$1`, id).Scan(&raw)
	if err != nil {
		return event, err
	}
	err = json.Unmarshal(raw, &event)
	event.ID = id
	return event, err
}
