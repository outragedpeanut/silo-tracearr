package main

import (
	"context"
	"log"
	"net/url"
	"strings"
	"time"
)

var reconcileDelays = []time.Duration{2 * time.Second, 5 * time.Second, 15 * time.Second, 45 * time.Second}

// tracearrBase is the address Tracearr should use to reach Silo: the
// "Address Tracearr uses" setting, or the Silo API address when that is not a
// loopback address (a loopback address is only valid inside Silo's container).
func (c config) tracearrBase() string {
	if c.PublicAddr != "" {
		return c.PublicAddr
	}
	u, err := url.Parse(c.SiloURL)
	if err != nil || u.Host == "" {
		return ""
	}
	host := u.Hostname()
	if host == "localhost" || strings.HasPrefix(host, "127.") || host == "::1" {
		return ""
	}
	return strings.TrimRight(c.SiloURL, "/")
}

func (c config) desiredURL(id string) string {
	base := c.tracearrBase()
	if base == "" || id == "" || c.AccessSecret == "" {
		return ""
	}
	return base + "/api/v1/plugins/" + id + "/t/" + c.AccessSecret
}

// reconcile makes the stored settings complete without any button: it makes an
// access secret when there is none and keeps the "Tracearr server URL" field
// matching the current address, installation id and secret. Silo restarts the
// plugin after a settings save, and the next run finds nothing to change.
func (h *handler) reconcile() {
	h.mu.Lock()
	if h.reconciling {
		h.mu.Unlock()
		return
	}
	h.reconciling = true
	h.mu.Unlock()
	defer func() { h.mu.Lock(); h.reconciling = false; h.mu.Unlock() }()

	var lastErr error
	for attempt := 0; attempt <= len(reconcileDelays); attempt++ {
		if attempt > 0 {
			h.sleep(reconcileDelays[attempt-1])
		}
		changed, err := h.reconcileOnce(context.Background())
		if err == nil {
			_ = changed
			return
		}
		lastErr = err
	}
	log.Printf("tracearr-connector: could not finish setup: %v", lastErr)
	h.mu.Lock()
	h.saveErr = lastErr.Error()
	h.mu.Unlock()
}

func (h *handler) reconcileOnce(ctx context.Context) (bool, error) {
	cfg := *h.cfg.Load()
	if cfg.AdminAPIKey == "" {
		return false, nil
	}
	id, why := h.installationID(ctx, cfg)
	if id == "" {
		return false, errString("could not find this plugin in Silo: " + why)
	}

	fields := obj{}
	if cfg.AccessSecret == "" {
		secret, err := newSecret()
		if err != nil {
			return false, err
		}
		cfg.AccessSecret = secret
		h.cfg.Store(&cfg) // usable at once, before the save completes
		fields["access_secret"] = secret
	}
	if want := cfg.desiredURL(id); want != "" && want != cfg.TracearrURL {
		fields["tracearr_url"] = want
	}
	if cfg.ShowURL {
		fields["show_url"] = false // hide the URL again once the page has been saved
	}
	if len(fields) == 0 {
		return false, nil
	}
	h.mu.Lock()
	h.persisted = false
	h.mu.Unlock()
	return true, h.saveFields(ctx, cfg, id, fields)
}

type errString string

func (e errString) Error() string { return string(e) }
