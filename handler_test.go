package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"google.golang.org/protobuf/types/known/structpb"
)

const testSecret = "0123456789abcdef0123456789abcdef"

func fakeSilo(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sa_key" {
			http.Error(w, "no", 401)
			return
		}
		switch r.URL.Path {
		case "/api/v2/admin/users":
			w.Write([]byte(`{"items":[{"id":"1","username":"dean","role":"admin","enabled":true},{"id":"2","username":"mum","role":"user","enabled":false}],"page":{"has_more":false}}`))
		case "/api/v2/admin/sessions":
			if r.URL.Query().Get("cursor") == "" {
				w.Write([]byte(`{"items":[{"session_id":"s1","user_id":"2","username":"mum","media_file_id":"9","media_title":"Show","media_type":"episode","series_name":"Show","episode_name":"Pilot","season_number":1,"episode_number":2,"poster_url":"/img/p.jpg","effective_play_method":"transcode","file_duration":3600,"position_seconds":60,"is_paused":true,"client_ip":"203.0.113.7","client_name":"Silo TV","device_id":"dev1","video_decision":"transcode","audio_decision":"copy","stream_bitrate_kbps":4000}],"page":{"has_more":true,"next_cursor":"c2"}}`))
				return
			}
			w.Write([]byte(`{"items":[{"session_id":"s2","user_id":"1","username":"dean","media_file_id":"10","media_title":"Film","media_type":"movie","effective_play_method":"direct","client_ip":"198.51.100.1"}],"page":{"has_more":false}}`))
		case "/img/p.jpg":
			w.Header().Set("Content-Type", "image/jpeg")
			w.Write([]byte("JPEG"))
		default:
			http.NotFound(w, r)
		}
	}))
}

func newTestHandler(t *testing.T, silo, jelly string) *handler {
	t.Helper()
	h := newHandler()
	v, _ := structpb.NewStruct(map[string]any{
		"access_secret": testSecret, "admin_api_key": "sa_key", "silo_url": silo, "jellyfin_url": jelly,
	})
	if err := h.configure(context.Background(), []*pluginv1.ConfigEntry{{Key: "connection", Value: v}}); err != nil {
		t.Fatal(err)
	}
	return h
}

func get(t *testing.T, h *handler, path string) *pluginv1.HandleHTTPResponse {
	t.Helper()
	resp, err := h.Handle(context.Background(), &pluginv1.HandleHTTPRequest{Method: "GET", Path: path})
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestSecretRequired(t *testing.T) {
	h := newTestHandler(t, "http://x", "http://x")
	for _, p := range []string{"/t/wrong/Users/Me", "/t/", "/other"} {
		if got := get(t, h, p).StatusCode; got != 404 {
			t.Errorf("%s: status %d, want 404", p, got)
		}
	}
}

func TestUnconfigured(t *testing.T) {
	h := newHandler()
	if got := get(t, h, "/t/"+testSecret+"/Users/Me").StatusCode; got != 503 {
		t.Fatalf("status %d, want 503", got)
	}
}

func TestShortSecretRejected(t *testing.T) {
	h := newHandler()
	v, _ := structpb.NewStruct(map[string]any{"access_secret": "short", "admin_api_key": "k"})
	h.configure(context.Background(), []*pluginv1.ConfigEntry{{Key: "connection", Value: v}})
	if got := get(t, h, "/t/short/Users/Me").StatusCode; got != 503 {
		t.Fatalf("status %d, want 503", got)
	}
}

func TestReadOnly(t *testing.T) {
	h := newTestHandler(t, "http://x", "http://x")
	resp, _ := h.Handle(context.Background(), &pluginv1.HandleHTTPRequest{Method: "POST", Path: "/t/" + testSecret + "/Sessions"})
	if resp.StatusCode != 405 {
		t.Fatalf("status %d, want 405", resp.StatusCode)
	}
}

func TestUsersMeIsAdmin(t *testing.T) {
	h := newTestHandler(t, "http://x", "http://x")
	var u obj
	json.Unmarshal(get(t, h, "/t/"+testSecret+"/Users/Me").Body, &u)
	if u["Policy"].(obj)["IsAdministrator"] != true {
		t.Fatalf("not admin: %v", u)
	}
}

func TestUsers(t *testing.T) {
	silo := fakeSilo(t)
	defer silo.Close()
	h := newTestHandler(t, silo.URL, "http://x")
	var us []obj
	json.Unmarshal(get(t, h, "/t/"+testSecret+"/Users").Body, &us)
	if len(us) != 2 {
		t.Fatalf("got %d users", len(us))
	}
	if us[0]["Policy"].(obj)["IsAdministrator"] != true || us[1]["Policy"].(obj)["IsAdministrator"] != false {
		t.Errorf("admin flags wrong: %v", us)
	}
	if us[1]["Policy"].(obj)["IsDisabled"] != true {
		t.Errorf("disabled flag wrong")
	}
}

func TestSessionsPaginationAndMapping(t *testing.T) {
	silo := fakeSilo(t)
	defer silo.Close()
	h := newTestHandler(t, silo.URL, "http://x")
	var ss []obj
	json.Unmarshal(get(t, h, "/t/"+testSecret+"/Sessions").Body, &ss)
	if len(ss) != 2 {
		t.Fatalf("got %d sessions, want 2 (pagination)", len(ss))
	}
	s1 := ss[0]
	if s1["RemoteEndPoint"] != "203.0.113.7" || s1["UserName"] != "mum" {
		t.Errorf("bad session: %v", s1)
	}
	if s1["UserId"] != userJFID("2") {
		t.Errorf("user id mismatch")
	}
	ps := s1["PlayState"].(obj)
	if ps["PlayMethod"] != "Transcode" || ps["IsPaused"] != true || ps["PositionTicks"].(float64) != 600000000 {
		t.Errorf("bad playstate: %v", ps)
	}
	ti := s1["TranscodingInfo"].(obj)
	if ti["IsVideoDirect"] != false || ti["IsAudioDirect"] != true || ti["Bitrate"].(float64) != 4_000_000 {
		t.Errorf("bad transcoding info: %v", ti)
	}
	np := s1["NowPlayingItem"].(obj)
	if np["Type"] != "Episode" || np["Name"] != "Pilot" || np["SeriesName"] != "Show" || np["ParentIndexNumber"].(float64) != 1 || np["IndexNumber"].(float64) != 2 {
		t.Errorf("bad item: %v", np)
	}
	if ss[1]["TranscodingInfo"] != nil {
		t.Errorf("direct play must not carry TranscodingInfo")
	}
	// user ids agree between /Users and /Sessions
	var us []obj
	json.Unmarshal(get(t, h, "/t/"+testSecret+"/Users").Body, &us)
	if us[1]["Id"] != s1["UserId"] {
		t.Errorf("user id differs between /Users and /Sessions")
	}
}

func TestImageFromSession(t *testing.T) {
	silo := fakeSilo(t)
	defer silo.Close()
	h := newTestHandler(t, silo.URL, "http://x")
	get(t, h, "/t/"+testSecret+"/Sessions")
	resp := get(t, h, "/t/"+testSecret+"/Items/"+itemJFID("9")+"/Images/Primary")
	if resp.StatusCode != 200 || string(resp.Body) != "JPEG" {
		t.Fatalf("image: %d %q", resp.StatusCode, resp.Body)
	}
	if get(t, h, "/t/"+testSecret+"/Items/unknown/Images/Primary").StatusCode != 404 {
		t.Errorf("unknown image should 404")
	}
}

func TestProxyUsesAdminKey(t *testing.T) {
	var gotAuth, gotQuery string
	jelly := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotQuery = r.Header.Get("Authorization"), r.URL.RawQuery
		w.Write([]byte(`[]`))
	}))
	defer jelly.Close()
	h := newTestHandler(t, "http://x", jelly.URL)
	q, _ := structpb.NewStruct(map[string]any{"limit": "5"})
	resp, _ := h.Handle(context.Background(), &pluginv1.HandleHTTPRequest{Method: "GET", Path: "/t/" + testSecret + "/Library/VirtualFolders", Query: q})
	if resp.StatusCode != 200 || !strings.Contains(gotAuth, `Token="sa_key"`) || gotQuery != "limit=5" {
		t.Fatalf("proxy: %d auth=%q query=%q", resp.StatusCode, gotAuth, gotQuery)
	}
	if get(t, h, "/t/"+testSecret+"/Users/abc/Password").StatusCode != 404 {
		t.Errorf("non-allowlisted path must 404")
	}
}

func TestJFIDStable(t *testing.T) {
	if jfID("a") != jfID("a") || jfID("a") == jfID("b") || len(jfID("a")) != 32 {
		t.Fatal("id scheme broken")
	}
	// version 5 nibble
	if jfID("a")[12] != '5' {
		t.Fatal("not a v5 uuid")
	}
}
