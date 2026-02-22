// Copyright 2024-2026 Aiku AI

package connector

import (
	"testing"

	"maunium.net/go/mautrix/event"
)

func TestMatrixfmtParse(t *testing.T) {
	t.Parallel()
	content := &event.MessageEventContent{
		MsgType: event.MsgText,
		Body:    "Hello world",
	}
	result := matrixfmtParse(content)
	if result != "Hello world" {
		t.Errorf("matrixfmtParse plain text: got %q, want %q", result, "Hello world")
	}
}

func TestMatrixfmtParse_Formatted(t *testing.T) {
	t.Parallel()
	content := &event.MessageEventContent{
		MsgType:       event.MsgText,
		Body:          "bold text",
		Format:        event.FormatHTML,
		FormattedBody: "<strong>bold</strong> text",
	}
	result := matrixfmtParse(content)
	if result == "" {
		t.Error("matrixfmtParse should return non-empty for formatted content")
	}
}

func TestMattermostfmtParse(t *testing.T) {
	t.Parallel()
	result := mattermostfmtParse("**bold** text")
	if result == nil {
		t.Fatal("mattermostfmtParse should not return nil")
	}
	if result.Body == "" {
		t.Error("parsed Body should not be empty")
	}
}
