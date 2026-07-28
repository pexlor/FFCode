package contextmanager

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"MyCode/internal/hook"
)

type compactHookStore struct {
	fakeConversationStore
	committed SummarySnapshot
	commits   int
	onCommit  func()
}

func (s *compactHookStore) CommitSummary(_ context.Context, snapshot SummarySnapshot, _ int) error {
	s.committed = snapshot
	s.commits++
	if s.onCommit != nil {
		s.onCommit()
	}
	return nil
}

type compactHookSummarizer struct {
	calls int
}

func (s *compactHookSummarizer) Summarize(_ context.Context, _ SummarizeRequest) (SummarizeResponse, error) {
	s.calls++
	return SummarizeResponse{Content: "summary"}, nil
}

func TestCompactorDispatchesHooksAroundCommittedSummary(t *testing.T) {
	dispatcher := hook.New(hook.DefaultConfig())
	var order []string
	if err := dispatcher.Register(hook.EventPreCompact, func(_ context.Context, input hook.Input) (hook.Output, error) {
		order = append(order, "pre")
		if input.SessionID != "session-1" || input.Metadata["previous_summary_version"] != 2 {
			t.Fatalf("pre input = %+v", input)
		}
		return hook.Output{}, nil
	}); err != nil {
		t.Fatal(err)
	}
	store := &compactHookStore{}
	summarizer := &compactHookSummarizer{}
	if err := dispatcher.Register(hook.EventPostCompact, func(_ context.Context, input hook.Input) (hook.Output, error) {
		order = append(order, "post")
		if store.commits != 1 {
			t.Fatalf("post ran before commit")
		}
		if input.Metadata["summary_version"] != 3 || input.Metadata["changed"] != true {
			t.Fatalf("post input = %+v", input)
		}
		return hook.Output{}, nil
	}); err != nil {
		t.Fatal(err)
	}
	compactor := ConversationCompactor{
		Store: store, Primary: summarizer, Estimator: ConservativeEstimator{}, Model: "test",
		Policy: ContextPolicy{RecentCompleteTurns: 0, MinCompactionIncrementTokens: 1}, Hooks: dispatcher,
	}
	active := SummarySnapshot{Version: 2, SessionID: "session-1"}
	snapshot, changed, err := compactor.Compact(context.Background(), "session-1", active, compactMessages(), 1000)
	if err != nil {
		t.Fatal(err)
	}
	if !changed || snapshot.Version != 3 || summarizer.calls != 1 || store.commits != 1 {
		t.Fatalf("snapshot=%+v changed=%v calls=%d commits=%d", snapshot, changed, summarizer.calls, store.commits)
	}
	if !reflect.DeepEqual(order, []string{"pre", "post"}) {
		t.Fatalf("hook order = %#v", order)
	}
}

func TestPreCompactDenialPreventsSummarizationAndCommit(t *testing.T) {
	dispatcher := hook.New(hook.DefaultConfig())
	if err := dispatcher.Register(hook.EventPreCompact, func(hook.Input) hook.Output {
		return hook.Output{Decision: hook.DecisionDeny, Reason: "keep full history"}
	}); err != nil {
		t.Fatal(err)
	}
	store := &compactHookStore{}
	summarizer := &compactHookSummarizer{}
	compactor := ConversationCompactor{
		Store: store, Primary: summarizer, Estimator: ConservativeEstimator{}, Model: "test",
		Policy: ContextPolicy{RecentCompleteTurns: 0, MinCompactionIncrementTokens: 1}, Hooks: dispatcher,
	}
	_, changed, err := compactor.Compact(context.Background(), "session-1", SummarySnapshot{}, compactMessages(), 1000)
	if !errors.Is(err, ErrCompactHookRejected) || changed || summarizer.calls != 0 || store.commits != 0 {
		t.Fatalf("changed=%v err=%v calls=%d commits=%d", changed, err, summarizer.calls, store.commits)
	}
}

func TestPostCompactRunsAfterCommitContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	store := &compactHookStore{onCommit: cancel}
	dispatcher := hook.New(hook.DefaultConfig())
	postCalls := 0
	if err := dispatcher.Register(hook.EventPostCompact, func(ctx context.Context, _ hook.Input) hook.Output {
		postCalls++
		if ctx.Err() != nil {
			t.Errorf("post hook context remained canceled: %v", ctx.Err())
		}
		return hook.Output{}
	}); err != nil {
		t.Fatal(err)
	}
	compactor := ConversationCompactor{
		Store: store, Primary: &compactHookSummarizer{}, Estimator: ConservativeEstimator{}, Model: "test",
		Policy: ContextPolicy{RecentCompleteTurns: 0, MinCompactionIncrementTokens: 1}, Hooks: dispatcher,
	}

	_, changed, err := compactor.Compact(ctx, "session-1", SummarySnapshot{}, compactMessages(), 1000)
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	if postCalls != 1 {
		t.Fatalf("post hook calls = %d, want 1", postCalls)
	}
}

func compactMessages() []StoredMessage {
	return []StoredMessage{{
		ID: "message-1", SessionID: "session-1", TurnID: "turn-1", Role: "user",
		Content: "content to compact", TurnStatus: TurnComplete,
	}}
}
