package session

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestFetchWriterStatusReturnPaths(t *testing.T) {
	tests := []struct {
		name    string
		output  string
		runErr  error
		wantErr string
		wantOK  bool
	}{
		{name: "command error", runErr: errors.New("ssh disconnected"), wantErr: "writer-status command failed"},
		{name: "empty output", output: " \n\t", wantErr: "returned no output"},
		{name: "non-object output", output: "[]", wantErr: "did not return a JSON object"},
		{name: "corrupt JSON", output: "{broken", wantErr: "returned corrupt JSON"},
		{name: "decoded status", output: `{"running":true,"detail":"healthy"}`, wantOK: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			runner := &SSHRunner{runFn: func(context.Context, ...string) ([]byte, error) {
				return []byte(tc.output), tc.runErr
			}}
			status, err := runner.FetchWriterStatus(context.Background())
			if tc.wantOK {
				if err != nil || !status.Running || status.Detail != "healthy" {
					t.Fatalf("status=%+v err=%v", status, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err=%v, want containing %q", err, tc.wantErr)
			}
			if status != (WriterStatus{}) {
				t.Fatalf("failure returned non-zero status: %+v", status)
			}
		})
	}
}
