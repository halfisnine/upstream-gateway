package console

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/halfisnine/upstream-gateway/internal/config"
	"github.com/halfisnine/upstream-gateway/internal/gateway"
	"github.com/halfisnine/upstream-gateway/internal/store"
)

//go:embed static/*
var assets embed.FS

type Console struct {
	Store *store.Store
	Key   []byte
	Token string
	Node  *gateway.Node
}

func send(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func failure(w http.ResponseWriter, status int, code string) {
	send(w, status, map[string]string{"error": code})
}
func (c *Console) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { send(w, 200, map[string]string{"status": "ok"}) })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		status := 503
		if c.Node.Ready() {
			status = 200
		}
		send(w, status, map[string]any{"ready": c.Node.Ready()})
	})
	api := http.NewServeMux()
	api.HandleFunc("GET /api/status", c.status)
	api.HandleFunc("GET /api/error-presets", c.presets)
	api.HandleFunc("POST /api/error-test", c.testRule)
	api.HandleFunc("GET /api/interceptions", c.interceptions)
	api.HandleFunc("GET /api/interceptions/{id}/original", c.originalError)
	api.HandleFunc("GET /api/versions", c.versions)
	api.HandleFunc("GET /api/versions/{id}", c.version)
	api.HandleFunc("POST /api/publish", c.publish)
	api.HandleFunc("POST /api/rollback", c.rollback)
	mux.Handle("/api/", c.authorize(api))
	sub, _ := fs.Sub(assets, "static")
	files := http.FileServer(http.FS(sub))
	mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			failure(w, 405, "method_not_allowed")
			return
		}
		if r.URL.Path != "/" && r.URL.Path != "/app.js" && r.URL.Path != "/style.css" && r.URL.Path != "/rules.js" && r.URL.Path != "/rules.css" {
			failure(w, 404, "not_found")
			return
		}
		files.ServeHTTP(w, r)
	}))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		mux.ServeHTTP(w, r)
	})
}
func (c *Console) authorize(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c.Token == "" {
			failure(w, 404, "console_disabled")
			return
		}
		if len(r.Header.Values("Authorization")) != 1 {
			failure(w, 401, "unauthorized")
			return
		}
		actual := sha256.Sum256([]byte(r.Header.Get("Authorization")))
		expected := sha256.Sum256([]byte("Bearer " + c.Token))
		if subtle.ConstantTimeCompare(actual[:], expected[:]) != 1 {
			failure(w, 401, "unauthorized")
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" {
			u, e := url.Parse(origin)
			if e != nil || u.Host != r.Host || (u.Scheme != "http" && u.Scheme != "https") {
				failure(w, 403, "cross_origin_rejected")
				return
			}
		}
		if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
			failure(w, 403, "cross_origin_rejected")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
func read(w http.ResponseWriter, r *http.Request, dst any) bool {
	if r.Header.Get("Content-Type") != "application/json" {
		failure(w, 415, "json_required")
		return false
	}
	data, e := io.ReadAll(http.MaxBytesReader(w, r.Body, 2<<20))
	if e != nil {
		failure(w, 413, "request_too_large")
		return false
	}
	if e = config.Decode(data, dst); e != nil {
		failure(w, 400, e.Error())
		return false
	}
	return true
}
func (c *Console) status(w http.ResponseWriter, r *http.Request) {
	latest, e := c.Store.Latest(r.Context())
	if e != nil {
		failure(w, 503, "database_unavailable")
		return
	}
	nodes, e := c.Store.Nodes(r.Context())
	if e != nil {
		failure(w, 503, "database_unavailable")
		return
	}
	send(w, 200, map[string]any{"latest_version": latest, "nodes": nodes, "local": c.Node.Status()})
}
func (c *Console) versions(w http.ResponseWriter, r *http.Request) {
	before, _ := strconv.ParseInt(r.URL.Query().Get("before"), 10, 64)
	v, e := c.Store.Versions(r.Context(), before)
	if e != nil {
		failure(w, 503, "database_unavailable")
		return
	}
	send(w, 200, v)
}
func (c *Console) version(w http.ResponseWriter, r *http.Request) {
	id, e := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if e != nil || id < 0 {
		failure(w, 400, "invalid_version")
		return
	}
	if id == 0 {
		send(w, 200, map[string]any{"version": store.Version{}, "config": config.Empty()})
		return
	}
	v, e := c.Store.Version(r.Context(), id)
	if e != nil {
		failure(w, 404, "version_unavailable")
		return
	}
	cfg, sourceSchema, e := config.OpenForEdit(v.Data, v.Digest, c.Key)
	if e != nil {
		failure(w, 422, "snapshot_unreadable")
		return
	}
	send(w, 200, map[string]any{"version": v, "config": cfg, "source_schema_version": sourceSchema, "requires_republish": sourceSchema != config.Schema})
}

type publishRequest struct {
	Base   int64         `json:"base_version"`
	Note   string        `json:"note"`
	Config config.Config `json:"config"`
}

func (c *Console) publish(w http.ResponseWriter, r *http.Request) {
	var req publishRequest
	if !read(w, r, &req) {
		return
	}
	if req.Base < 0 || len(req.Note) > 500 {
		failure(w, 400, "invalid_publication")
		return
	}
	if e := config.Validate(req.Config); e != nil {
		failure(w, 422, e.Error())
		return
	}
	c.save(w, r, req.Base, req.Config, req.Note)
}
func (c *Console) save(w http.ResponseWriter, r *http.Request, base int64, cfg config.Config, note string) {
	data, digest, e := config.Seal(cfg, c.Key)
	if e != nil {
		failure(w, 500, "encryption_failed")
		return
	}
	v, e := c.Store.Publish(r.Context(), base, data, digest, strings.TrimSpace(note))
	if errors.Is(e, store.ErrConflict) {
		failure(w, 409, "version_conflict")
		return
	}
	if e != nil {
		failure(w, 503, "publication_failed")
		return
	}
	send(w, 201, v)
}
func (c *Console) rollback(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Base   int64  `json:"base_version"`
		Source int64  `json:"source_version"`
		Note   string `json:"note"`
	}
	if !read(w, r, &req) {
		return
	}
	if req.Base < 0 || req.Source <= 0 || len(req.Note) > 400 {
		failure(w, 400, "invalid_publication")
		return
	}
	v, e := c.Store.Version(r.Context(), req.Source)
	if e != nil {
		failure(w, 404, "version_unavailable")
		return
	}
	cfg, _, e := config.OpenForEdit(v.Data, v.Digest, c.Key)
	if e != nil {
		failure(w, 422, "snapshot_unreadable")
		return
	}
	c.save(w, r, req.Base, cfg, "Restore V"+strconv.FormatInt(req.Source, 10)+": "+req.Note)
}
