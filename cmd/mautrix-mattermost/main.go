// Copyright 2024-2026 Remi Philippe
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Command mautrix-mattermost is a Matrix-Mattermost puppeting bridge built
// on the mautrix bridgev2 framework. It translates messages between the two
// platforms and supports per-user puppet identity routing so each Matrix user
// can post to Mattermost under a dedicated bot account.
package main

import (
	"github.com/aiku/mautrix-mattermost/pkg/connector"
	"maunium.net/go/mautrix/bridgev2/matrix/mxmain"
)

// These are filled at build time with -ldflags.
var (
	Tag       = "unknown"
	Commit    = "unknown"
	BuildTime = "unknown"
)

var m = mxmain.BridgeMain{
	Name:        "mautrix-mattermost",
	URL:         "https://github.com/aiku/mautrix-mattermost",
	Description: "A Matrix-Mattermost puppeting bridge",
	Version:     "0.1.0",

	Connector: &connector.MattermostConnector{},
}

func main() {
	m.Run()
}
