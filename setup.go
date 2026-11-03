package main

import (
	"bytes"
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
)

//go:embed setup.html
var setupHTML []byte

const thisPluginID = "community.tracearr-connector"

func header(req *pluginv1.HandleHTTPRequest, name string) string {
	for k, v := range req.GetHeaders() {
		if strings.EqualFold(k, name) {
			return v
		}
	}
	return ""
}

// setup serves the admin-only setup page and its small JSON API. Silo has
// already checked the admin role for these routes; the header check is a
// second guard in case a route's access is ever loosened.
func (h *handler) setup(ctx context.Context, cfg config, req *pluginv1.HandleHTTPRequest) (*pluginv1.HandleHTTPResponse, error) {
	if !strings.EqualFold(header(req, "X-Silo-User-Role"), "admin") {
		return failure(403, "administrators only"), nil
	}
	path := strings.TrimRight(req.GetPath(), "/")
	switch {
	case path == "/setup" && req.GetMethod() == http.MethodGet:
		return &pluginv1.HandleHTTPResponse{
			StatusCode: 200,
			Headers:    map[string]string{"Content-Type": "text/html; charset=utf-8", "Cache-Control": "no-store"},
			Body:       setupHTML,
		}, nil
	case path == "/setup/api/state" && req.GetMethod() == http.MethodGet:
		return h.setupState(ctx, cfg), nil
	case path == "/setup/api/generate" && req.GetMethod() == http.MethodPost:
		// A cross-site form cannot send this content type without a preflight.
		if !strings.HasPrefix(strings.ToLower(header(req, "Content-Type")), "application/json") {
			return failure(415, "JSON required"), nil
		}
		return h.setupGenerate(ctx, cfg), nil
	}
	return failure(404, "not found"), nil
}

func noStore(r *pluginv1.HandleHTTPResponse) *pluginv1.HandleHTTPResponse {
	r.Headers["Cache-Control"] = "no-store"
	return r
}

func (h *handler) setupState(ctx context.Context, cfg config) *pluginv1.HandleHTTPResponse {
	h.mu.Lock()
	persisted, saveErr := h.persisted, h.saveErr
	h.mu.Unlock()
	id, idErr := h.installationID(ctx, cfg)
	st := obj{
		"has_api_key":     cfg.AdminAPIKey != "",
		"secret":          cfg.AccessSecret,
		"persisted":       persisted,
		"save_error":      saveErr,
		"installation_id": id,
	}
	if idErr != "" {
		st["lookup_error"] = idErr
	}
	return noStore(reply(200, st))
}

func newSecret() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// setupGenerate makes a new access secret, uses it at once, and saves it to
// the plugin's stored settings in the background: Silo restarts the plugin
// when settings change, which would cut off a request still waiting on it.
func (h *handler) setupGenerate(ctx context.Context, cfg config) *pluginv1.HandleHTTPResponse {
	if cfg.AdminAPIKey == "" {
		return failure(409, "Enter the Silo admin API key in the plugin settings first.")
	}
	secret, err := newSecret()
	if err != nil {
		return failure(500, "could not generate a secret")
	}
	id, idErr := h.installationID(ctx, cfg)
	if id == "" {
		return failure(502, "Could not find this plugin's installation in Silo: "+idErr)
	}

	next := cfg
	next.AccessSecret = secret
	h.cfg.Store(&next)
	h.mu.Lock()
	h.persisted, h.saveErr = false, ""
	h.mu.Unlock()

	h.async(func() {
		fields := obj{"access_secret": secret}
		if u := next.desiredURL(id); u != "" {
			fields["tracearr_url"] = u
		}
		if err := h.saveFields(context.Background(), next, id, fields); err != nil {
			log.Printf("saving access secret: %v", err)
			h.mu.Lock()
			h.saveErr = err.Error()
			h.mu.Unlock()
		}
	})
	return noStore(reply(200, obj{"secret": secret, "installation_id": id}))
}

// saveFields writes fields into this plugin's stored "connection" entry.
// Silo merges it over the stored fields and restarts the plugin, so a dropped
// connection at the end is expected and not treated as a failure.
func (h *handler) saveFields(ctx context.Context, cfg config, id string, fields obj) error {
	body, _ := json.Marshal(obj{"key": "connection", "value": fields})
	r, err := http.NewRequestWithContext(ctx, http.MethodPut, cfg.SiloURL+"/api/v2/admin/plugins/installations/"+id+"/config", bytes.NewReader(body))
	if err != nil {
		return err
	}
	r.Header.Set("Authorization", "Bearer "+cfg.AdminAPIKey)
	r.Header.Set("Content-Type", "application/json")
	resp, err := h.client.Do(r)
	if err != nil {
		return nil // plugin stopped mid-request after Silo stored the settings
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("Silo refused to save the settings (HTTP %d); copy the secret into the plugin settings by hand", resp.StatusCode)
	}
	return nil
}

// installationID finds this plugin's installation id through Silo's admin API.
func (h *handler) installationID(ctx context.Context, cfg config) (string, string) {
	h.mu.Lock()
	cached := h.instID
	h.mu.Unlock()
	if cached != "" {
		return cached, ""
	}
	if cfg.AdminAPIKey == "" {
		return "", "no admin API key set"
	}
	type row struct {
		ID       json.RawMessage `json:"id"`
		PluginID string          `json:"plugin_id"`
	}
	rows, err := siloList[row](ctx, h, cfg, "/api/v2/admin/plugins/installations")
	if err != nil {
		return "", err.Error()
	}
	for _, r := range rows {
		if r.PluginID == thisPluginID {
			id := strings.Trim(string(r.ID), `"`)
			h.mu.Lock()
			h.instID = id
			h.mu.Unlock()
			return id, ""
		}
	}
	return "", "plugin not found in Silo's installation list"
}
