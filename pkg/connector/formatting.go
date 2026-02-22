// Copyright 2024-2026 Aiku AI

package connector

import (
	"github.com/aiku/mautrix-mattermost/pkg/connector/matrixfmt"
	"github.com/aiku/mautrix-mattermost/pkg/connector/mattermostfmt"
	"maunium.net/go/mautrix/event"
)

// mattermostfmtParse converts Mattermost markdown to Matrix HTML message content.
func mattermostfmtParse(text string) *mattermostfmt.ParsedMessage {
	return mattermostfmt.Parse(text)
}

// matrixfmtParse converts Matrix message content to Mattermost markdown.
func matrixfmtParse(content *event.MessageEventContent) string {
	return matrixfmt.Parse(content)
}
