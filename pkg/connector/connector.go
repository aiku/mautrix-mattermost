// Copyright 2024-2026 Aiku AI

package connector

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/mattermost/mattermost/server/public/model"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/id"
)

// PuppetEntry describes a single puppet agent for config-driven loading
// via the hot-reload JSON API.
type PuppetEntry struct {
	Slug  string `json:"slug"`
	MXID  string `json:"mxid"`
	Token string `json:"token"`
}

// PuppetClient holds a Mattermost API client for a specific Matrix user,
// allowing their messages to appear as a dedicated Mattermost bot/user.
type PuppetClient struct {
	MXID     id.UserID
	Client   *model.Client4
	UserID   string // Mattermost user/bot ID
	Username string
}

// MattermostConnector implements bridgev2.NetworkConnector for Mattermost.
type MattermostConnector struct {
	Bridge   *bridgev2.Bridge
	Config   Config
	Puppets  map[id.UserID]*PuppetClient
	puppetMu sync.RWMutex
}

var _ bridgev2.NetworkConnector = (*MattermostConnector)(nil)

func (mc *MattermostConnector) Init(bridge *bridgev2.Bridge) {
	mc.Bridge = bridge
}

func (mc *MattermostConnector) Start(ctx context.Context) error {
	if err := mc.Config.PostProcess(); err != nil {
		return fmt.Errorf("failed to post-process config: %w", err)
	}
	mc.Puppets = make(map[id.UserID]*PuppetClient)
	mc.loadPuppets(ctx)
	go mc.autoLogin(ctx)

	// Start continuous portal watcher for relay setup on new rooms.
	go mc.WatchNewPortals(ctx, 0)

	// Start admin HTTP API for puppet hot-reload.
	apiAddr := mc.Config.AdminAPIAddr
	if apiAddr == "" {
		apiAddr = os.Getenv("BRIDGE_API_ADDR")
	}
	if apiAddr == "" {
		apiAddr = ":29320"
	}
	if apiAddr != "" {
		mux := http.NewServeMux()
		mux.HandleFunc("/api/reload-puppets", mc.HandleReloadPuppets)
		server := &http.Server{
			Addr:         apiAddr,
			Handler:      mux,
			ReadTimeout:  10 * time.Second,
			WriteTimeout: 10 * time.Second,
			IdleTimeout:  60 * time.Second,
		}
		go func() {
			mc.Bridge.Log.Info().Str("addr", apiAddr).Msg("Starting bridge admin API")
			if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				mc.Bridge.Log.Error().Err(err).Msg("Bridge admin API error")
			}
		}()
	}

	return nil
}

// loadPuppets reads MATTERMOST_PUPPET_* env vars to create dedicated
// Mattermost clients for specific Matrix users. This allows a puppet bot
// to post as its own Mattermost identity instead of being relayed.
//
// Env var format:
//
//	MATTERMOST_PUPPET_<NAME>_MXID  = @puppet-bot:example.com
//	MATTERMOST_PUPPET_<NAME>_TOKEN = <mattermost bot access token>
//	MATTERMOST_PUPPET_<NAME>_URL   = http://mattermost:8065  (optional, falls back to network.server_url)
func (mc *MattermostConnector) loadPuppets(ctx context.Context) {
	// Scan for puppet env vars. We look for known names first,
	// then fall back to scanning MATTERMOST_PUPPET_*_MXID patterns.
	puppetNames := []string{}

	// Also scan environment for any MATTERMOST_PUPPET_*_MXID vars.
	for _, env := range os.Environ() {
		if len(env) > len("MATTERMOST_PUPPET_") {
			rest := env[len("MATTERMOST_PUPPET_"):]
			if idx := findSuffix(rest, "_MXID="); idx > 0 {
				name := rest[:idx]
				found := false
				for _, n := range puppetNames {
					if n == name {
						found = true
						break
					}
				}
				if !found {
					puppetNames = append(puppetNames, name)
				}
			}
		}
	}

	for _, name := range puppetNames {
		mxid := os.Getenv("MATTERMOST_PUPPET_" + name + "_MXID")
		token := os.Getenv("MATTERMOST_PUPPET_" + name + "_TOKEN")
		if mxid == "" || token == "" {
			continue
		}

		serverURL := os.Getenv("MATTERMOST_PUPPET_" + name + "_URL")
		if serverURL == "" {
			serverURL = mc.Config.ServerURL
		}

		client := model.NewAPIv4Client(serverURL)
		client.SetToken(token)

		me, _, err := client.GetMe(ctx, "")
		if err != nil {
			mc.Bridge.Log.Error().Err(err).
				Str("puppet", name).
				Str("mxid", mxid).
				Msg("Failed to verify puppet token")
			continue
		}

		puppet := &PuppetClient{
			MXID:     id.UserID(mxid),
			Client:   client,
			UserID:   me.Id,
			Username: me.Username,
		}
		mc.Puppets[puppet.MXID] = puppet
		mc.Bridge.Log.Info().
			Str("puppet", name).
			Str("mxid", mxid).
			Str("mm_username", me.Username).
			Str("mm_user_id", me.Id).
			Msg("Loaded puppet client")
	}
}

// findSuffix returns the index where suffix starts in s, or -1 if not found.
func findSuffix(s, suffix string) int {
	for i := 0; i <= len(s)-len(suffix); i++ {
		if s[i:i+len(suffix)] == suffix {
			return i
		}
	}
	return -1
}

// autoLogin checks for MATTERMOST_AUTO_TOKEN and MATTERMOST_AUTO_SERVER_URL
// env vars and performs an automatic login if no existing logins are found.
// This allows the bridge to connect on first boot without manual bot interaction.
func (mc *MattermostConnector) autoLogin(ctx context.Context) {
	token := os.Getenv("MATTERMOST_AUTO_TOKEN")
	serverURL := os.Getenv("MATTERMOST_AUTO_SERVER_URL")
	ownerMXID := os.Getenv("MATTERMOST_AUTO_OWNER_MXID")
	if token == "" || serverURL == "" || ownerMXID == "" {
		return
	}

	// Wait for the bridge framework to finish loading existing logins.
	time.Sleep(5 * time.Second)

	// Check if any logins already exist — if so, the framework handles reconnection.
	existingUsers, err := mc.Bridge.DB.UserLogin.GetAllUserIDsWithLogins(ctx)
	if err != nil {
		mc.Bridge.Log.Error().Err(err).Msg("Auto-login: failed to check existing logins")
		return
	}
	if len(existingUsers) > 0 {
		mc.Bridge.Log.Info().Int("count", len(existingUsers)).Msg("Existing logins found, skipping auto-login")
		return
	}

	mc.Bridge.Log.Info().Str("server_url", serverURL).Msg("Performing auto-login")

	client := model.NewAPIv4Client(serverURL)
	client.SetToken(token)

	me, _, err := client.GetMe(ctx, "")
	if err != nil {
		mc.Bridge.Log.Error().Err(err).Msg("Auto-login: failed to verify token")
		return
	}

	teams, _, err := client.GetTeamsForUser(ctx, me.Id, "")
	if err != nil {
		mc.Bridge.Log.Error().Err(err).Msg("Auto-login: failed to get teams")
		return
	}
	teamID := ""
	if len(teams) > 0 {
		teamID = teams[0].Id
	}

	loginID := MakeUserLoginID(me.Id)

	// Get or create the bridge user for the owner Matrix account.
	user, err := mc.Bridge.GetUserByMXID(ctx, id.UserID(ownerMXID))
	if err != nil {
		mc.Bridge.Log.Error().Err(err).Msg("Auto-login: failed to get bridge user")
		return
	}

	ul, err := user.NewLogin(ctx, &database.UserLogin{
		ID:         loginID,
		RemoteName: fmt.Sprintf("%s @ %s (auto)", me.Username, serverURL),
	}, &bridgev2.NewLoginParams{
		LoadUserLogin: mc.LoadUserLogin,
	})
	if err != nil {
		mc.Bridge.Log.Error().Err(err).Msg("Auto-login: failed to create login")
		return
	}

	meta := ul.Metadata.(*UserLoginMetadata)
	meta.ServerURL = serverURL
	meta.Token = token
	meta.UserID = me.Id
	meta.TeamID = teamID
	if err := ul.Save(ctx); err != nil {
		mc.Bridge.Log.Error().Err(err).Msg("Auto-login: failed to save login")
		return
	}

	mmClient := ul.Client.(*MattermostClient)
	mmClient.client = client
	mmClient.serverURL = serverURL
	mmClient.userID = me.Id
	mmClient.teamID = teamID
	mmClient.Connect(ctx)

	mc.Bridge.Log.Info().
		Str("username", me.Username).
		Str("server_url", serverURL).
		Msg("Auto-login complete")

	// Set relay for all portals so puppet users' Matrix messages get bridged.
	// The bridgev2 framework requires a per-portal relay login before it will
	// call HandleMatrixMessage (where our puppet system selects the correct
	// Mattermost client). Without relay, the framework rejects with "not
	// logged in" and the puppet system is never reached.
	go mc.autoSetRelay(ctx, ul)
}

// autoSetRelay sets the auto-login user as the relay for all bridged rooms.
// Runs with retries because portals are created asynchronously during
// Mattermost channel sync after the WebSocket connects.
func (mc *MattermostConnector) autoSetRelay(ctx context.Context, login *bridgev2.UserLogin) {
	// Wait for initial channel sync to create portals.
	time.Sleep(15 * time.Second)

	for attempt := range 3 {
		portals, err := mc.Bridge.GetAllPortalsWithMXID(ctx)
		if err != nil {
			mc.Bridge.Log.Error().Err(err).Msg("Auto-relay: failed to get portals")
			return
		}

		setCount := 0
		for _, portal := range portals {
			if portal.Relay == nil {
				if err := portal.SetRelay(ctx, login); err != nil {
					mc.Bridge.Log.Warn().Err(err).
						Str("portal_mxid", string(portal.MXID)).
						Msg("Auto-relay: failed to set relay")
				} else {
					setCount++
				}
			}
		}

		mc.Bridge.Log.Info().
			Int("set_count", setCount).
			Int("total_portals", len(portals)).
			Int("attempt", attempt+1).
			Msg("Auto-relay: updated portals")

		if attempt < 2 {
			time.Sleep(30 * time.Second)
		}
	}
}

func (mc *MattermostConnector) LoadUserLogin(_ context.Context, login *bridgev2.UserLogin) error {
	mmClient := NewMattermostClient(login, mc)
	login.Client = mmClient
	return nil
}

func (mc *MattermostConnector) GetName() bridgev2.BridgeName {
	return bridgev2.BridgeName{
		DisplayName:      "Mattermost",
		NetworkURL:       "https://mattermost.com",
		NetworkIcon:      "mxc://maunium.net/mattermost",
		NetworkID:        "mattermost",
		BeeperBridgeType: "mattermost",
		DefaultPort:      29319,
	}
}

func (mc *MattermostConnector) GetDBMetaTypes() database.MetaTypes {
	return database.MetaTypes{
		UserLogin: func() any {
			return &UserLoginMetadata{}
		},
	}
}

func (mc *MattermostConnector) GetCapabilities() *bridgev2.NetworkGeneralCapabilities {
	return &bridgev2.NetworkGeneralCapabilities{
		DisappearingMessages: false,
		AggressiveUpdateInfo: false,
	}
}

func (mc *MattermostConnector) GetBridgeInfoVersion() (info, capabilities int) {
	return 1, 1
}

// UserLoginMetadata stores Mattermost-specific login data.
type UserLoginMetadata struct {
	ServerURL string `json:"server_url"`
	Token     string `json:"token"`
	UserID    string `json:"user_id"`
	TeamID    string `json:"team_id"`
}

// MakeUserLoginID creates a UserLoginID from a Mattermost user ID.
func MakeUserLoginID(userID string) networkid.UserLoginID {
	return networkid.UserLoginID(userID)
}

// ParseUserLoginID extracts the Mattermost user ID from a UserLoginID.
func ParseUserLoginID(loginID networkid.UserLoginID) string {
	return string(loginID)
}

// IsPuppetUserID returns true if the given Mattermost user ID belongs to
// any loaded puppet bot. Thread-safe.
func (mc *MattermostConnector) IsPuppetUserID(mmUserID string) bool {
	mc.puppetMu.RLock()
	defer mc.puppetMu.RUnlock()
	for _, puppet := range mc.Puppets {
		if puppet.UserID == mmUserID {
			return true
		}
	}
	return false
}

// ReloadPuppets re-reads puppet configuration from environment variables and
// updates the Puppets map. New puppets are added, removed env vars cause
// puppet removal. Existing puppets with unchanged tokens are kept as-is.
// Returns the number of added and removed puppets.
func (mc *MattermostConnector) ReloadPuppets(ctx context.Context) (added, removed int) {
	entries := mc.envToPuppetEntries()
	return mc.ReloadPuppetsFromEntries(ctx, entries)
}

// envToPuppetEntries scans the current environment for puppet config pairs
// and returns them as PuppetEntry values.
func (mc *MattermostConnector) envToPuppetEntries() []PuppetEntry {
	const prefix = "MATTERMOST_PUPPET_"
	const mxidSuffix = "_MXID"
	const tokenSuffix = "_TOKEN"

	slugs := make(map[string]struct{})
	for _, env := range os.Environ() {
		parts := strings.SplitN(env, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := parts[0]
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		rest := key[len(prefix):]
		if strings.HasSuffix(rest, mxidSuffix) {
			slug := rest[:len(rest)-len(mxidSuffix)]
			slugs[slug] = struct{}{}
		}
	}

	var entries []PuppetEntry
	for slug := range slugs {
		mxidVal := os.Getenv(prefix + slug + mxidSuffix)
		tokenVal := os.Getenv(prefix + slug + tokenSuffix)
		if mxidVal != "" && tokenVal != "" {
			entries = append(entries, PuppetEntry{Slug: slug, MXID: mxidVal, Token: tokenVal})
		}
	}
	return entries
}

// ReloadPuppetsFromEntries updates the puppet map from an explicit list of
// entries. This is the core reload logic used by both env-based reload and
// the HTTP API endpoint. Thread-safe.
func (mc *MattermostConnector) ReloadPuppetsFromEntries(ctx context.Context, entries []PuppetEntry) (added, removed int) {
	// Build desired set from entries.
	desired := make(map[id.UserID]PuppetEntry, len(entries))
	for _, e := range entries {
		desired[id.UserID(e.MXID)] = e
	}

	mc.puppetMu.Lock()
	defer mc.puppetMu.Unlock()

	// Remove puppets that are no longer in the desired set.
	for uid := range mc.Puppets {
		if _, ok := desired[uid]; !ok {
			mc.Bridge.Log.Info().Str("mxid", string(uid)).Msg("Removing puppet")
			delete(mc.Puppets, uid)
			removed++
		}
	}

	// Add or update puppets.
	for uid, entry := range desired {
		existing, ok := mc.Puppets[uid]
		if ok && existing.Client != nil && existing.Client.AuthToken == entry.Token {
			// Unchanged -- keep as-is.
			continue
		}

		serverURL := os.Getenv("MATTERMOST_PUPPET_" + entry.Slug + "_URL")
		if serverURL == "" {
			serverURL = mc.Config.ServerURL
		}

		client := model.NewAPIv4Client(serverURL)
		client.SetToken(entry.Token)

		me, _, err := client.GetMe(ctx, "")
		if err != nil {
			mc.Bridge.Log.Error().Err(err).
				Str("slug", entry.Slug).
				Str("mxid", entry.MXID).
				Msg("Failed to authenticate puppet during reload, skipping")
			continue
		}

		puppet := &PuppetClient{
			MXID:     uid,
			Client:   client,
			UserID:   me.Id,
			Username: me.Username,
		}
		mc.Puppets[uid] = puppet
		added++

		mc.Bridge.Log.Info().
			Str("slug", entry.Slug).
			Str("mxid", entry.MXID).
			Str("mm_user_id", me.Id).
			Str("mm_username", me.Username).
			Msg("Hot-loaded puppet")
	}

	mc.Bridge.Log.Info().
		Int("added", added).
		Int("removed", removed).
		Int("total", len(mc.Puppets)).
		Msg("Puppet reload complete")

	return added, removed
}

// PuppetCount returns the current number of loaded puppets. Thread-safe.
func (mc *MattermostConnector) PuppetCount() int {
	mc.puppetMu.RLock()
	defer mc.puppetMu.RUnlock()
	return len(mc.Puppets)
}

// maxReloadBodySize is the maximum allowed request body for puppet reload (1 MB).
const maxReloadBodySize = 1 << 20

// HandleReloadPuppets is an HTTP handler for POST /api/reload-puppets.
// It accepts an optional JSON body with explicit puppet entries; if the body
// is empty or absent, it reloads from environment variables.
func (mc *MattermostConnector) HandleReloadPuppets(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	mc.Bridge.Log.Info().
		Str("remote_addr", r.RemoteAddr).
		Str("content_length", r.Header.Get("Content-Length")).
		Msg("Puppet reload requested")

	ctx := r.Context()
	var added, removed int

	// Try to read entries from body.
	var entries []PuppetEntry
	if r.Body != nil && r.ContentLength != 0 {
		r.Body = http.MaxBytesReader(w, r.Body, maxReloadBodySize)
		defer r.Body.Close()
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			return
		}
		if len(body) > 0 {
			if err := json.Unmarshal(body, &entries); err != nil {
				http.Error(w, "invalid JSON", http.StatusBadRequest)
				return
			}
		}
	}

	mc.Bridge.Log.Info().
		Str("remote_addr", r.RemoteAddr).
		Int("entries", len(entries)).
		Str("source", func() string {
			if len(entries) > 0 {
				return "body"
			}
			return "env"
		}()).
		Msg("Processing puppet reload")

	if len(entries) > 0 {
		added, removed = mc.ReloadPuppetsFromEntries(ctx, entries)
	} else {
		added, removed = mc.ReloadPuppets(ctx)
	}

	resp := map[string]int{
		"added":   added,
		"removed": removed,
		"total":   mc.PuppetCount(),
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		mc.Bridge.Log.Warn().Err(err).Msg("Failed to write reload response")
	}
}

// WatchNewPortals periodically checks for new portal rooms that don't have
// relay set, and sets the relay user on them. This replaces the fixed 3-attempt
// boot cycle with continuous monitoring for rooms created after startup (e.g.,
// when a new PL agent is provisioned).
//
// The interval parameter controls how often the check runs. Pass 0 to use
// the default of 60 seconds.
func (mc *MattermostConnector) WatchNewPortals(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 60 * time.Second
	}

	mc.Bridge.Log.Info().
		Dur("interval", interval).
		Msg("Starting WatchNewPortals loop")

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			mc.Bridge.Log.Info().Msg("WatchNewPortals stopped")
			return
		case <-ticker.C:
			mc.checkAndSetRelay(ctx)
		}
	}
}

// checkAndSetRelay scans portal rooms and sets relay on any that lack it.
func (mc *MattermostConnector) checkAndSetRelay(ctx context.Context) {
	if mc.Bridge == nil || mc.Bridge.DB == nil {
		return
	}
	portals, err := mc.Bridge.GetAllPortalsWithMXID(ctx)
	if err != nil {
		mc.Bridge.Log.Error().Err(err).Msg("WatchNewPortals: failed to get portals")
		return
	}

	// Find the auto-login user to use as relay.
	loginUsers, err := mc.Bridge.DB.UserLogin.GetAllUserIDsWithLogins(ctx)
	if err != nil || len(loginUsers) == 0 {
		return
	}

	setCount := 0
	for _, portal := range portals {
		if portal.Relay == nil {
			// Get any available login to use as relay.
			for _, userID := range loginUsers {
				user, err := mc.Bridge.GetUserByMXID(ctx, userID)
				if err != nil {
					continue
				}
				logins := user.GetUserLogins()
				if len(logins) > 0 {
					if err := portal.SetRelay(ctx, logins[0]); err != nil {
						mc.Bridge.Log.Warn().Err(err).
							Str("portal_mxid", string(portal.MXID)).
							Msg("WatchNewPortals: failed to set relay")
					} else {
						setCount++
					}
					break
				}
			}
		}
	}

	if setCount > 0 {
		mc.Bridge.Log.Info().
			Int("set_count", setCount).
			Int("total_portals", len(portals)).
			Msg("WatchNewPortals: set relay on new portals")
	}
}
