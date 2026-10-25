package opencode

import "testing"

// TestResolveChatURL — the chat endpoint is a path of the catalog host, not the
// catalog path itself.
//
// The default is checked against the address the reference client uses. The
// other cases are the ones a mirror can produce: an unchanged path, a whole URL
// on another host, and a path that already carries the mirror's prefix.
func TestResolveChatURL(t *testing.T) {
	cases := []struct {
		name string
		base string
		path string
		want string
	}{
		{
			name: "default",
			base: DefaultBaseURL,
			path: DefaultChatPath,
			want: "https://opencode.ai/inference/openai/v1/chat/completions",
		},
		{
			name: "empty path uses the default",
			base: "https://mirror.test/v1",
			path: "",
			want: "https://mirror.test/inference/openai/v1/chat/completions",
		},
		{
			name: "absolute url",
			base: "https://opencode.ai/inference/v1",
			path: "https://chat.test/v1/chat/completions",
			want: "https://chat.test/v1/chat/completions",
		},
		{
			name: "path with the mirror prefix",
			base: "https://mirror.test/prefix/v1",
			path: "/prefix/chat/completions",
			want: "https://mirror.test/prefix/chat/completions",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := resolveChatURL(c.base, c.path); got != c.want {
				t.Fatalf("resolveChatURL(%q, %q) = %q, want %q", c.base, c.path, got, c.want)
			}
		})
	}
}
