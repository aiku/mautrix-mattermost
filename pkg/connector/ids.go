// Copyright 2024-2026 Aiku AI

package connector

import (
	"strconv"

	"maunium.net/go/mautrix/bridgev2/networkid"
)

// MakePortalID creates a networkid.PortalID from a Mattermost channel ID.
func MakePortalID(channelID string) networkid.PortalID {
	return networkid.PortalID(channelID)
}

// ParsePortalID extracts the Mattermost channel ID from a PortalID.
func ParsePortalID(portalID networkid.PortalID) string {
	return string(portalID)
}

// MakeUserID creates a networkid.UserID from a Mattermost user ID.
func MakeUserID(userID string) networkid.UserID {
	return networkid.UserID(userID)
}

// ParseUserID extracts the Mattermost user ID from a networkid.UserID.
func ParseUserID(userID networkid.UserID) string {
	return string(userID)
}

// MakeMessageID creates a networkid.MessageID from a Mattermost post ID.
func MakeMessageID(postID string) networkid.MessageID {
	return networkid.MessageID(postID)
}

// ParseMessageID extracts the Mattermost post ID from a MessageID.
func ParseMessageID(messageID networkid.MessageID) string {
	return string(messageID)
}

// MakeMessagePartID creates a networkid.PartID for message parts (e.g., file attachments).
func MakeMessagePartID(index int) networkid.PartID {
	if index == 0 {
		return ""
	}
	return networkid.PartID(strconv.Itoa(index))
}

// MakeEmojiID creates a networkid.EmojiID from a Mattermost emoji name.
func MakeEmojiID(emojiName string) networkid.EmojiID {
	return networkid.EmojiID(emojiName)
}

// ParseEmojiID extracts the Mattermost emoji name from an EmojiID.
func ParseEmojiID(emojiID networkid.EmojiID) string {
	return string(emojiID)
}

// makePortalKey creates a networkid.PortalKey from a Mattermost channel ID.
func makePortalKey(channelID string) networkid.PortalKey {
	return networkid.PortalKey{
		ID: MakePortalID(channelID),
	}
}
