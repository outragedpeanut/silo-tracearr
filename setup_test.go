package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"google.golang.org/protobuf/types/known/structpb"
)

type putCapture struct {
	mu   sync.Mutex
	path string
	auth string
	body string
	code int
}

func setupSilo(t *testing.T, c *putCapture) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/api/v2/admin/plugins/installations":
			w.Write([]byte(`{"items":[{"id":"1","plugin_id":"other"},{"id":"7","plugin_id":"community.tracearr-connector"}],"page":{"has_more":false}}`))
		case r.Method == "PUT":
			b, _ := io.ReadAll(r.Body)
			c.mu.Lock()
			c.path, c.auth, c.body = r.URL.Path, r.Header.Get("Authorization"), string(b)
			code := c.code
			c.mu.Unlock()
			if code == 0 {
				code = 204
			}
			w.WriteHeader(code)
		default:
			http.NotFound(w, r)
		}
	}))
}

func adminReq(method, path string, json bool) *pluginv1.HandleHTTPRequest {
	h := map[string]string{"X-Silo-User-Role": "admin"}
	if json {
		h["Content-Type"] = "application/json"
	}
	return &pluginv1.HandleHTTPRequest{Method: method, Path: path, Headers: h}
}

func keyOnlyHandler(t *testing.T, silo string) *handler {
	t.Helper()
	h := newHandler()
	h.async = func(f func()) {} // no automatic setup while loading the config
	v, _ := structpb.NewStruct(map[string]any{"admin_api_key": "sa_key", "silo_url": silo})
	h.configure(context.Background(), []*pluginv1.ConfigEntry{{Key: "connection", Value: v}})
	h.async = func(f func()) { f() }
	h.sleep = func(time.Duration) {}
	return h
}

func TestSetupRequiresAdmin(t *testing.T) {
	h := newHandler()
	for _, p := range []string{"/setup", "/setup/api/state"} {
		resp, _ := h.Handle(context.Background(), &pluginv1.HandleHTTPRequest{Method: "GET", Path: p})
		if resp.StatusCode != 403 {
			t.Errorf("%s: %d, want 403", p, resp.StatusCode)
		}
		resp, _ = h.Handle(context.Background(), &pluginv1.HandleHTTPRequest{Method: "GET", Path: p, Headers: map[string]string{"X-Silo-User-Role": "user"}})
		if resp.StatusCode != 403 {
			t.Errorf("%s as user: %d, want 403", p, resp.StatusCode)
		}
	}
}

func TestSetupPageServed(t *testing.T) {
	h := newHandler()
	resp, _ := h.Handle(context.Background(), adminReq("GET", "/setup", false))
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Headers["Content-Type"], "text/html") || !strings.Contains(string(resp.Body), "Tracearr setup") {
		t.Fatalf("page: %d %v", resp.StatusCode, resp.Headers)
	}
	// the page must not carry a secret and must hide the URL by default
	if !strings.Contains(string(resp.Body), `type="password" readonly`) {
		t.Errorf("URL field must default to hidden")
	}
}

func TestGenerateSavesSecret(t *testing.T) {
	var c putCapture
	silo := setupSilo(t, &c)
	defer silo.Close()
	h := keyOnlyHandler(t, silo.URL)

	// before generating: key present, no secret, public API says setup first
	var st obj
	resp, _ := h.Handle(context.Background(), adminReq("GET", "/setup/api/state", false))
	json.Unmarshal(resp.Body, &st)
	if st["has_api_key"] != true || st["secret"] != "" || st["installation_id"] != "7" {
		t.Fatalf("state: %v", st)
	}
	if get(t, h, "/t/anything/Sessions").StatusCode != 503 {
		t.Errorf("unconfigured API should 503")
	}

	resp, _ = h.Handle(context.Background(), adminReq("POST", "/setup/api/generate", true))
	var g obj
	json.Unmarshal(resp.Body, &g)
	secret, _ := g["secret"].(string)
	if resp.StatusCode != 200 || len(secret) != 48 || g["installation_id"] != "7" {
		t.Fatalf("generate: %d %v", resp.StatusCode, g)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.path != "/api/v2/admin/plugins/installations/7/config" || c.auth != "Bearer sa_key" {
		t.Errorf("save request: %s %s", c.path, c.auth)
	}
	if !strings.Contains(c.body, `"key":"connection"`) || !strings.Contains(c.body, secret) {
		t.Errorf("save body: %s", c.body)
	}
	// the new secret works immediately on the public route
	if got := get(t, h, "/t/"+secret+"/Users/Me").StatusCode; got != 200 {
		t.Errorf("new secret rejected: %d", got)
	}
	// the host re-delivering config marks it persisted
	resp, _ = h.Handle(context.Background(), adminReq("GET", "/setup/api/state", false))
	json.Unmarshal(resp.Body, &st)
	if st["persisted"] != false {
		t.Errorf("should not be persisted before restart: %v", st)
	}
	v, _ := structpb.NewStruct(map[string]any{"admin_api_key": "sa_key", "access_secret": secret, "silo_url": silo.URL})
	h.configure(context.Background(), []*pluginv1.ConfigEntry{{Key: "connection", Value: v}})
	resp, _ = h.Handle(context.Background(), adminReq("GET", "/setup/api/state", false))
	json.Unmarshal(resp.Body, &st)
	if st["persisted"] != true || st["secret"] != secret {
		t.Errorf("after configure: %v", st)
	}
}

func TestGenerateReportsSaveFailure(t *testing.T) {
	c := putCapture{code: 403}
	silo := setupSilo(t, &c)
	defer silo.Close()
	h := keyOnlyHandler(t, silo.URL)
	h.Handle(context.Background(), adminReq("POST", "/setup/api/generate", true))
	var st obj
	resp, _ := h.Handle(context.Background(), adminReq("GET", "/setup/api/state", false))
	json.Unmarshal(resp.Body, &st)
	if !strings.Contains(st["save_error"].(string), "403") || st["persisted"] != false {
		t.Fatalf("state: %v", st)
	}
}

func TestGenerateGuards(t *testing.T) {
	var c putCapture
	silo := setupSilo(t, &c)
	defer silo.Close()
	h := keyOnlyHandler(t, silo.URL)
	if r, _ := h.Handle(context.Background(), adminReq("POST", "/setup/api/generate", false)); r.StatusCode != 415 {
		t.Errorf("non-JSON generate: %d, want 415", r.StatusCode)
	}
	if r, _ := h.Handle(context.Background(), adminReq("GET", "/setup/api/generate", true)); r.StatusCode != 404 {
		t.Errorf("GET generate: %d, want 404", r.StatusCode)
	}
	noKey := newHandler()
	if r, _ := noKey.Handle(context.Background(), adminReq("POST", "/setup/api/generate", true)); r.StatusCode != 409 {
		t.Errorf("no key: %d, want 409", r.StatusCode)
	}
}

func configureWith(h *handler, m map[string]any) {
	v, _ := structpb.NewStruct(m)
	h.configure(context.Background(), []*pluginv1.ConfigEntry{{Key: "connection", Value: v}})
}

func inlineHandler() *handler {
	h := newHandler()
	h.async = func(f func()) { f() }
	h.sleep = func(time.Duration) {}
	return h
}

func TestAutoGeneratesSecretAndURL(t *testing.T) {
	var c putCapture
	silo := setupSilo(t, &c)
	defer silo.Close()
	h := inlineHandler()
	configureWith(h, map[string]any{"admin_api_key": "sa_key", "silo_url": silo.URL, "public_address": "http://192.168.1.10:8090/"})

	c.mu.Lock()
	body := c.body
	c.mu.Unlock()
	var in struct {
		Value struct {
			Secret string `json:"access_secret"`
			URL    string `json:"tracearr_url"`
		} `json:"value"`
	}
	if err := json.Unmarshal([]byte(body), &in); err != nil || len(in.Value.Secret) != 48 {
		t.Fatalf("save body: %s (%v)", body, err)
	}
	if want := "http://192.168.1.10:8090/api/v1/plugins/7/t/" + in.Value.Secret; in.Value.URL != want {
		t.Errorf("url %q, want %q", in.Value.URL, want)
	}
	if get(t, h, "/t/"+in.Value.Secret+"/Users/Me").StatusCode != 200 {
		t.Errorf("new secret not usable at once")
	}

	// the restart delivers the saved values: nothing more to save
	c.mu.Lock()
	c.body = ""
	c.mu.Unlock()
	configureWith(h, map[string]any{"admin_api_key": "sa_key", "silo_url": silo.URL, "public_address": "http://192.168.1.10:8090", "access_secret": in.Value.Secret, "tracearr_url": in.Value.URL})
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.body != "" {
		t.Errorf("settings already complete but saved again: %s", c.body)
	}
}

func TestURLRefreshedWhenAddressChanges(t *testing.T) {
	var c putCapture
	silo := setupSilo(t, &c)
	defer silo.Close()
	h := inlineHandler()
	sec := strings.Repeat("ab", 24)
	configureWith(h, map[string]any{"admin_api_key": "sa_key", "silo_url": silo.URL, "public_address": "http://10.0.0.5:8090",
		"access_secret": sec, "tracearr_url": "http://old:1/api/v1/plugins/7/t/" + sec})
	c.mu.Lock()
	defer c.mu.Unlock()
	if !strings.Contains(c.body, "http://10.0.0.5:8090/api/v1/plugins/7/t/"+sec) || strings.Contains(c.body, `"access_secret"`) {
		t.Fatalf("expected only the URL to be updated: %s", c.body)
	}
}

func TestClearedSecretIsRegenerated(t *testing.T) {
	var c putCapture
	silo := setupSilo(t, &c)
	defer silo.Close()
	h := inlineHandler()
	configureWith(h, map[string]any{"admin_api_key": "sa_key", "silo_url": silo.URL, "public_address": "http://10.0.0.5:8090",
		"tracearr_url": "http://10.0.0.5:8090/api/v1/plugins/7/t/oldoldoldoldoldoldoldold"})
	c.mu.Lock()
	defer c.mu.Unlock()
	if !strings.Contains(c.body, `"access_secret"`) || !strings.Contains(c.body, `"tracearr_url"`) {
		t.Fatalf("new secret and URL expected: %s", c.body)
	}
}

func TestTracearrBase(t *testing.T) {
	cases := []struct{ pub, silo, want string }{
		{"http://a:1/", "http://127.0.0.1:8080", "http://a:1"},
		{"", "http://127.0.0.1:8080", ""},
		{"", "http://localhost:8080", ""},
		{"", "http://192.168.3.131:8090", "http://192.168.3.131:8090"},
	}
	for _, c := range cases {
		got := config{PublicAddr: strings.TrimRight(c.pub, "/"), SiloURL: c.silo}.tracearrBase()
		if got != c.want {
			t.Errorf("%+v: %q, want %q", c, got, c.want)
		}
	}
}

func TestReconcileRetriesThenReportsFailure(t *testing.T) {
	h := inlineHandler()
	configureWith(h, map[string]any{"admin_api_key": "sa_key", "silo_url": "http://127.0.0.1:1"})
	var st obj
	resp, _ := h.Handle(context.Background(), adminReq("GET", "/setup/api/state", false))
	json.Unmarshal(resp.Body, &st)
	if st["save_error"] == "" {
		t.Errorf("failure should be reported: %v", st)
	}
}
