// Copyright 2024-2026 Aiku AI

package mattermostfmt_test

import (
	"fmt"

	"github.com/aiku/mautrix-mattermost/pkg/connector/mattermostfmt"
)

func ExampleParse() {
	msg := mattermostfmt.Parse("**hello** world")
	fmt.Println(msg.FormattedBody)
	// Output: <strong>hello</strong> world
}
