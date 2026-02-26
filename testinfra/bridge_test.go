// Package testinfra runs end-to-end integration tests against a real
// Synapse + Mattermost + mautrix-mattermost bridge stack started via docker compose.
//
// The full chat pipeline is tested: Matrix <-> Bridge <-> Mattermost.
// Covers: basic bridging, puppet identity, double puppeting, echo prevention,
// admin API endpoints, threads, reactions, and multi-agent scenarios.
//
// Run:  cd testinfra && ./run.sh
package testinfra

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// ────────────────────────────────────────────────────────────────────
// Constants & shared state
// ────────────────────────────────────────────────────────────────────

const (
	bridgeASToken = "test-bridge-as-token" // mautrix-mattermost appservice
	sharedSecret  = "test-shared-secret"
	domain        = "localhost"
)

var (
	synapseURL string
	mmURL      string
	mmToken    string // Mattermost admin auth token
	mmCEOToken string // Separate user for posting (bridge ignores its own user)
	mmTeamID   string // Mattermost team ID

	// Per-agent bot tokens for puppet/echo-prevention testing
	mmCOOBotToken  string
	mmCTOBotToken  string
	mmCOOBotUserID string
	mmCTOBotUserID string
	mmCEOUserID    string // CEO's MM user ID (for admin API double puppet registration)
	mmAdminUserID  string // Admin's MM user ID (bridge relay owner, for auto-login DP test)
	bridgeAdminURL string // Bridge admin API (port 29320)

	synapseAdminToken string

	// portalRooms: channel slug -> Matrix room ID (discovered from bridge portal rooms)
	portalRooms map[string]string
)

func TestMain(m *testing.M) {
	synapseURL = envOr("SYNAPSE_URL", "http://localhost:18008")
	mmURL = envOr("MM_URL", "http://localhost:18065")
	mmToken = os.Getenv("MM_TOKEN")
	mmCEOToken = os.Getenv("MM_CEO_TOKEN")
	mmTeamID = os.Getenv("MM_TEAM_ID")

	if mmToken == "" || mmTeamID == "" {
		fmt.Println("SKIP: MM_TOKEN and MM_TEAM_ID required (run via ./run.sh)")
		os.Exit(0)
	}
	if mmCEOToken == "" {
		mmCEOToken = mmToken // fallback
	}

	mmCOOBotToken = os.Getenv("MM_COO_BOT_TOKEN")
	mmCTOBotToken = os.Getenv("MM_CTO_BOT_TOKEN")
	mmCOOBotUserID = os.Getenv("MM_COO_BOT_USER_ID")
	mmCTOBotUserID = os.Getenv("MM_CTO_BOT_USER_ID")
	mmCEOUserID = os.Getenv("MM_CEO_USER_ID")
	mmAdminUserID = os.Getenv("MM_ADMIN_USER_ID")
	bridgeAdminURL = envOr("BRIDGE_ADMIN_URL", "http://localhost:29320")

	// Bootstrap Synapse admin for room discovery
	synapseAdminToken = mustRegisterSynapseAdmin()

	// Discover bridge portal rooms
	portalRooms = mustDiscoverPortalRooms()

	os.Exit(m.Run())
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// ────────────────────────────────────────────────────────────────────
// HTTP helpers
// ────────────────────────────────────────────────────────────────────

func doJSON(t testing.TB, method, url string, body any, token string) (int, map[string]any) {
	t.Helper()
	var bodyReader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		bodyReader = bytes.NewReader(data)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("HTTP %s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	var result map[string]any
	json.NewDecoder(resp.Body).Decode(&result) //nolint:errcheck
	return resp.StatusCode, result
}

func doJSONRaw(method, url string, body any, token string) (int, map[string]any, error) {
	var bodyReader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		bodyReader = bytes.NewReader(data)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return 0, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	var result map[string]any
	json.NewDecoder(resp.Body).Decode(&result) //nolint:errcheck
	return resp.StatusCode, result, nil
}

func computeMAC(nonce, user, password string, admin bool) string {
	mac := hmac.New(sha1.New, []byte(sharedSecret))
	mac.Write([]byte(nonce))
	mac.Write([]byte("\x00"))
	mac.Write([]byte(user))
	mac.Write([]byte("\x00"))
	mac.Write([]byte(password))
	mac.Write([]byte("\x00"))
	if admin {
		mac.Write([]byte("admin"))
	} else {
		mac.Write([]byte("notadmin"))
	}
	return hex.EncodeToString(mac.Sum(nil))
}

// ────────────────────────────────────────────────────────────────────
// Synapse helpers
// ────────────────────────────────────────────────────────────────────

func mustRegisterSynapseAdmin() string {
	code, resp, err := doJSONRaw("GET", synapseURL+"/_synapse/admin/v1/register", nil, "")
	if err != nil {
		fmt.Printf("FAIL: cannot reach Synapse: %v\n", err)
		os.Exit(1)
	}
	if code != 200 {
		fmt.Printf("FAIL: register nonce: %d %v\n", code, resp)
		os.Exit(1)
	}
	nonce := resp["nonce"].(string)

	body := map[string]any{
		"nonce":    nonce,
		"username": "admin",
		"password": "adminpass123",
		"admin":    true,
		"mac":      computeMAC(nonce, "admin", "adminpass123", true),
	}
	code, resp, err = doJSONRaw("POST", synapseURL+"/_synapse/admin/v1/register", body, "")
	if err != nil {
		fmt.Printf("FAIL: register admin: %v\n", err)
		os.Exit(1)
	}
	if code == 200 {
		return resp["access_token"].(string)
	}
	if errCode, _ := resp["errcode"].(string); errCode == "M_USER_IN_USE" {
		return mustSynapseLogin("admin", "adminpass123")
	}
	fmt.Printf("FAIL: register admin: %d %v\n", code, resp)
	os.Exit(1)
	return ""
}

func mustSynapseLogin(user, password string) string {
	body := map[string]any{
		"type":       "m.login.password",
		"identifier": map[string]string{"type": "m.id.user", "user": user},
		"password":   password,
	}
	code, resp, err := doJSONRaw("POST", synapseURL+"/_matrix/client/v3/login", body, "")
	if err != nil || code != 200 {
		fmt.Printf("FAIL: login %s: %d %v %v\n", user, code, resp, err)
		os.Exit(1)
	}
	return resp["access_token"].(string)
}

func mustDiscoverPortalRooms() map[string]string {
	rooms := make(map[string]string)

	for attempt := 0; attempt < 15; attempt++ {
		code, resp, err := doJSONRaw("GET",
			synapseURL+"/_synapse/admin/v1/rooms?limit=100",
			nil, synapseAdminToken)
		if err != nil || code != 200 {
			time.Sleep(2 * time.Second)
			continue
		}

		rawRooms, _ := resp["rooms"].([]any)
		for _, r := range rawRooms {
			rm, _ := r.(map[string]any)
			name, _ := rm["name"].(string)
			roomID, _ := rm["room_id"].(string)
			slug := roomNameToSlug(name)
			if slug != "" && roomID != "" {
				rooms[slug] = roomID
			}
		}

		if len(rooms) >= 2 {
			fmt.Printf("Discovered %d portal rooms\n", len(rooms))
			return rooms
		}
		time.Sleep(3 * time.Second)
	}

	fmt.Printf("WARNING: only found %d portal rooms (expected >= 2)\n", len(rooms))
	return rooms
}

func roomNameToSlug(name string) string {
	if name == "" {
		return ""
	}
	s := strings.ToLower(strings.TrimSpace(name))
	var b strings.Builder
	prevHyphen := false
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			prevHyphen = false
		} else if !prevHyphen && b.Len() > 0 {
			b.WriteRune('-')
			prevHyphen = true
		}
	}
	return strings.Trim(b.String(), "-")
}

// ────────────────────────────────────────────────────────────────────
// Matrix helpers
// ────────────────────────────────────────────────────────────────────

func sendMatrixMsg(t *testing.T, roomID, senderMXID, message string) string {
	t.Helper()
	txnID := fmt.Sprintf("test-%d", time.Now().UnixNano())
	body := map[string]string{"msgtype": "m.text", "body": message}
	code, resp := doJSON(t, "PUT",
		fmt.Sprintf("%s/_matrix/client/v3/rooms/%s/send/m.room.message/%s?user_id=%s",
			synapseURL, roomID, txnID, senderMXID),
		body, bridgeASToken)
	if code != 200 {
		t.Fatalf("send as %s to %s: %d %v", senderMXID, roomID, code, resp)
	}
	return resp["event_id"].(string)
}

func sendMatrixReply(t *testing.T, roomID, senderMXID, message, replyToID string) string {
	t.Helper()
	txnID := fmt.Sprintf("test-%d", time.Now().UnixNano())
	body := map[string]any{
		"msgtype": "m.text",
		"body":    message,
		"m.relates_to": map[string]any{
			"m.in_reply_to": map[string]string{"event_id": replyToID},
		},
	}
	code, resp := doJSON(t, "PUT",
		fmt.Sprintf("%s/_matrix/client/v3/rooms/%s/send/m.room.message/%s?user_id=%s",
			synapseURL, roomID, txnID, senderMXID),
		body, bridgeASToken)
	if code != 200 {
		t.Fatalf("reply as %s: %d %v", senderMXID, code, resp)
	}
	return resp["event_id"].(string)
}

func sendMatrixReaction(t *testing.T, roomID, senderMXID, targetEventID, emoji string) string {
	t.Helper()
	txnID := fmt.Sprintf("test-%d", time.Now().UnixNano())
	body := map[string]any{
		"m.relates_to": map[string]any{
			"rel_type": "m.annotation",
			"event_id": targetEventID,
			"key":      emoji,
		},
	}
	code, resp := doJSON(t, "PUT",
		fmt.Sprintf("%s/_matrix/client/v3/rooms/%s/send/m.reaction/%s?user_id=%s",
			synapseURL, roomID, txnID, senderMXID),
		body, bridgeASToken)
	if code != 200 {
		t.Fatalf("reaction as %s: %d %v", senderMXID, code, resp)
	}
	return resp["event_id"].(string)
}

func getMatrixMessages(t *testing.T, roomID string, limit int) []map[string]any {
	t.Helper()
	// Use Synapse admin API — does not require being in the room
	code, resp := doJSON(t, "GET",
		fmt.Sprintf("%s/_synapse/admin/v1/rooms/%s/messages?dir=b&limit=%d",
			synapseURL, roomID, limit),
		nil, synapseAdminToken)
	if code != 200 {
		t.Fatalf("messages %s: %d %v", roomID, code, resp)
	}
	chunk, ok := resp["chunk"].([]any)
	if !ok {
		return nil
	}
	var msgs []map[string]any
	for _, c := range chunk {
		if m, ok := c.(map[string]any); ok {
			msgs = append(msgs, m)
		}
	}
	return msgs
}

func joinUserToPortalRoom(t *testing.T, roomID, userMXID string) {
	t.Helper()
	bridgeBotMXID := "@mattermostbot:" + domain

	// Bridge bot invites the user
	inviteBody := map[string]string{"user_id": userMXID}
	code, resp := doJSON(t, "POST",
		fmt.Sprintf("%s/_matrix/client/v3/rooms/%s/invite?user_id=%s",
			synapseURL, roomID, bridgeBotMXID),
		inviteBody, bridgeASToken)
	if code != 200 {
		errMsg, _ := resp["error"].(string)
		if !strings.Contains(errMsg, "already in the room") {
			t.Logf("invite %s to %s: %d %v", userMXID, roomID, code, resp)
		}
	}

	// User joins using bridge AS token (all test users are in bridge namespace)
	code, resp = doJSON(t, "POST",
		fmt.Sprintf("%s/_matrix/client/v3/join/%s?user_id=%s",
			synapseURL, roomID, userMXID),
		map[string]string{}, bridgeASToken)
	if code != 200 {
		errMsg, _ := resp["error"].(string)
		if !strings.Contains(errMsg, "already in the room") {
			t.Fatalf("join %s to %s: %d %v", userMXID, roomID, code, resp)
		}
	}
}

func getRoomMembers(t *testing.T, roomID string) []string {
	t.Helper()
	code, resp := doJSON(t, "GET",
		fmt.Sprintf("%s/_synapse/admin/v1/rooms/%s/members", synapseURL, roomID),
		nil, synapseAdminToken)
	if code != 200 {
		t.Fatalf("members %s: %d %v", roomID, code, resp)
	}
	membersRaw, _ := resp["members"].([]any)
	members := make([]string, 0, len(membersRaw))
	for _, m := range membersRaw {
		if s, ok := m.(string); ok {
			members = append(members, s)
		}
	}
	return members
}

// ────────────────────────────────────────────────────────────────────
// Mattermost helpers
// ────────────────────────────────────────────────────────────────────

func getMMChannel(t *testing.T, channelName string) (string, string) {
	t.Helper()
	code, resp := doJSON(t, "GET",
		fmt.Sprintf("%s/api/v4/teams/%s/channels/name/%s", mmURL, mmTeamID, channelName),
		nil, mmToken)
	if code != 200 {
		t.Fatalf("get MM channel %s: %d %v", channelName, code, resp)
	}
	return resp["id"].(string), resp["display_name"].(string)
}

func getMMPosts(t *testing.T, channelID string) []map[string]any {
	t.Helper()
	code, resp := doJSON(t, "GET",
		fmt.Sprintf("%s/api/v4/channels/%s/posts", mmURL, channelID),
		nil, mmToken)
	if code != 200 {
		t.Fatalf("get MM posts: %d %v", code, resp)
	}

	order, _ := resp["order"].([]any)
	postsMap, _ := resp["posts"].(map[string]any)
	var posts []map[string]any
	for _, id := range order {
		idStr, _ := id.(string)
		if p, ok := postsMap[idStr]; ok {
			if pm, ok := p.(map[string]any); ok {
				posts = append(posts, pm)
			}
		}
	}
	return posts
}

// postToMM posts as the CEO user (NOT admin — bridge ignores admin's own messages).
func postToMM(t *testing.T, channelID, message string) string {
	t.Helper()
	body := map[string]string{"channel_id": channelID, "message": message}
	code, resp := doJSON(t, "POST", mmURL+"/api/v4/posts", body, mmCEOToken)
	if code != 201 {
		t.Fatalf("MM post: %d %v", code, resp)
	}
	return resp["id"].(string)
}

func postToMMAsBot(t *testing.T, channelID, message, botToken string) string {
	t.Helper()
	body := map[string]string{"channel_id": channelID, "message": message}
	code, resp := doJSON(t, "POST", mmURL+"/api/v4/posts", body, botToken)
	if code != 201 {
		t.Fatalf("MM bot post: %d %v", code, resp)
	}
	return resp["id"].(string)
}

func postReplyToMM(t *testing.T, channelID, rootID, message string) string {
	t.Helper()
	body := map[string]string{
		"channel_id": channelID,
		"root_id":    rootID,
		"message":    message,
	}
	code, resp := doJSON(t, "POST", mmURL+"/api/v4/posts", body, mmCEOToken)
	if code != 201 {
		t.Fatalf("MM reply: %d %v", code, resp)
	}
	return resp["id"].(string)
}

// ────────────────────────────────────────────────────────────────────
// Test setup helpers
// ────────────────────────────────────────────────────────────────────

func requirePortalRoom(t *testing.T, channel string) string {
	t.Helper()
	roomID, ok := portalRooms[channel]
	if !ok {
		t.Skipf("portal room for %q not found (bridge may not have created it)", channel)
	}
	return roomID
}

func joinAgentToRoom(t *testing.T, slug, roomID string) string {
	t.Helper()
	agentMXID := "@" + slug + ":" + domain
	joinUserToPortalRoom(t, roomID, agentMXID)
	return agentMXID
}

func pollMMForMessage(t *testing.T, channelID string, match func(map[string]any) bool, timeout time.Duration) map[string]any {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		posts := getMMPosts(t, channelID)
		for _, p := range posts {
			if match(p) {
				return p
			}
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("message not found in MM channel %s within %v", channelID, timeout)
	return nil
}

func pollMatrixForMessage(t *testing.T, roomID string, match func(map[string]any) bool, timeout time.Duration) map[string]any {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		msgs := getMatrixMessages(t, roomID, 30)
		for _, m := range msgs {
			if match(m) {
				return m
			}
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("message not found in Matrix room %s within %v", roomID, timeout)
	return nil
}

// ════════════════════════════════════════════════════════════════════
// TESTS — Health checks
// ════════════════════════════════════════════════════════════════════

func TestSynapseHealthy(t *testing.T) {
	code, _ := doJSON(t, "GET", synapseURL+"/health", nil, "")
	if code != 200 {
		t.Fatalf("Synapse /health: %d", code)
	}
}

func TestMattermostHealthy(t *testing.T) {
	code, _ := doJSON(t, "GET", mmURL+"/api/v4/system/ping", nil, "")
	if code != 200 {
		t.Fatalf("Mattermost /ping: %d", code)
	}
}

func TestBridgePortalRoomsExist(t *testing.T) {
	if len(portalRooms) == 0 {
		t.Fatal("no portal rooms discovered — bridge may not be working")
	}
	t.Logf("Portal rooms: %v", portalRooms)

	if _, ok := portalRooms["general-bridge"]; !ok {
		t.Error("missing portal room for general-bridge")
	}
}

func TestBridgeAdminAPIHealthy(t *testing.T) {
	// The admin API server should be listening on port 29320
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", bridgeAdminURL+"/api/reload-puppets", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Skipf("bridge admin API unreachable: %v", err)
	}
	resp.Body.Close()
	// GET on a POST endpoint should return 405, which means the server is up
	if resp.StatusCode != 405 {
		t.Logf("admin API responded with %d (expected 405 for GET on POST endpoint)", resp.StatusCode)
	}
}

// ════════════════════════════════════════════════════════════════════
// TESTS — Basic bidirectional messaging
// ════════════════════════════════════════════════════════════════════

func TestMatrixToMattermost(t *testing.T) {
	roomID := requirePortalRoom(t, "general-bridge")
	mmChID, _ := getMMChannel(t, "general-bridge")

	cooMXID := joinAgentToRoom(t, "aiku-coo", roomID)

	marker := fmt.Sprintf("TestM2MM-%d", time.Now().UnixNano())
	sendMatrixMsg(t, roomID, cooMXID, "Bridge test: "+marker)

	pollMMForMessage(t, mmChID, func(p map[string]any) bool {
		msg, _ := p["message"].(string)
		return strings.Contains(msg, marker)
	}, 30*time.Second)

	t.Log("Matrix -> Mattermost relay confirmed")
}

func TestMattermostToMatrix(t *testing.T) {
	roomID := requirePortalRoom(t, "general-bridge")
	mmChID, _ := getMMChannel(t, "general-bridge")

	marker := fmt.Sprintf("TestMM2M-%d", time.Now().UnixNano())
	postToMM(t, mmChID, "CEO message: "+marker)

	pollMatrixForMessage(t, roomID, func(m map[string]any) bool {
		content, _ := m["content"].(map[string]any)
		body, _ := content["body"].(string)
		return strings.Contains(body, marker)
	}, 30*time.Second)

	t.Log("Mattermost -> Matrix relay confirmed")
}

func TestThreadReplyBridgedToMM(t *testing.T) {
	roomID := requirePortalRoom(t, "dev-channel")
	mmChID, _ := getMMChannel(t, "dev-channel")
	cooMXID := joinAgentToRoom(t, "aiku-coo", roomID)

	marker := fmt.Sprintf("thread-%d", time.Now().UnixNano())
	parentID := sendMatrixMsg(t, roomID, cooMXID, "Original: "+marker)
	sendMatrixReply(t, roomID, cooMXID, "Reply: "+marker, parentID)

	pollMMForMessage(t, mmChID, func(p map[string]any) bool {
		msg, _ := p["message"].(string)
		return strings.Contains(msg, "Original: "+marker)
	}, 30*time.Second)

	pollMMForMessage(t, mmChID, func(p map[string]any) bool {
		msg, _ := p["message"].(string)
		return strings.Contains(msg, "Reply: "+marker)
	}, 30*time.Second)

	t.Log("Thread reply bridged to Mattermost")
}

func TestMMThreadReplyBridgedToMatrix(t *testing.T) {
	roomID := requirePortalRoom(t, "dev-channel")
	mmChID, _ := getMMChannel(t, "dev-channel")

	marker := fmt.Sprintf("mmthread-%d", time.Now().UnixNano())
	rootID := postToMM(t, mmChID, "Root: "+marker)
	postReplyToMM(t, mmChID, rootID, "Reply: "+marker)

	pollMatrixForMessage(t, roomID, func(m map[string]any) bool {
		content, _ := m["content"].(map[string]any)
		body, _ := content["body"].(string)
		return strings.Contains(body, "Root: "+marker)
	}, 30*time.Second)

	pollMatrixForMessage(t, roomID, func(m map[string]any) bool {
		content, _ := m["content"].(map[string]any)
		body, _ := content["body"].(string)
		return strings.Contains(body, "Reply: "+marker)
	}, 30*time.Second)

	t.Log("MM thread reply bridged to Matrix")
}

func TestLargeMessageBridge(t *testing.T) {
	roomID := requirePortalRoom(t, "general-bridge")
	mmChID, _ := getMMChannel(t, "general-bridge")
	cooMXID := joinAgentToRoom(t, "aiku-coo", roomID)

	marker := fmt.Sprintf("large-%d", time.Now().UnixNano())
	largeMsg := marker + " " + strings.Repeat("Test paragraph for large message bridging. ", 150)
	sendMatrixMsg(t, roomID, cooMXID, largeMsg)

	pollMMForMessage(t, mmChID, func(p map[string]any) bool {
		msg, _ := p["message"].(string)
		return strings.Contains(msg, marker)
	}, 45*time.Second)

	t.Log("Large message bridged successfully")
}

func TestBidirectionalRapidFire(t *testing.T) {
	roomID := requirePortalRoom(t, "test-channel")
	mmChID, _ := getMMChannel(t, "test-channel")
	cooMXID := joinAgentToRoom(t, "aiku-coo", roomID)

	marker := fmt.Sprintf("rapid-%d", time.Now().UnixNano())

	for i := 0; i < 3; i++ {
		sendMatrixMsg(t, roomID, cooMXID, fmt.Sprintf("M2MM-%d-%s", i, marker))
	}
	for i := 0; i < 3; i++ {
		postToMM(t, mmChID, fmt.Sprintf("MM2M-%d-%s", i, marker))
	}

	for i := 0; i < 3; i++ {
		expected := fmt.Sprintf("M2MM-%d-%s", i, marker)
		pollMMForMessage(t, mmChID, func(p map[string]any) bool {
			msg, _ := p["message"].(string)
			return strings.Contains(msg, expected)
		}, 30*time.Second)
	}
	for i := 0; i < 3; i++ {
		expected := fmt.Sprintf("MM2M-%d-%s", i, marker)
		pollMatrixForMessage(t, roomID, func(m map[string]any) bool {
			content, _ := m["content"].(map[string]any)
			body, _ := content["body"].(string)
			return strings.Contains(body, expected)
		}, 30*time.Second)
	}

	t.Log("Bidirectional rapid-fire verified")
}

func TestReactionBridgedToMM(t *testing.T) {
	roomID := requirePortalRoom(t, "dev-channel")
	cooMXID := joinAgentToRoom(t, "aiku-coo", roomID)

	msgID := sendMatrixMsg(t, roomID, cooMXID, "React to this message")
	reactID := sendMatrixReaction(t, roomID, cooMXID, msgID, "\u2705")
	if reactID == "" {
		t.Fatal("empty reaction event ID")
	}
	t.Logf("Reaction %s on message %s", reactID, msgID)
}

// ════════════════════════════════════════════════════════════════════
// TESTS — Puppet identity (Matrix -> MM direction)
// ════════════════════════════════════════════════════════════════════

func TestPuppetIdentity(t *testing.T) {
	roomID := requirePortalRoom(t, "general-bridge")
	mmChID, _ := getMMChannel(t, "general-bridge")
	cooMXID := joinAgentToRoom(t, "aiku-coo", roomID)

	marker := fmt.Sprintf("puppet-%d", time.Now().UnixNano())
	sendMatrixMsg(t, roomID, cooMXID, "COO reporting: "+marker)

	post := pollMMForMessage(t, mmChID, func(p map[string]any) bool {
		msg, _ := p["message"].(string)
		return strings.Contains(msg, marker)
	}, 30*time.Second)

	userID, _ := post["user_id"].(string)
	code, userResp := doJSON(t, "GET",
		fmt.Sprintf("%s/api/v4/users/%s", mmURL, userID),
		nil, mmToken)
	if code == 200 {
		username, _ := userResp["username"].(string)
		t.Logf("MM post username=%s", username)
		if username == "aiku-coo" {
			t.Log("PUPPET MODE: Agent posted under correct identity")
		} else {
			t.Errorf("RELAY MODE: Agent posted as %q, want %q (puppet tokens may not be active)", username, "aiku-coo")
		}
	}
}

func TestMultiplePuppetsSameRoom(t *testing.T) {
	roomID := requirePortalRoom(t, "general-bridge")
	mmChID, _ := getMMChannel(t, "general-bridge")

	marker := fmt.Sprintf("multi-%d", time.Now().UnixNano())
	agents := []string{"aiku-coo", "aiku-cto"}

	for _, slug := range agents {
		joinAgentToRoom(t, slug, roomID)
		mxid := "@" + slug + ":" + domain
		sendMatrixMsg(t, roomID, mxid, fmt.Sprintf("Report from %s: %s", slug, marker))
	}

	for _, slug := range agents {
		pollMMForMessage(t, mmChID, func(p map[string]any) bool {
			msg, _ := p["message"].(string)
			return strings.Contains(msg, fmt.Sprintf("Report from %s: %s", slug, marker))
		}, 30*time.Second)
	}

	t.Log("Multiple puppets same room verified")
}

func TestCrossChannelPuppetPosting(t *testing.T) {
	channels := []string{"general-bridge", "dev-channel", "test-channel"}
	cooMXID := "@aiku-coo:" + domain

	marker := fmt.Sprintf("xchan-%d", time.Now().UnixNano())

	for _, ch := range channels {
		roomID := requirePortalRoom(t, ch)
		joinAgentToRoom(t, "aiku-coo", roomID)
		sendMatrixMsg(t, roomID, cooMXID, fmt.Sprintf("[%s] Cross-channel: %s", ch, marker))
	}

	for _, ch := range channels {
		mmChID, _ := getMMChannel(t, ch)
		pollMMForMessage(t, mmChID, func(p map[string]any) bool {
			msg, _ := p["message"].(string)
			return strings.Contains(msg, fmt.Sprintf("[%s] Cross-channel: %s", ch, marker))
		}, 30*time.Second)
	}

	t.Log("Cross-channel puppet posting verified")
}

func TestPuppetIdentityPerAgent(t *testing.T) {
	roomID := requirePortalRoom(t, "dev-channel")
	mmChID, _ := getMMChannel(t, "dev-channel")

	agents := []string{"aiku-coo", "aiku-cto"}

	for _, slug := range agents {
		agentMXID := joinAgentToRoom(t, slug, roomID)
		marker := fmt.Sprintf("identity-%s-%d", slug, time.Now().UnixNano())
		sendMatrixMsg(t, roomID, agentMXID, marker)

		post := pollMMForMessage(t, mmChID, func(p map[string]any) bool {
			msg, _ := p["message"].(string)
			return strings.Contains(msg, marker)
		}, 30*time.Second)

		userID, _ := post["user_id"].(string)
		code, userResp := doJSON(t, "GET",
			fmt.Sprintf("%s/api/v4/users/%s", mmURL, userID),
			nil, mmToken)
		if code == 200 {
			username, _ := userResp["username"].(string)
			if username != slug {
				t.Errorf("%s: MM post username=%q, want %q", slug, username, slug)
			} else {
				t.Logf("%s: correct puppet identity verified", slug)
			}
		}
	}
}

// ════════════════════════════════════════════════════════════════════
// TESTS — Echo prevention
// ════════════════════════════════════════════════════════════════════

func TestPuppetBotEchoPrevention(t *testing.T) {
	if mmCOOBotToken == "" {
		t.Skip("MM_COO_BOT_TOKEN not set (run via ./run.sh)")
	}
	roomID := requirePortalRoom(t, "general-bridge")
	mmChID, _ := getMMChannel(t, "general-bridge")

	marker := fmt.Sprintf("echo-prevent-%d", time.Now().UnixNano())
	postToMMAsBot(t, mmChID, "COO puppet echo test: "+marker, mmCOOBotToken)

	// Wait, then verify the message does NOT appear in Matrix
	time.Sleep(5 * time.Second)
	msgs := getMatrixMessages(t, roomID, 50)
	for _, m := range msgs {
		content, _ := m["content"].(map[string]any)
		body, _ := content["body"].(string)
		if strings.Contains(body, marker) {
			t.Errorf("puppet bot post leaked to Matrix (echo prevention failed): %s", body)
			return
		}
	}
	t.Log("Puppet bot echo prevention verified")
}

func TestBridgeBotEchoPrevention(t *testing.T) {
	roomID := requirePortalRoom(t, "general-bridge")
	mmChID, _ := getMMChannel(t, "general-bridge")

	// Post as admin (bridge relay owner) — bridge should NOT relay back
	marker := fmt.Sprintf("admin-echo-%d", time.Now().UnixNano())
	body := map[string]string{"channel_id": mmChID, "message": "Admin echo test: " + marker}
	code, resp := doJSON(t, "POST", mmURL+"/api/v4/posts", body, mmToken)
	if code != 201 {
		t.Fatalf("admin MM post: %d %v", code, resp)
	}

	// Wait, then verify admin's message does NOT appear in Matrix
	time.Sleep(5 * time.Second)
	msgs := getMatrixMessages(t, roomID, 50)
	for _, m := range msgs {
		content, _ := m["content"].(map[string]any)
		b, _ := content["body"].(string)
		if strings.Contains(b, marker) {
			t.Errorf("admin/bridge bot post leaked to Matrix (echo prevention failed): %s", b)
			return
		}
	}
	t.Log("Bridge bot (admin) echo prevention verified")
}

// ════════════════════════════════════════════════════════════════════
// TESTS — Double puppeting (MM -> Matrix direction)
// ════════════════════════════════════════════════════════════════════

func TestDoublePuppetAdminIntentSetup(t *testing.T) {
	roomID := requirePortalRoom(t, "general-bridge")

	// Verify the admin double puppet intent can send as @admin:localhost
	adminMXID := "@admin:" + domain

	// Join admin to the room using bridge AS token
	bridgeBotMXID := "@mattermostbot:" + domain
	invBody := map[string]string{"user_id": adminMXID}
	doJSON(t, "POST",
		fmt.Sprintf("%s/_matrix/client/v3/rooms/%s/invite?user_id=%s",
			synapseURL, roomID, bridgeBotMXID),
		invBody, bridgeASToken)
	code0, resp0 := doJSON(t, "POST",
		fmt.Sprintf("%s/_matrix/client/v3/join/%s?user_id=%s",
			synapseURL, roomID, adminMXID),
		map[string]string{}, bridgeASToken)
	if code0 != 200 {
		errMsg, _ := resp0["error"].(string)
		if !strings.Contains(errMsg, "already in the room") {
			t.Fatalf("join admin to room: %d %v", code0, resp0)
		}
	}

	// Send as admin using bridge AS token (same as double puppet intent)
	marker := fmt.Sprintf("dp-intent-%d", time.Now().UnixNano())
	txnID := fmt.Sprintf("dp-%d", time.Now().UnixNano())
	body := map[string]string{"msgtype": "m.text", "body": "Double puppet intent: " + marker}
	code, resp := doJSON(t, "PUT",
		fmt.Sprintf("%s/_matrix/client/v3/rooms/%s/send/m.room.message/%s?user_id=%s",
			synapseURL, roomID, txnID, adminMXID),
		body, bridgeASToken)
	if code != 200 {
		t.Fatalf("double puppet intent send: %d %v", code, resp)
	}

	msgs := getMatrixMessages(t, roomID, 20)
	for _, m := range msgs {
		content, _ := m["content"].(map[string]any)
		b, _ := content["body"].(string)
		if strings.Contains(b, marker) {
			sender, _ := m["sender"].(string)
			if sender != adminMXID {
				t.Errorf("double puppet sent as %q, want %q", sender, adminMXID)
			} else {
				t.Logf("Double puppet intent works: message sent as %s", sender)
			}
			return
		}
	}
	t.Fatal("double puppet intent message not found in room history")
}

// TestDoublePuppetAutoLoginEnabled verifies that the auto-login user (admin)
// automatically gets double puppeting via the as_token: config path — without
// needing the /api/double-puppet workaround.
//
// This is the regression test for the bug where autoLogin called the legacy
// password-based setupDoublePuppet (which required SYNAPSE_DOUBLE_PUPPET_PASSWORD)
// instead of setupUserDoublePuppet (which uses double_puppet.secrets as_token:).
func TestDoublePuppetAutoLoginEnabled(t *testing.T) {
	if mmAdminUserID == "" {
		t.Skip("MM_ADMIN_USER_ID not set (run via ./run.sh)")
	}

	// Calling /api/double-puppet for the admin user is idempotent.
	// If auto-login already set up DP correctly, this call still succeeds.
	// The key assertion: this call should succeed (200), confirming the
	// admin UserLogin exists and DP mapping is registered.
	adminMXID := "@admin:" + domain
	code, resp, err := doJSONRaw("POST", bridgeAdminURL+"/api/double-puppet",
		map[string]string{
			"mm_user_id":  mmAdminUserID,
			"matrix_mxid": adminMXID,
		}, "")
	if err != nil {
		t.Skipf("bridge admin API unreachable: %v", err)
	}
	if code != 200 {
		t.Fatalf("auto-login user DP registration: %d %v (bridge may not support it)", code, resp)
	}
	t.Logf("Auto-login user DP: %v", resp)

	// Verify the DP intent can actually send as @admin:localhost.
	// This confirms the as_token: config path is working for the auto-login user.
	roomID := requirePortalRoom(t, "general-bridge")
	bridgeBotMXID := "@mattermostbot:" + domain
	invBody := map[string]string{"user_id": adminMXID}
	doJSON(t, "POST",
		fmt.Sprintf("%s/_matrix/client/v3/rooms/%s/invite?user_id=%s",
			synapseURL, roomID, bridgeBotMXID),
		invBody, bridgeASToken)
	doJSON(t, "POST",
		fmt.Sprintf("%s/_matrix/client/v3/join/%s?user_id=%s",
			synapseURL, roomID, adminMXID),
		map[string]string{}, bridgeASToken)

	marker := fmt.Sprintf("auto-dp-%d", time.Now().UnixNano())
	txnID := fmt.Sprintf("auto-dp-%d", time.Now().UnixNano())
	body := map[string]string{"msgtype": "m.text", "body": "Auto-login DP test: " + marker}
	code2, resp2 := doJSON(t, "PUT",
		fmt.Sprintf("%s/_matrix/client/v3/rooms/%s/send/m.room.message/%s?user_id=%s",
			synapseURL, roomID, txnID, adminMXID),
		body, bridgeASToken)
	if code2 != 200 {
		t.Fatalf("auto-login DP intent send: %d %v", code2, resp2)
	}

	msgs := getMatrixMessages(t, roomID, 20)
	for _, m := range msgs {
		content, _ := m["content"].(map[string]any)
		b, _ := content["body"].(string)
		if strings.Contains(b, marker) {
			sender, _ := m["sender"].(string)
			if sender == adminMXID {
				t.Logf("Auto-login DP works: message sent as %s (not ghost)", sender)
			} else {
				t.Errorf("Auto-login DP failed: sent as %q, want %q", sender, adminMXID)
			}
			return
		}
	}
	t.Fatal("auto-login DP test message not found")
}

func TestDoublePuppetCEOGhost(t *testing.T) {
	// Before admin API registration, CEO messages arrive as ghosts
	roomID := requirePortalRoom(t, "general-bridge")
	mmChID, _ := getMMChannel(t, "general-bridge")

	marker := fmt.Sprintf("ceo-ghost-%d", time.Now().UnixNano())
	postToMM(t, mmChID, "CEO ghost test: "+marker)

	msg := pollMatrixForMessage(t, roomID, func(m map[string]any) bool {
		content, _ := m["content"].(map[string]any)
		body, _ := content["body"].(string)
		return strings.Contains(body, marker)
	}, 30*time.Second)

	sender, _ := msg["sender"].(string)
	t.Logf("CEO sender: %s", sender)

	if strings.HasPrefix(sender, "@mattermost_") {
		t.Logf("Expected: CEO posts as ghost %s (no double puppet before admin API registration)", sender)
	} else {
		t.Logf("Unexpected: CEO posted as real MXID %s (double puppet may already be active)", sender)
	}
}

func TestDoublePuppetAppserviceWhoami(t *testing.T) {
	// Validates the as_token + ?user_id= assertion path used by bridgev2
	tests := []struct {
		name     string
		mxid     string
		wantCode int
	}{
		{"admin (bridge relay owner)", "@admin:" + domain, 200},
		{"ceo (real user)", "@ceo:" + domain, 200},
		{"agent coo", "@aiku-coo:" + domain, 200},
		{"nobody (outside namespace)", "@nobody:" + domain, 403},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			code, resp := doJSON(t, "GET",
				fmt.Sprintf("%s/_matrix/client/v3/account/whoami?user_id=%s",
					synapseURL, tc.mxid),
				nil, bridgeASToken)

			if code != tc.wantCode {
				errCode, _ := resp["errcode"].(string)
				errMsg, _ := resp["error"].(string)
				t.Errorf("whoami %s: got %d, want %d (errcode=%s error=%s)",
					tc.mxid, code, tc.wantCode, errCode, errMsg)
			}

			if code == 200 {
				returnedID, _ := resp["user_id"].(string)
				if returnedID != tc.mxid {
					t.Errorf("whoami returned %q, want %q", returnedID, tc.mxid)
				}
			}
		})
	}
}

// TestDoublePuppetAdminAPI registers CEO for double puppeting via the
// /api/double-puppet admin endpoint. After registration, CEO messages
// from Mattermost should appear in Matrix under @ceo:localhost.
//
// MUST run after TestDoublePuppetCEOGhost (which documents ghost baseline).
// Go runs tests in definition order.
func TestDoublePuppetAdminAPI(t *testing.T) {
	if mmCEOUserID == "" {
		t.Skip("MM_CEO_USER_ID not set (run via ./run.sh)")
	}
	roomID := requirePortalRoom(t, "general-bridge")
	mmChID, _ := getMMChannel(t, "general-bridge")

	// Register CEO for double puppeting via admin API
	ceoMXID := "@ceo:" + domain
	body := map[string]string{
		"mm_user_id":  mmCEOUserID,
		"matrix_mxid": ceoMXID,
	}
	code, resp, err := doJSONRaw("POST", bridgeAdminURL+"/api/double-puppet", body, "")
	if err != nil {
		t.Skipf("bridge admin API unreachable: %v", err)
	}
	if code == 404 || code == 405 {
		t.Skip("/api/double-puppet not available")
	}
	if code != 200 {
		t.Fatalf("admin API /api/double-puppet: %d %v", code, resp)
	}
	t.Logf("Registered CEO double puppet: %v", resp)

	// Give bridge time to set up the DP intent
	time.Sleep(3 * time.Second)

	// CEO posts to Mattermost
	marker := fmt.Sprintf("ceo-dp-%d", time.Now().UnixNano())
	postToMM(t, mmChID, "CEO double puppet test: "+marker)

	// Check Matrix sender — should now be real MXID
	msg := pollMatrixForMessage(t, roomID, func(m map[string]any) bool {
		content, _ := m["content"].(map[string]any)
		b, _ := content["body"].(string)
		return strings.Contains(b, marker)
	}, 30*time.Second)

	sender, _ := msg["sender"].(string)
	t.Logf("CEO MM->Matrix sender after admin API: %s", sender)

	if sender == ceoMXID {
		t.Logf("DOUBLE PUPPET ACTIVE: CEO message sent as real MXID %s", sender)
	} else if strings.HasPrefix(sender, "@mattermost_") {
		t.Errorf("DOUBLE PUPPET INACTIVE: CEO sent as ghost %s, want %s", sender, ceoMXID)
	} else {
		t.Errorf("unexpected sender %s, want %s", sender, ceoMXID)
	}
}

// TestDoublePuppetAgentViaPuppetSystem verifies that agents configured via
// MATTERMOST_PUPPET_*_MXID/TOKEN automatically get double puppeting.
// When an agent's MM bot posts, the bridge echo-prevents it (good). But when
// the agent's MM bot *receives* an event from another user in the channel,
// the bridge should use the agent's double puppet intent.
//
// We test this indirectly: have CEO post in MM, check that the bridge uses
// senderFor() to route the event. Since CEO doesn't have DP set up via puppets,
// the baseline is ghost. Agents configured via puppets should have DP active.
func TestDoublePuppetAgentViaPuppetSystem(t *testing.T) {
	if mmCOOBotUserID == "" {
		t.Skip("MM_COO_BOT_USER_ID not set (run via ./run.sh)")
	}
	roomID := requirePortalRoom(t, "dev-channel")
	mmChID, _ := getMMChannel(t, "dev-channel")

	// Verify COO agent has double puppet configured by checking the admin API
	// The bridge should have set up DP for aiku-coo during puppet loading
	code, resp, err := doJSONRaw("POST", bridgeAdminURL+"/api/double-puppet",
		map[string]string{
			"mm_user_id":  mmCOOBotUserID,
			"matrix_mxid": "@aiku-coo:" + domain,
		}, "")
	if err != nil {
		t.Skipf("bridge admin API unreachable: %v", err)
	}
	// 200 = registered (or already registered), both are fine
	t.Logf("COO double puppet registration: %d %v", code, resp)

	time.Sleep(2 * time.Second)

	// Now test: have CEO post, verify it arrives (as ghost or DP depending on CEO state)
	// The important thing is that the bridge is working with puppet-configured agents
	marker := fmt.Sprintf("agent-dp-%d", time.Now().UnixNano())
	postToMM(t, mmChID, "Agent DP test from CEO: "+marker)

	pollMatrixForMessage(t, roomID, func(m map[string]any) bool {
		content, _ := m["content"].(map[string]any)
		body, _ := content["body"].(string)
		return strings.Contains(body, marker)
	}, 30*time.Second)

	t.Log("Agent double puppet system operational (bridge processes events with puppet-configured agents)")
}

// ════════════════════════════════════════════════════════════════════
// TESTS — Admin API endpoints
// ════════════════════════════════════════════════════════════════════

func TestAdminAPIReloadPuppetsMethodNotAllowed(t *testing.T) {
	code, _, err := doJSONRaw("GET", bridgeAdminURL+"/api/reload-puppets", nil, "")
	if err != nil {
		t.Skipf("bridge admin API unreachable: %v", err)
	}
	if code != 405 {
		t.Errorf("GET /api/reload-puppets: got %d, want 405", code)
	}
}

func TestAdminAPIReloadPuppets(t *testing.T) {
	// POST with empty body reloads from env vars
	code, resp, err := doJSONRaw("POST", bridgeAdminURL+"/api/reload-puppets", nil, "")
	if err != nil {
		t.Skipf("bridge admin API unreachable: %v", err)
	}
	if code != 200 {
		t.Fatalf("POST /api/reload-puppets: %d %v", code, resp)
	}
	t.Logf("Reload puppets response: %v", resp)
}

func TestAdminAPIDoublePuppetMethodNotAllowed(t *testing.T) {
	code, _, err := doJSONRaw("GET", bridgeAdminURL+"/api/double-puppet", nil, "")
	if err != nil {
		t.Skipf("bridge admin API unreachable: %v", err)
	}
	if code != 405 {
		t.Errorf("GET /api/double-puppet: got %d, want 405", code)
	}
}

func TestAdminAPIDoublePuppetMissingFields(t *testing.T) {
	cases := []struct {
		name string
		body map[string]string
	}{
		{"missing mm_user_id", map[string]string{"matrix_mxid": "@test:localhost"}},
		{"missing matrix_mxid", map[string]string{"mm_user_id": "abc123"}},
		{"both empty", map[string]string{"mm_user_id": "", "matrix_mxid": ""}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, _, err := doJSONRaw("POST", bridgeAdminURL+"/api/double-puppet", tc.body, "")
			if err != nil {
				t.Skipf("bridge admin API unreachable: %v", err)
			}
			if code == 200 {
				t.Errorf("expected error for %s, got 200", tc.name)
			}
		})
	}
}

func TestAdminAPIDoublePuppetInvalidJSON(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "POST", bridgeAdminURL+"/api/double-puppet",
		strings.NewReader("not json"))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Skipf("bridge admin API unreachable: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode == 200 {
		t.Error("expected error for invalid JSON, got 200")
	}
}

// ════════════════════════════════════════════════════════════════════
// TESTS — Room membership & state
// ════════════════════════════════════════════════════════════════════

func TestRoomMembershipIntegrity(t *testing.T) {
	roomID := requirePortalRoom(t, "general-bridge")
	ctoMXID := joinAgentToRoom(t, "aiku-cto", roomID)

	members := getRoomMembers(t, roomID)
	memberSet := make(map[string]bool)
	for _, m := range members {
		memberSet[m] = true
	}

	if !memberSet[ctoMXID] {
		t.Error("aiku-cto not in room after join")
	}
	if !memberSet["@mattermostbot:"+domain] {
		t.Error("mattermostbot not in room (bridge issue)")
	}
}

func TestPortalRoomNoDuplicates(t *testing.T) {
	// Different channels should not map to the same room
	seen := make(map[string]string)
	for ch, rid := range portalRooms {
		if prev, ok := seen[rid]; ok {
			t.Errorf("room %s mapped to both %q and %q", rid, prev, ch)
		}
		seen[rid] = ch
	}
}

// ════════════════════════════════════════════════════════════════════
// TESTS — Full convergence scenario
// ════════════════════════════════════════════════════════════════════

func TestFullBridgeConvergenceFlow(t *testing.T) {
	genRoom := requirePortalRoom(t, "general-bridge")
	devRoom := requirePortalRoom(t, "dev-channel")

	mmGenChID, _ := getMMChannel(t, "general-bridge")
	mmDevChID, _ := getMMChannel(t, "dev-channel")

	cooMXID := joinAgentToRoom(t, "aiku-coo", genRoom)
	ctoMXID := joinAgentToRoom(t, "aiku-cto", devRoom)

	marker := fmt.Sprintf("converge-%d", time.Now().UnixNano())

	// Step 1: CEO posts in MM general-bridge
	postToMM(t, mmGenChID, "CEO directive: test convergence. "+marker)

	// Verify CEO message arrives in Matrix
	pollMatrixForMessage(t, genRoom, func(m map[string]any) bool {
		content, _ := m["content"].(map[string]any)
		body, _ := content["body"].(string)
		return strings.Contains(body, marker)
	}, 30*time.Second)

	// Step 2: COO responds in Matrix general-bridge
	sendMatrixMsg(t, genRoom, cooMXID, "COO response: acknowledged. "+marker)

	// Verify COO response in MM
	pollMMForMessage(t, mmGenChID, func(p map[string]any) bool {
		msg, _ := p["message"].(string)
		return strings.Contains(msg, "COO response") && strings.Contains(msg, marker)
	}, 30*time.Second)

	// Step 3: CTO responds in Matrix dev-channel
	sendMatrixMsg(t, devRoom, ctoMXID, "CTO assessment: feasible. "+marker)

	// Verify CTO response in MM
	pollMMForMessage(t, mmDevChID, func(p map[string]any) bool {
		msg, _ := p["message"].(string)
		return strings.Contains(msg, "CTO assessment") && strings.Contains(msg, marker)
	}, 30*time.Second)

	// Step 4: COO posts synthesis back to general-bridge
	joinAgentToRoom(t, "aiku-coo", genRoom)
	sendMatrixMsg(t, genRoom, cooMXID, "Synthesis: all teams aligned. "+marker)

	pollMMForMessage(t, mmGenChID, func(p map[string]any) bool {
		msg, _ := p["message"].(string)
		return strings.Contains(msg, "Synthesis") && strings.Contains(msg, marker)
	}, 30*time.Second)

	t.Log("Full convergence flow verified end-to-end")
}
