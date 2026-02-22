// Copyright 2024-2026 Aiku AI

package matrixfmt_test

import (
	"fmt"

	"maunium.net/go/mautrix/event"

	"github.com/aiku/mautrix-mattermost/pkg/connector/matrixfmt"
)

func ExampleParse() {
	content := &event.MessageEventContent{
		MsgType:       event.MsgText,
		Body:          "hello world",
		Format:        event.FormatHTML,
		FormattedBody: "<strong>hello</strong> <em>world</em>",
	}

	md := matrixfmt.Parse(content)
	fmt.Println(md)
	// Output: **hello** _world_
}
