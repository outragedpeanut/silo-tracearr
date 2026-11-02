package main

import (
	"testing"

	"github.com/Silo-Server/silo-plugin-sdk/pkg/pluginsdk/manifest"
)

// Unknown manifest keys are silently dropped by the SDK, so check that the
// fields this plugin depends on survive parsing.
func TestManifestFields(t *testing.T) {
	m, err := manifest.LoadWithChecksum(manifestJSON, pluginVersion)
	if err != nil {
		t.Fatal(err)
	}
	if m.GetPluginId() != "community.tracearr-connector" || m.GetVersion() != pluginVersion {
		t.Errorf("id/version: %s %s", m.GetPluginId(), m.GetVersion())
	}
	access := map[string]string{}
	navigable := 0
	for _, r := range m.GetHttpRoutes() {
		access[r.GetPath()] = r.GetAccess()
		if r.GetNavigable() && r.GetNavigationKind() == "admin" && r.GetNavigationLabel() != "" {
			navigable++
		}
	}
	if access["/t/*"] != "public" || access["/setup"] != "admin" || access["/setup/api/*"] != "admin" || navigable != 1 {
		t.Errorf("routes: %v navigable=%d", access, navigable)
	}
	cs := m.GetGlobalConfigSchema()
	if len(cs) != 1 || cs[0].GetKey() != "connection" {
		t.Fatalf("config schema: %v", cs)
	}
	fields := cs[0].GetAdminForm().GetFields()
	if len(fields) != 4 {
		t.Fatalf("form fields: %d", len(fields))
	}
	secrets := 0
	for _, f := range fields {
		if f.GetSecret() {
			secrets++
		}
	}
	if secrets != 2 {
		t.Errorf("secret fields: %d", secrets)
	}
	if m.GetPresentation().GetDisplayName() == "" || m.GetPresentation().GetLicenseSpdx() == "" {
		t.Errorf("presentation lost")
	}
	if m.GetChecksum() == "" || m.GetChecksum() == "__CHECKSUM__" {
		t.Errorf("checksum not stamped")
	}
}
