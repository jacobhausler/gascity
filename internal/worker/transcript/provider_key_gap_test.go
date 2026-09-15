package transcript

import (
	"testing"

	"github.com/gastownhall/gascity/internal/config"
)

func TestProviderConversationKeyGap(t *testing.T) {
	tests := []struct {
		name string
		rp   config.ResolvedProvider
		want bool
	}{
		{
			name: "claude assigns its own id",
			rp:   config.ResolvedProvider{BuiltinAncestor: "claude", SessionIDFlag: "--session-id", SupportsHooks: true},
			want: false,
		},
		{
			name: "codex has no flag but reports one through its hook",
			rp:   config.ResolvedProvider{BuiltinAncestor: "codex", SupportsHooks: true},
			want: false,
		},
		{
			name: "grok has neither route and its transcripts are keyed by id",
			rp:   config.ResolvedProvider{BuiltinAncestor: "grok", ResumeFlag: "--resume"},
			want: true,
		},
		{
			name: "a work-dir-keyed family needs no id to be found",
			rp:   config.ResolvedProvider{BuiltinAncestor: "zcode"},
			want: false,
		},
		{
			name: "opencode is both work-dir-keyed and hook-managed",
			rp:   config.ResolvedProvider{BuiltinAncestor: "opencode", SupportsHooks: true},
			want: false,
		},
		{
			// gc cannot assume a layout it has never seen, so it must not
			// claim a gap either (same restraint as HasKeyedTranscript).
			name: "fully custom provider is not judged",
			rp:   config.ResolvedProvider{Name: "in-house-agent", Command: "in-house-agent"},
			want: false,
		},
		{
			// Kind carries the provider's own name for a custom provider, so it
			// must never be read as a family: that would widen every custom
			// provider into the match.
			name: "legacy kind is not a family",
			rp:   config.ResolvedProvider{Name: "in-house", Kind: "in-house", ResumeFlag: "--resume"},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ProviderConversationKeyGap(tt.rp); got != tt.want {
				t.Errorf("ProviderConversationKeyGap() = %v, want %v", got, tt.want)
			}
		})
	}
}
