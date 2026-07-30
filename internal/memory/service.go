package memory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"FFCode/internal/conversation"
)

type TranscriptSource interface {
	ListSessions(context.Context, string, int) ([]conversation.SessionMetadata, error)
	ListMessages(context.Context, string) ([]conversation.StoredMessage, error)
}

type Service struct {
	Store        Store
	Source       TranscriptSource
	Extractor    Extractor
	Consolidator Consolidator
	OwnerID      string
	Workspace    string
	MinIdle      time.Duration
	LeaseTTL     time.Duration
	MaxSessions  int
	Concurrency  int
	Now          func() time.Time

	mu      sync.Mutex
	running bool
}

func (s *Service) RunOnce(ctx context.Context) error {
	if s == nil || s.Store == nil || s.Source == nil || s.Extractor == nil {
		return errors.New("memory service dependencies are required")
	}
	now := time.Now()
	if s.Now != nil {
		now = s.Now()
	}
	if s.MinIdle <= 0 {
		s.MinIdle = 30 * time.Minute
	}
	if s.LeaseTTL <= 0 {
		s.LeaseTTL = time.Hour
	}
	if s.MaxSessions <= 0 {
		s.MaxSessions = 100
	}
	if s.Concurrency <= 0 {
		s.Concurrency = 2
	}
	if s.OwnerID == "" {
		s.OwnerID = "memory-worker"
	}
	sessions, err := s.Source.ListSessions(ctx, s.Workspace, s.MaxSessions)
	if err != nil {
		return err
	}
	sem := make(chan struct{}, s.Concurrency)
	var wait sync.WaitGroup
	var firstErr error
	var errMu sync.Mutex
	for _, session := range sessions {
		if session.UpdatedAt.After(now.Add(-s.MinIdle)) {
			continue
		}
		messages, listErr := s.Source.ListMessages(ctx, session.ID)
		if listErr != nil {
			errMu.Lock()
			if firstErr == nil {
				firstErr = listErr
			}
			errMu.Unlock()
			continue
		}
		if !completeTranscript(messages) {
			continue
		}
		candidate, hashErr := makeCandidate(session, messages)
		if hashErr != nil {
			return hashErr
		}
		sem <- struct{}{}
		wait.Add(1)
		go func(session conversation.SessionMetadata, messages []conversation.StoredMessage, candidate ExtractionCandidate) {
			defer wait.Done()
			defer func() { <-sem }()
			if runErr := s.extractOne(ctx, session, messages, candidate); runErr != nil && !errors.Is(runErr, ErrAlreadyProcessed) && !errors.Is(runErr, ErrLeaseHeld) {
				errMu.Lock()
				if firstErr == nil {
					firstErr = runErr
				}
				errMu.Unlock()
			}
		}(session, messages, candidate)
	}
	wait.Wait()
	if s.Consolidator != nil {
		if consolidateErr := s.RunConsolidationOnce(ctx); firstErr == nil {
			firstErr = consolidateErr
		}
	}
	return firstErr
}

func (s *Service) RunConsolidationOnce(ctx context.Context) error {
	if s.Consolidator == nil {
		return nil
	}
	claim, err := s.Store.ClaimConsolidation(ctx, s.OwnerID, time.Hour)
	if err != nil {
		return err
	}
	inputs, err := s.Store.ListConsolidationInputs(ctx, 200, time.Now())
	if err != nil {
		_ = s.Store.ReleaseConsolidation(ctx, claim)
		return err
	}
	previous, err := s.Store.ActiveSnapshot(ctx)
	if err != nil {
		_ = s.Store.ReleaseConsolidation(ctx, claim)
		return err
	}
	if len(inputs) == 0 {
		return s.Store.ReleaseConsolidation(ctx, claim)
	}
	snapshot, err := s.Consolidator.Consolidate(ctx, ConsolidateRequest{Previous: previous, Inputs: inputs})
	if err != nil {
		_ = s.Store.ReleaseConsolidation(ctx, claim)
		return err
	}
	expected := 0
	if previous != nil {
		expected = previous.Version
	}
	snapshot.Version = expected + 1
	return s.Store.CommitSnapshot(ctx, claim, expected, snapshot)
}

func (s *Service) Start(ctx context.Context) context.CancelFunc {
	workerCtx, cancel := context.WithCancel(ctx)
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-workerCtx.Done():
				return
			case <-ticker.C:
				_ = s.RunOnce(workerCtx)
			}
		}
	}()
	return cancel
}

func (s *Service) extractOne(ctx context.Context, session conversation.SessionMetadata, messages []conversation.StoredMessage, candidate ExtractionCandidate) error {
	claim, err := s.Store.ClaimExtraction(ctx, candidate, s.OwnerID, s.LeaseTTL)
	if err != nil {
		return err
	}
	raw, err := s.Extractor.Extract(ctx, ExtractRequest{SessionID: session.ID, Workspace: session.Workspace, SourceVersion: candidate.SourceVersion, TranscriptHash: candidate.TranscriptHash, Messages: messages})
	if err != nil {
		return s.Store.FailExtraction(ctx, claim, err.Error(), time.Now().Add(time.Hour))
	}
	if raw.ID != "" {
		if err := ValidateRawMemory(raw, messages); err != nil {
			_ = s.Store.FailExtraction(ctx, claim, err.Error(), time.Now().Add(time.Hour))
			return err
		}
	}
	return s.Store.CompleteExtraction(ctx, claim, raw)
}

func makeCandidate(session conversation.SessionMetadata, messages []conversation.StoredMessage) (ExtractionCandidate, error) {
	data, err := json.Marshal(messages)
	if err != nil {
		return ExtractionCandidate{}, err
	}
	digest := sha256.Sum256(data)
	return ExtractionCandidate{SessionID: session.ID, Workspace: session.Workspace, SourceVersion: len(messages), TranscriptHash: hex.EncodeToString(digest[:]), UpdatedAt: session.UpdatedAt}, nil
}

func completeTranscript(messages []conversation.StoredMessage) bool {
	return len(messages) > 0 && messages[len(messages)-1].TurnStatus == conversation.TurnComplete
}

func (s *Service) String() string {
	return fmt.Sprintf("memory service owner=%s workspace=%s", s.OwnerID, s.Workspace)
}
