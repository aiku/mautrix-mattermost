// Copyright 2024-2026 Aiku AI

package connector

import (
	"context"
	"testing"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/rs/zerolog"
	"gopkg.in/yaml.v3"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/id"
)

func TestGetName(t *testing.T) {
	mc := &MattermostConnector{}
	name := mc.GetName()

	if name.DisplayName != "Mattermost" {
		t.Errorf("DisplayName: got %q, want %q", name.DisplayName, "Mattermost")
	}
	if name.NetworkID != "mattermost" {
		t.Errorf("NetworkID: got %q, want %q", name.NetworkID, "mattermost")
	}
	if name.DefaultPort != 29319 {
		t.Errorf("DefaultPort: got %d, want %d", name.DefaultPort, 29319)
	}
	if name.NetworkURL != "https://mattermost.com" {
		t.Errorf("NetworkURL: got %q, want %q", name.NetworkURL, "https://mattermost.com")
	}
	if name.BeeperBridgeType != "mattermost" {
		t.Errorf("BeeperBridgeType: got %q, want %q", name.BeeperBridgeType, "mattermost")
	}
}

func TestGetCapabilities(t *testing.T) {
	mc := &MattermostConnector{}
	caps := mc.GetCapabilities()

	if caps == nil {
		t.Fatal("GetCapabilities returned nil")
	}
	if caps.DisappearingMessages {
		t.Error("DisappearingMessages should be false")
	}
	if caps.AggressiveUpdateInfo {
		t.Error("AggressiveUpdateInfo should be false")
	}
}

func TestGetBridgeInfoVersion(t *testing.T) {
	mc := &MattermostConnector{}
	info, caps := mc.GetBridgeInfoVersion()

	if info != 1 {
		t.Errorf("info version: got %d, want 1", info)
	}
	if caps != 1 {
		t.Errorf("caps version: got %d, want 1", caps)
	}
}

func TestGetDBMetaTypes(t *testing.T) {
	mc := &MattermostConnector{}
	meta := mc.GetDBMetaTypes()

	if meta.UserLogin == nil {
		t.Fatal("UserLogin meta factory should not be nil")
	}
	instance := meta.UserLogin()
	if _, ok := instance.(*UserLoginMetadata); !ok {
		t.Errorf("UserLogin factory returned %T, want *UserLoginMetadata", instance)
	}
}

// TestGetConfigBeforeInit ensures GetConfig returns an addressable config
// that the YAML decoder can write to, even before Init is called.
// Regression test: Config was previously a *Config pointer field, which was
// nil before Init, causing a panic in the mxmain YAML decoder.
func TestGetConfigBeforeInit(t *testing.T) {
	mc := &MattermostConnector{} // Init not called — mirrors mxmain.LoadConfig order
	example, data, upgrader := mc.GetConfig()

	if example == "" {
		t.Error("example config should not be empty")
	}
	if data == nil {
		t.Fatal("config data must not be nil before Init")
	}
	if upgrader == nil {
		t.Fatal("upgrader must not be nil")
	}

	// Simulate what mxmain.LoadConfig does: YAML decode into the data pointer.
	node := &yaml.Node{}
	if err := yaml.Unmarshal([]byte("server_url: http://test:8065\n"), node); err != nil {
		t.Fatalf("unmarshal YAML node: %v", err)
	}
	if err := node.Decode(data); err != nil {
		t.Fatalf("Decode into config should not panic or error: %v", err)
	}

	// Verify the decoded value landed in the connector's Config.
	if mc.Config.ServerURL != "http://test:8065" {
		t.Errorf("ServerURL after decode: got %q, want %q", mc.Config.ServerURL, "http://test:8065")
	}
}

func TestUserLoginMetadata(t *testing.T) {
	meta := &UserLoginMetadata{
		ServerURL: "http://mm.local:8065",
		Token:     "tok123",
		UserID:    "usr456",
		TeamID:    "team789",
	}

	if meta.ServerURL != "http://mm.local:8065" {
		t.Errorf("ServerURL: got %q", meta.ServerURL)
	}
	if meta.Token != "tok123" {
		t.Errorf("Token: got %q", meta.Token)
	}
	if meta.UserID != "usr456" {
		t.Errorf("UserID: got %q", meta.UserID)
	}
	if meta.TeamID != "team789" {
		t.Errorf("TeamID: got %q", meta.TeamID)
	}
}

func TestFindSuffix(t *testing.T) {
	tests := []struct {
		name   string
		s      string
		suffix string
		want   int
	}{
		{"ALICE_MXID", "ALICE_MXID=", "_MXID=", 5},
		{"BOB_SMITH_TOKEN", "BOB_SMITH_TOKEN=", "_TOKEN=", 9},
		{"starts_with_suffix", "_MXID=", "_MXID=", 0},
		{"no_match", "NOMATCH", "_MXID=", -1},
		{"empty_string", "", "_MXID=", -1},
		{"shorter_than_suffix", "SHORT", "_MXID=", -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := findSuffix(tt.s, tt.suffix)
			if got != tt.want {
				t.Errorf("findSuffix(%q, %q) = %d, want %d", tt.s, tt.suffix, got, tt.want)
			}
		})
	}
}

func TestInit(t *testing.T) {
	mc := &MattermostConnector{}
	bridge := &bridgev2.Bridge{}
	mc.Init(bridge)
	if mc.Bridge != bridge {
		t.Error("Init should set Bridge")
	}
}

func TestEnvToPuppetEntries(t *testing.T) {
	mc := &MattermostConnector{}
	t.Setenv("MATTERMOST_PUPPET_ALICE_MXID", "@alice:example.com")
	t.Setenv("MATTERMOST_PUPPET_ALICE_TOKEN", "tok-alice")
	t.Setenv("MATTERMOST_PUPPET_BOB_MXID", "@bob:example.com")
	t.Setenv("MATTERMOST_PUPPET_BOB_TOKEN", "tok-bob")

	entries := mc.envToPuppetEntries()

	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	// Check that both ALICE and BOB are present (order not guaranteed since map iteration).
	foundAlice, foundBob := false, false
	for _, e := range entries {
		if e.Slug == "ALICE" && e.MXID == "@alice:example.com" && e.Token == "tok-alice" {
			foundAlice = true
		}
		if e.Slug == "BOB" && e.MXID == "@bob:example.com" && e.Token == "tok-bob" {
			foundBob = true
		}
	}
	if !foundAlice {
		t.Error("ALICE entry not found")
	}
	if !foundBob {
		t.Error("BOB entry not found")
	}
}

func TestEnvToPuppetEntries_MissingToken(t *testing.T) {
	mc := &MattermostConnector{}
	t.Setenv("MATTERMOST_PUPPET_ONLY_MXID_MXID", "@only:example.com")
	// No TOKEN set

	entries := mc.envToPuppetEntries()
	for _, e := range entries {
		if e.Slug == "ONLY_MXID" {
			t.Error("entry with missing token should not be included")
		}
	}
}

func TestLoadPuppets(t *testing.T) {
	fake := newFakeMM()
	defer fake.Close()

	fake.Users["mm-puppet1"] = &model.User{Id: "mm-puppet1", Username: "puppet1"}
	fake.TokenToUser["tok-puppet1"] = "mm-puppet1"

	mc := &MattermostConnector{
		Bridge:  &bridgev2.Bridge{},
		Config:  Config{ServerURL: fake.Server.URL},
		Puppets: make(map[id.UserID]*PuppetClient),
	}
	mc.Bridge.Log = zerolog.Nop()

	t.Setenv("MATTERMOST_PUPPET_P1_MXID", "@puppet1:example.com")
	t.Setenv("MATTERMOST_PUPPET_P1_TOKEN", "tok-puppet1")

	mc.loadPuppets(context.Background())

	if len(mc.Puppets) != 1 {
		t.Fatalf("expected 1 puppet, got %d", len(mc.Puppets))
	}
	puppet, ok := mc.Puppets[id.UserID("@puppet1:example.com")]
	if !ok {
		t.Fatal("puppet not found")
	}
	if puppet.UserID != "mm-puppet1" {
		t.Errorf("UserID: got %q, want %q", puppet.UserID, "mm-puppet1")
	}
	if puppet.Username != "puppet1" {
		t.Errorf("Username: got %q, want %q", puppet.Username, "puppet1")
	}
}

func TestLoadPuppets_MissingToken(t *testing.T) {
	mc := &MattermostConnector{
		Bridge:  &bridgev2.Bridge{},
		Config:  Config{},
		Puppets: make(map[id.UserID]*PuppetClient),
	}
	mc.Bridge.Log = zerolog.Nop()

	t.Setenv("MATTERMOST_PUPPET_Q1_MXID", "@q1:example.com")
	// No TOKEN env var

	mc.loadPuppets(context.Background())

	if len(mc.Puppets) != 0 {
		t.Errorf("expected 0 puppets (missing token), got %d", len(mc.Puppets))
	}
}

func TestLoadPuppets_AuthFailure(t *testing.T) {
	fake := newFakeMM()
	defer fake.Close()
	// No users registered — GetMe will fail.

	mc := &MattermostConnector{
		Bridge:  &bridgev2.Bridge{},
		Config:  Config{ServerURL: fake.Server.URL},
		Puppets: make(map[id.UserID]*PuppetClient),
	}
	mc.Bridge.Log = zerolog.Nop()

	t.Setenv("MATTERMOST_PUPPET_BAD_MXID", "@bad:example.com")
	t.Setenv("MATTERMOST_PUPPET_BAD_TOKEN", "invalid-token")

	mc.loadPuppets(context.Background())

	if len(mc.Puppets) != 0 {
		t.Errorf("expected 0 puppets (auth failure), got %d", len(mc.Puppets))
	}
}

func TestLoadPuppets_CustomURL(t *testing.T) {
	fake := newFakeMM()
	defer fake.Close()

	fake.Users["mm-custom"] = &model.User{Id: "mm-custom", Username: "custom"}
	fake.TokenToUser["tok-custom"] = "mm-custom"

	mc := &MattermostConnector{
		Bridge:  &bridgev2.Bridge{},
		Config:  Config{ServerURL: "http://default-wont-be-used:8065"},
		Puppets: make(map[id.UserID]*PuppetClient),
	}
	mc.Bridge.Log = zerolog.Nop()

	t.Setenv("MATTERMOST_PUPPET_C1_MXID", "@custom:example.com")
	t.Setenv("MATTERMOST_PUPPET_C1_TOKEN", "tok-custom")
	t.Setenv("MATTERMOST_PUPPET_C1_URL", fake.Server.URL)

	mc.loadPuppets(context.Background())

	if len(mc.Puppets) != 1 {
		t.Fatalf("expected 1 puppet, got %d", len(mc.Puppets))
	}
}

func TestCheckAndSetRelay_NilBridge(t *testing.T) {
	mc := &MattermostConnector{Bridge: nil}
	// Should return immediately without panic.
	mc.checkAndSetRelay(context.Background())
}

func TestCheckAndSetRelay_NilDB(t *testing.T) {
	mc := &MattermostConnector{Bridge: &bridgev2.Bridge{}}
	// Bridge.DB is nil, should return immediately without panic.
	mc.checkAndSetRelay(context.Background())
}

func TestMakeUserLoginID_ParseUserLoginID_RoundTrip(t *testing.T) {
	// Verify empty string round-trips correctly.
	got := ParseUserLoginID(MakeUserLoginID(""))
	if got != "" {
		t.Errorf("empty string round trip: got %q, want %q", got, "")
	}

	// Verify non-empty round-trip.
	got = ParseUserLoginID(MakeUserLoginID("user123"))
	if got != "user123" {
		t.Errorf("non-empty round trip: got %q, want %q", got, "user123")
	}
}
