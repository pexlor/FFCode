package llm

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"MyCode/internal/conversation"
)

func TestAnthropicStreamReturnsStructuredProviderError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Retry-After", "2")
		writer.WriteHeader(529)
		_, _ = writer.Write([]byte(`{"type":"error","error":{"type":"overloaded_error"}}`))
	}))
	defer server.Close()
	client, err := newAnthropicClient(&ModelParm{APIKey: "key", BaseURL: server.URL, ModelName: "model"})
	if err != nil {
		t.Fatal(err)
	}

	_, errorsChannel := client.Stream(&StreamRequest{Context: context.Background()})
	got := <-errorsChannel
	var providerErr *ProviderError
	if !errors.As(got, &providerErr) {
		t.Fatalf("error = %T %v", got, got)
	}
	if providerErr.StatusCode != 529 || !providerErr.Retryable || providerErr.RetryAfter != 2*time.Second {
		t.Fatalf("provider error = %+v", providerErr)
	}
}

func TestAnthropicMalformedToolInputIsRetryable(t *testing.T) {
	state := anthropicStreamState{blocks: make(map[int]*anthropicBlockState)}
	start := anthropicSSEEvent{Type: "content_block_start", Index: 0}
	start.ContentBlock.Type = "tool_use"
	start.ContentBlock.ID = "tool-1"
	start.ContentBlock.Name = "ReadFile"
	if _, _, err := state.consume(start); err != nil {
		t.Fatalf("consume start: %v", err)
	}

	delta := anthropicSSEEvent{Type: "content_block_delta", Index: 0}
	delta.Delta.Type = "input_json_delta"
	delta.Delta.PartialJSON = `{"path":`
	if _, _, err := state.consume(delta); err != nil {
		t.Fatalf("consume delta: %v", err)
	}

	_, _, err := state.consume(anthropicSSEEvent{Type: "content_block_stop", Index: 0})
	if !errors.Is(err, ErrMalformedToolInput) {
		t.Fatalf("expected ErrMalformedToolInput, got %v", err)
	}
}

func TestAnthropicRequestPreservesThinkingSignature(t *testing.T) {
	request, err := buildAnthropicRequest(&StreamRequest{
		Context: context.Background(),
		Messages: []conversation.Message{{
			Role: conversation.ASSISTANT,
			ThinkingBlocks: []conversation.ThinkingBlock{{
				Thinking:  "reasoning",
				Signature: "signed",
			}},
			ToolUses: []conversation.ToolUseBlock{{ToolUseID: "tool-1", ToolName: "read"}},
		}},
	}, &ModelParm{ModelName: "test"})
	if err != nil {
		t.Fatal(err)
	}
	thinking := request.Messages[0].Content[0]
	if thinking.Thinking != "reasoning" || thinking.Signature != "signed" {
		t.Fatalf("thinking block = %#v", thinking)
	}
}
