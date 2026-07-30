package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigValidate(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
		want string
	}{
		{name: "empty", cfg: Config{}, want: "no mcpServers"},
		{name: "empty name", cfg: Config{Servers: map[string]ServerConfig{"": {Command: "server"}}}, want: "name cannot be empty"},
		{name: "empty command", cfg: Config{Servers: map[string]ServerConfig{"demo": {}}}, want: "command cannot be empty"},
		{name: "valid", cfg: Config{Servers: map[string]ServerConfig{"demo": {Command: "server", Args: []string{"--stdio"}}}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
			if tt.want == "" {
				if err != nil {
					t.Fatalf("Validate() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Validate() error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestLoadConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp.yaml")
	if err := os.WriteFile(path, []byte("mcpServers:\n  demo:\n    command: server\n    args: [--stdio]\n    env:\n      TOKEN: value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	server := cfg.Servers["demo"]
	if server.Command != "server" || len(server.Args) != 1 || server.Env["TOKEN"] != "value" {
		t.Fatalf("loaded server = %+v", server)
	}
}

func TestToolResultText(t *testing.T) {
	result := ToolResult{Content: []Content{
		{Type: "text", Text: "hello"},
		{Type: "image", Data: "abc", MIMEType: "image/png"},
	}}
	text := result.Text()
	if !strings.Contains(text, "hello") || !strings.Contains(text, `"type":"image"`) {
		t.Fatalf("Text() = %q", text)
	}
}

func TestClientCorrelatesSuccessfulResponse(t *testing.T) {
	ch := make(chan response, 1)
	client := &Client{pending: map[int64]chan response{7: ch}}
	client.handleResponse(json.RawMessage(`{"jsonrpc":"2.0","id":7,"result":{"tools":[]}}`))

	got := <-ch
	if got.err != nil || string(got.result) != `{"tools":[]}` {
		t.Fatalf("response = %+v", got)
	}
	if len(client.pending) != 0 {
		t.Fatalf("pending requests were not removed: %#v", client.pending)
	}
}

func TestClientCorrelatesErrorResponse(t *testing.T) {
	ch := make(chan response, 1)
	client := &Client{pending: map[int64]chan response{9: ch}}
	client.handleResponse(json.RawMessage(`{"jsonrpc":"2.0","id":9,"error":{"code":-1,"message":"failed"}}`))

	got := <-ch
	if got.err == nil || !strings.Contains(got.err.Error(), "MCP error -1: failed") {
		t.Fatalf("response error = %v", got.err)
	}
}

func TestClientCancellationRemovesPendingRequest(t *testing.T) {
	client := &Client{writer: bufio.NewWriter(io.Discard), pending: make(map[int64]chan response)}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := client.request(ctx, "tools/list", nil, &struct{}{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("request error = %v, want context.Canceled", err)
	}
	if len(client.pending) != 0 {
		t.Fatalf("pending requests after cancellation = %#v", client.pending)
	}
}

func TestClientFailPendingNotifiesEveryRequest(t *testing.T) {
	first := make(chan response, 1)
	second := make(chan response, 1)
	client := &Client{pending: map[int64]chan response{1: first, 2: second}}
	want := errors.New("server exited")
	client.failPending(want)

	for index, ch := range []chan response{first, second} {
		got := <-ch
		if !errors.Is(got.err, want) {
			t.Fatalf("response %d error = %v", index, got.err)
		}
	}
	if !client.closed || len(client.pending) != 0 {
		t.Fatalf("client state after failure: closed=%t pending=%d", client.closed, len(client.pending))
	}
}
