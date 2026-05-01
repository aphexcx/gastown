package session

import "testing"

func TestBuildAgentArgv_AutoInjectsChannelsWhenEnabled(t *testing.T) {
	tests := []struct {
		name string
		cmd  string
		args []string
		opts SlackChannelOpts
		want []string
	}{
		{
			name: "enabled+claude",
			cmd:  "claude",
			args: []string{"--model", "sonnet[1m]"},
			opts: SlackChannelOpts{Enabled: true},
			want: []string{"claude", "--model", "sonnet[1m]", "--channels", "plugin:gt-slack@gastown"},
		},
		{
			name: "disabled",
			cmd:  "claude",
			args: []string{"--model", "sonnet[1m]"},
			opts: SlackChannelOpts{Enabled: false},
			want: []string{"claude", "--model", "sonnet[1m]"},
		},
		{
			name: "enabled+codex (non-claude provider)",
			cmd:  "codex",
			args: []string{"--config", "x"},
			opts: SlackChannelOpts{Enabled: true},
			want: []string{"codex", "--config", "x"},
		},
		{
			name: "claude with no extra args",
			cmd:  "claude",
			args: nil,
			opts: SlackChannelOpts{Enabled: true},
			want: []string{"claude", "--channels", "plugin:gt-slack@gastown"},
		},
		{
			name: "non-claude with no args",
			cmd:  "gemini",
			args: nil,
			opts: SlackChannelOpts{Enabled: true},
			want: []string{"gemini"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := BuildAgentArgv(tt.cmd, tt.args, tt.opts)
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("argv[%d] = %q, want %q (full got=%v)", i, got[i], tt.want[i], got)
				}
			}
		})
	}
}

func TestBuildAgentArgv_DoesNotMutateCallerArgs(t *testing.T) {
	args := []string{"--model", "sonnet"}
	original := make([]string, len(args))
	copy(original, args)
	_ = BuildAgentArgv("claude", args, SlackChannelOpts{Enabled: true})
	for i := range args {
		if args[i] != original[i] {
			t.Fatalf("BuildAgentArgv mutated caller args at %d: got %q, want %q", i, args[i], original[i])
		}
	}
}

func TestInjectChannelsArgs(t *testing.T) {
	tests := []struct {
		name string
		cmd  string
		args []string
		opts SlackChannelOpts
		want []string
	}{
		{
			name: "enabled+claude appends",
			cmd:  "claude",
			args: []string{"--model", "sonnet"},
			opts: SlackChannelOpts{Enabled: true},
			want: []string{"--model", "sonnet", "--channels", "plugin:gt-slack@gastown"},
		},
		{
			name: "disabled returns copy",
			cmd:  "claude",
			args: []string{"--model", "sonnet"},
			opts: SlackChannelOpts{Enabled: false},
			want: []string{"--model", "sonnet"},
		},
		{
			name: "non-claude returns copy",
			cmd:  "codex",
			args: []string{"-c"},
			opts: SlackChannelOpts{Enabled: true},
			want: []string{"-c"},
		},
		{
			name: "nil args, claude+enabled",
			cmd:  "claude",
			args: nil,
			opts: SlackChannelOpts{Enabled: true},
			want: []string{"--channels", "plugin:gt-slack@gastown"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := InjectChannelsArgs(tt.cmd, tt.args, tt.opts)
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("argv[%d] = %q, want %q (full got=%v)", i, got[i], tt.want[i], got)
				}
			}
		})
	}
}
