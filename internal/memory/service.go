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
	ScanInterval time.Duration
	Now          func() time.Time
	OnError      func(error)

	mu      sync.Mutex
	running bool
	trigger chan struct{}
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
		s.MinIdle = 10 * time.Minute
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
		watermark, watermarkErr := s.Store.ExtractionWatermark(ctx, session.ID)
		if watermarkErr != nil {
			return watermarkErr
		}
		extractionMessages := messages
		if watermark > 0 && watermark < len(messages) {
			extractionMessages = messages[watermark:]
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
		}(session, extractionMessages, candidate)
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
	previous, err := s.Store.ActiveSnapshot(ctx)
	if err != nil {
		_ = s.Store.ReleaseConsolidation(ctx, claim)
		return err
	}
	inputs, err := s.Store.ListConsolidationInputs(ctx, 200, time.Now())
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
	if ctx == nil {
		ctx = context.Background()
	}
	workerCtx, cancel := context.WithCancel(ctx)
	interval := s.ScanInterval
	if interval <= 0 {
		interval = 30 * time.Second
	}
	idleDelay := s.MinIdle
	if idleDelay <= 0 {
		idleDelay = 10 * time.Minute
	}
	s.mu.Lock()
	trigger := make(chan struct{}, 1)
	s.trigger = trigger
	s.mu.Unlock()
	go func() {
		defer func() {
			s.mu.Lock()
			if s.trigger == trigger {
				s.trigger = nil
			}
			s.mu.Unlock()
		}()
		run := func() {
			if err := s.RunOnce(workerCtx); err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, ErrLeaseHeld) && s.OnError != nil {
				s.OnError(err)
			}
		}
		run()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		idleTimer := time.NewTimer(idleDelay)
		defer idleTimer.Stop()
		for {
			select {
			case <-workerCtx.Done():
				return
			case <-trigger:
				if !idleTimer.Stop() {
					select {
					case <-idleTimer.C:
					default:
					}
				}
				idleTimer.Reset(idleDelay)
			case <-idleTimer.C:
				run()
				idleTimer.Reset(idleDelay)
			case <-ticker.C:
				run()
			}
		}
	}()
	return cancel
}

// NotifyTurnComplete schedules an exact idle-boundary check without blocking
// the foreground turn. The periodic scanner remains a crash-recovery fallback.
func (s *Service) NotifyTurnComplete(_ string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	trigger := s.trigger
	s.mu.Unlock()
	if trigger != nil {
		select {
		case trigger <- struct{}{}:
		default:
		}
	}
}

func (s *Service) extractOne(ctx context.Context, session conversation.SessionMetadata, messages []conversation.StoredMessage, candidate ExtractionCandidate) error {
	claim, err := s.Store.ClaimExtraction(ctx, candidate, s.OwnerID, s.LeaseTTL)
	if err != nil {
		return err
	}
	raw, err := s.Extractor.Extract(ctx, ExtractRequest{SessionID: session.ID, Workspace: session.Workspace, SourceVersion: candidate.SourceVersion, TranscriptHash: candidate.TranscriptHash, Messages: messages})
	if err != nil {
		if failErr := s.Store.FailExtraction(ctx, claim, err.Error(), time.Now().Add(time.Hour)); failErr != nil {
			return errors.Join(err, failErr)
		}
		return err
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
