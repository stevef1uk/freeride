//go:build ignore

// One-off dev helper (imports gastown). Excluded from go test ./... via build tag.

package main

import (
	"fmt"
	"github.com/steveyegge/gastown/internal/session"
)

func main() {
	id, err := session.ParseSessionName("te-testgt2-architect")
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}
	fmt.Printf("Role: %s, Rig: %s, Name: %s, Prefix: %s\n", id.Role, id.Rig, id.Name, id.Prefix)
}
