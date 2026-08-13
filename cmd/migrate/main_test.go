package main

import (
	"context"
	"testing"
)

func TestRunRejectsInvalidArgsBeforeExternalAccess(t *testing.T) {
	tests := map[string]struct {
		args []string
		want string
	}{
		"no args": {
			want: "usage: mcpaste-migrate up|status|verify|down --steps 1",
		},
		"unknown command": {
			args: []string{"unknown"},
			want: "usage: mcpaste-migrate up|status|verify|down --steps 1",
		},
		"up extra": {
			args: []string{"up", "extra"},
			want: "usage: mcpaste-migrate up",
		},
		"status extra": {
			args: []string{"status", "extra"},
			want: "usage: mcpaste-migrate status",
		},
		"verify extra": {
			args: []string{"verify", "extra"},
			want: "usage: mcpaste-migrate verify",
		},
		"down missing args": {
			args: []string{"down"},
			want: "usage: mcpaste-migrate down --steps 1",
		},
		"down missing value": {
			args: []string{"down", "--steps"},
			want: "usage: mcpaste-migrate down --steps 1",
		},
		"down wrong flag": {
			args: []string{"down", "--step", "1"},
			want: "usage: mcpaste-migrate down --steps 1",
		},
		"down unsupported steps": {
			args: []string{"down", "--steps", "2"},
			want: "usage: mcpaste-migrate down --steps 1",
		},
		"down extra": {
			args: []string{"down", "--steps", "1", "extra"},
			want: "usage: mcpaste-migrate down --steps 1",
		},
	}
	for name, item := range tests {
		t.Run(name, func(t *testing.T) {
			err := run(context.Background(), item.args, func(string) string {
				t.Fatal("run called getenv for invalid arguments")
				return ""
			})
			if err == nil || err.Error() != item.want {
				t.Fatalf("run() error = %v, want %q", err, item.want)
			}
		})
	}
}
