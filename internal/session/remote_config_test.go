package session

import "testing"

func TestRemoteConfigGetKindNormalizesKnownKinds(t *testing.T) {
	tests := []struct {
		name string
		kind string
		want string
	}{
		{name: "agentbox mixed case", kind: "AgentBox", want: RemoteKindAgentbox},
		{name: "ssh mixed case", kind: "SSH", want: RemoteKindSSH},
		{name: "url defaults to agentbox", want: RemoteKindAgentbox},
		{name: "host defaults to ssh", want: RemoteKindSSH},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rc := RemoteConfig{Kind: tt.kind}
			if tt.name == "url defaults to agentbox" {
				rc.URL = "https://agentbox.example"
			}
			if tt.name == "host defaults to ssh" {
				rc.Host = "root@agentbox"
			}
			if got := rc.GetKind(); got != tt.want {
				t.Fatalf("GetKind() = %q, want %q", got, tt.want)
			}
		})
	}
}
