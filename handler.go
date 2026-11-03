package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"google.golang.org/protobuf/types/known/structpb"
)

const (
	pluginVersion = "0.2.1"
	routePrefix   = "/t/"
	maxBody       = 32 << 20
	maxPages      = 10
)

var serverID = jfID("server")

type config struct {
	AccessSecret string
	AdminAPIKey  string
	SiloURL      string
	JellyfinURL  string
}

func (c config) ready() bool { return len(c.AccessSecret) >= 24 && c.AdminAPIKey != "" }

const setupHint = "Tracearr connector is not configured: open Tracearr setup in the plugin menu"

// parseConfig reads the "connection" entry delivered by the host.
func parseConfig(entries []*pluginv1.ConfigEntry) config {
	c := config{SiloURL: "http://127.0.0.1:8080", JellyfinURL: "http://127.0.0.1:8096"}
	for _, e := range entries {
		if e.GetKey() != "connection" || e.GetValue() == nil {
			continue
		}
		m := e.GetValue().AsMap()
		str := func(k string) string {
			v, _ := m[k].(string)
			return strings.TrimSpace(v)
		}
		c.AccessSecret = str("access_secret")
		c.AdminAPIKey = str("admin_api_key")
		if v := str("silo_url"); v != "" {
			c.SiloURL = strings.TrimRight(v, "/")
		}
		if v := str("jellyfin_url"); v != "" {
			c.JellyfinURL = strings.TrimRight(v, "/")
		}
	}
	return c
}

type handler struct {
	pluginv1.UnimplementedHttpRoutesServer
	cfg    atomic.Pointer[config]
	client *http.Client

	mu      sync.Mutex
	posters map[string]string // item id -> Silo poster path, filled from live sessions

	// Setup page state, guarded by mu.
	persisted bool   // the current config came from the host, so it is stored
	saveErr   string // last failure saving a generated secret
	instID    string // this plugin's installation id, looked up from Silo

	// async runs the save of a generated secret; tests run it inline.
	async func(func())
}

func newHandler() *handler {
	h := &handler{
		client:  &http.Client{Timeout: 15 * time.Second},
		posters: map[string]string{},
		async:   func(f func()) { go f() },
	}
	h.cfg.Store(&config{})
	return h
}

func (h *handler) configure(_ context.Context, entries []*pluginv1.ConfigEntry) error {
	c := parseConfig(entries)
	h.cfg.Store(&c)
	h.mu.Lock()
	h.persisted, h.saveErr = true, ""
	h.mu.Unlock()
	return nil
}

func reply(status int, body any) *pluginv1.HandleHTTPResponse {
	b, _ := json.Marshal(body)
	return &pluginv1.HandleHTTPResponse{
		StatusCode: int32(status),
		Headers:    map[string]string{"Content-Type": "application/json"},
		Body:       b,
	}
}

func failure(status int, msg string) *pluginv1.HandleHTTPResponse {
	return reply(status, obj{"error": msg})
}

// Handle serves every /t/<secret>/... request.
func (h *handler) Handle(ctx context.Context, req *pluginv1.HandleHTTPRequest) (*pluginv1.HandleHTTPResponse, error) {
	cfg := *h.cfg.Load()

	if strings.HasPrefix(req.GetPath(), "/setup") {
		return h.setup(ctx, cfg, req)
	}
	if !cfg.ready() {
		return failure(503, setupHint), nil
	}

	rest, ok := strings.CutPrefix(req.GetPath(), routePrefix)
	if !ok {
		return failure(404, "not found"), nil
	}
	secret, rest, _ := strings.Cut(rest, "/")
	if subtle.ConstantTimeCompare([]byte(secret), []byte(cfg.AccessSecret)) != 1 {
		return failure(404, "not found"), nil
	}
	path := "/" + rest
	lower := strings.ToLower(strings.TrimRight(path, "/"))

	if req.GetMethod() != http.MethodGet && req.GetMethod() != http.MethodHead {
		return failure(405, "this connector is read-only"), nil
	}

	switch {
	case lower == "/users/me":
		return h.usersMe(ctx, cfg)
	case lower == "/users":
		return h.users(ctx, cfg)
	case lower == "/sessions":
		return h.sessions(ctx, cfg)
	case lower == "/auth/keys":
		return reply(200, obj{"Items": []any{}, "TotalRecordCount": 0, "StartIndex": 0}), nil
	case lower == "/system/info/public":
		return reply(200, obj{
			"ServerName": "Silo", "Version": "10.10.0", "ProductName": "Jellyfin Server",
			"Id": serverID, "StartupWizardCompleted": true,
		}), nil
	case lower == "/system/info":
		return reply(200, obj{
			"ServerName": "Silo", "Version": "10.10.0", "ProductName": "Jellyfin Server",
			"Id": serverID, "OperatingSystem": "Linux",
		}), nil
	case strings.HasPrefix(lower, "/items/") && strings.Contains(lower, "/images/"):
		return h.image(ctx, cfg, lower)
	case allowedProxy(lower):
		return h.proxy(ctx, cfg, path, req.GetQuery())
	}
	return failure(404, "not found"), nil
}

func allowedProxy(lower string) bool {
	for _, p := range []string{"/library/", "/items", "/genres", "/shows/", "/movies/"} {
		if strings.HasPrefix(lower, p) {
			return true
		}
	}
	return false
}

func (h *handler) usersMe(ctx context.Context, cfg config) (*pluginv1.HandleHTTPResponse, error) {
	return reply(200, mapUser(siloUser{ID: "connector", Username: "Tracearr", Role: "admin", Enabled: true}, true)), nil
}

// siloGet calls Silo's native API with the admin key.
func (h *handler) siloGet(ctx context.Context, cfg config, path string, out any) error {
	r, err := http.NewRequestWithContext(ctx, http.MethodGet, cfg.SiloURL+path, nil)
	if err != nil {
		return err
	}
	r.Header.Set("Authorization", "Bearer "+cfg.AdminAPIKey)
	r.Header.Set("Accept", "application/json")
	resp, err := h.client.Do(r)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return err
	}
	if resp.StatusCode != 200 {
		return fmt.Errorf("silo %s returned %d", path, resp.StatusCode)
	}
	return json.Unmarshal(body, out)
}

type collection[T any] struct {
	Items []T `json:"items"`
	Page  struct {
		NextCursor string `json:"next_cursor"`
		HasMore    bool   `json:"has_more"`
	} `json:"page"`
}

// siloList follows Silo's cursor pagination.
func siloList[T any](ctx context.Context, h *handler, cfg config, path string) ([]T, error) {
	var all []T
	cursor := ""
	for i := 0; i < maxPages; i++ {
		q := "limit=200"
		if cursor != "" {
			q += "&cursor=" + url.QueryEscape(cursor)
		}
		var page collection[T]
		if err := h.siloGet(ctx, cfg, path+"?"+q, &page); err != nil {
			return nil, err
		}
		all = append(all, page.Items...)
		if !page.Page.HasMore || page.Page.NextCursor == "" {
			break
		}
		cursor = page.Page.NextCursor
	}
	return all, nil
}

func (h *handler) users(ctx context.Context, cfg config) (*pluginv1.HandleHTTPResponse, error) {
	list, err := siloList[siloUser](ctx, h, cfg, "/api/v2/admin/users")
	if err != nil {
		return failure(502, err.Error()), nil
	}
	out := make([]obj, 0, len(list))
	for _, u := range list {
		out = append(out, mapUser(u, false))
	}
	return reply(200, out), nil
}

func (h *handler) sessions(ctx context.Context, cfg config) (*pluginv1.HandleHTTPResponse, error) {
	list, err := siloList[siloSession](ctx, h, cfg, "/api/v2/admin/sessions")
	if err != nil {
		return failure(502, err.Error()), nil
	}
	sort.SliceStable(list, func(i, j int) bool { return list[i].SessionID < list[j].SessionID })
	out := make([]obj, 0, len(list))
	posters := map[string]string{}
	for _, s := range list {
		m, itemID, poster := mapSession(s)
		out = append(out, m)
		if poster != "" {
			posters[itemID] = poster
		}
	}
	h.mu.Lock()
	for k, v := range posters {
		h.posters[k] = v
	}
	if len(h.posters) > 500 { // bounded: drop everything, live sessions refill it
		h.posters = posters
	}
	h.mu.Unlock()
	return reply(200, out), nil
}

// image serves artwork for items seen in live sessions.
func (h *handler) image(ctx context.Context, cfg config, lower string) (*pluginv1.HandleHTTPResponse, error) {
	parts := strings.Split(strings.Trim(lower, "/"), "/")
	if len(parts) < 3 {
		return failure(404, "not found"), nil
	}
	h.mu.Lock()
	poster := h.posters[parts[1]]
	h.mu.Unlock()
	if poster == "" || !strings.HasPrefix(poster, "/") || strings.HasPrefix(poster, "//") {
		return failure(404, "no image"), nil
	}
	return h.fetch(ctx, cfg.SiloURL+poster, "Bearer "+cfg.AdminAPIKey)
}

func (h *handler) proxy(ctx context.Context, cfg config, path string, q *structpb.Struct) (*pluginv1.HandleHTTPResponse, error) {
	target := cfg.JellyfinURL + path
	if raw := rawQuery(q); raw != "" {
		target += "?" + raw
	}
	return h.fetch(ctx, target, fmt.Sprintf(`MediaBrowser Token="%s", Client="Tracearr Connector", Device="Silo plugin", DeviceId="tracearr-connector", Version="%s"`, cfg.AdminAPIKey, pluginVersion))
}

func (h *handler) fetch(ctx context.Context, target, auth string) (*pluginv1.HandleHTTPResponse, error) {
	r, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return failure(502, err.Error()), nil
	}
	r.Header.Set("Authorization", auth)
	if strings.HasPrefix(auth, "MediaBrowser") {
		r.Header.Set("X-Emby-Authorization", auth)
	}
	resp, err := h.client.Do(r)
	if err != nil {
		return failure(502, "upstream unreachable"), nil
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return failure(502, "upstream read failed"), nil
	}
	ct := resp.Header.Get("Content-Type")
	if ct == "" {
		ct = "application/octet-stream"
	}
	return &pluginv1.HandleHTTPResponse{
		StatusCode: int32(resp.StatusCode),
		Headers:    map[string]string{"Content-Type": ct},
		Body:       body,
	}, nil
}

// rawQuery rebuilds a query string from the host's struct, which carries each
// parameter as a string or a list of strings.
func rawQuery(q *structpb.Struct) string {
	if q == nil {
		return ""
	}
	v := url.Values{}
	for k, val := range q.AsMap() {
		switch t := val.(type) {
		case string:
			v.Add(k, t)
		case []any:
			for _, e := range t {
				if s, ok := e.(string); ok {
					v.Add(k, s)
				}
			}
		}
	}
	return v.Encode()
}
