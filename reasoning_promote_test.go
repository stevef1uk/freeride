package main

import "testing"

func TestPromoteReasoningContent(t *testing.T) {
	tests := []struct {
		name         string
		message      map[string]interface{}
		wantPromoted bool
		wantContent  string
	}{
		{
			name:         "empty message unchanged",
			message:      map[string]interface{}{},
			wantPromoted: false,
		},
		{
			name:         "existing content is left alone",
			message:      map[string]interface{}{"content": "CMD: ls", "reasoning": "CMD: ignored"},
			wantPromoted: false,
			wantContent:  "CMD: ls",
		},
		{
			name:         "cmd glued in reasoning is promoted",
			message:      map[string]interface{}{"content": "", "reasoning": "Let's run commands.CMD: cd rig && bd list --status=open"},
			wantPromoted: true,
			wantContent:  "CMD: cd rig && bd list --status=open",
		},
		{
			name:         "reasoning_content key supported",
			message:      map[string]interface{}{"content": "", "reasoning_content": "thinking...\nCMD: go test ./..."},
			wantPromoted: true,
			wantContent:  "CMD: go test ./...",
		},
		{
			name:         "last marker wins",
			message:      map[string]interface{}{"content": "", "reasoning": "first CMD: a\nsecond CMD: b"},
			wantPromoted: true,
			wantContent:  "second CMD: b",
		},
		{
			name:         "multi-line write block preserved",
			message:      map[string]interface{}{"content": "", "reasoning": "now\nWRITE: foo.go\npackage main\nfunc main(){}\nEND WRITE"},
			wantPromoted: true,
			wantContent:  "WRITE: foo.go\npackage main\nfunc main(){}\nEND WRITE",
		},
		{
			name:         "json outcome promoted",
			message:      map[string]interface{}{"content": "", "reasoning": "done.{\"outcome\":\"success\",\"summary\":\"ok\"}"},
			wantPromoted: true,
			wantContent:  "{\"outcome\":\"success\",\"summary\":\"ok\"}",
		},
		{
			name:         "reasoning without action marker unchanged",
			message:      map[string]interface{}{"content": "", "reasoning": "just thinking out loud, no actions here"},
			wantPromoted: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := promoteReasoningContent(tt.message)
			if got != tt.wantPromoted {
				t.Fatalf("promoted = %v, want %v", got, tt.wantPromoted)
			}
			if tt.wantContent != "" {
				if c, _ := tt.message["content"].(string); c != tt.wantContent {
					t.Fatalf("content = %q, want %q", c, tt.wantContent)
				}
			}
		})
	}
}
