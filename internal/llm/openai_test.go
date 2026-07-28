package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOpenAIRequestIncludesThinkingEffort(t *testing.T) {
	var payload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = writer.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"))
	}))
	defer server.Close()
	client, err := newOpenAiCompatClient(&ModelParm{
		APIKey: "key", BaseURL: server.URL, ModelName: "model", ThinkingEffort: ThinkingEffortHigh,
	})
	if err != nil {
		t.Fatal(err)
	}
	events, errs := client.Stream(&StreamRequest{Context: context.Background()})
	for range events {
	}
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if payload["reasoning_effort"] != string(ThinkingEffortHigh) {
		t.Fatalf("reasoning_effort = %#v, payload = %#v", payload["reasoning_effort"], payload)
	}
}

func TestOpenAIRequestMapsQwenThinkingBudget(t *testing.T) {
	var payload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = writer.Write([]byte("data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"))
	}))
	defer server.Close()
	client, err := newOpenAiCompatClient(&ModelParm{
		APIKey: "key", BaseURL: server.URL, ModelName: "model", EnableThinking: true,
		ThinkingEffort: ThinkingEffortLow, ThinkingBudget: 6000,
	})
	if err != nil {
		t.Fatal(err)
	}
	events, errs := client.Stream(&StreamRequest{Context: context.Background()})
	for range events {
	}
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if payload["enable_thinking"] != true || payload["thinking_budget"] != float64(6000) {
		t.Fatalf("payload = %#v", payload)
	}
	if _, ok := payload["reasoning_effort"]; ok {
		t.Fatalf("qwen payload unexpectedly has reasoning_effort: %#v", payload)
	}
}
