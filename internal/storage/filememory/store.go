package filememory

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"MyCode/internal/conversation"
	"MyCode/internal/memory"
)

const formatVersion = 1

type manifest struct {
	FormatVersion         int       `json:"format_version"`
	ActiveSnapshotVersion int       `json:"active_snapshot_version"`
	InputWatermark        string    `json:"input_watermark,omitempty"`
	LastConsolidationAt   time.Time `json:"last_consolidation_at,omitempty"`
}

type extractionJob struct {
	Candidate   memory.ExtractionCandidate `json:"candidate"`
	Status      string                     `json:"status"`
	OwnerID     string                     `json:"owner_id,omitempty"`
	Token       string                     `json:"token,omitempty"`
	LeaseUntil  time.Time                  `json:"lease_until,omitempty"`
	RetryAt     time.Time                  `json:"retry_at,omitempty"`
	Failure     string                     `json:"failure,omitempty"`
	RawMemoryID string                     `json:"raw_memory_id,omitempty"`
}

type Store struct {
	root string
	mu   sync.Mutex
}

func New(root string) (*Store, error) {
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("memory store root is required")
	}
	for _, dir := range []string{root, filepath.Join(root, "sources"), filepath.Join(root, "jobs", "extraction"), filepath.Join(root, "leases"), filepath.Join(root, "snapshots"), filepath.Join(root, "quarantine")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("create memory store: %w", err)
		}
	}
	path := filepath.Join(root, "manifest.json")
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		if err := writeJSONAtomic(path, manifest{FormatVersion: formatVersion}); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	return &Store{root: root}, nil
}

func (s *Store) ClaimExtraction(ctx context.Context, candidate memory.ExtractionCandidate, owner string, ttl time.Duration) (memory.ExtractionClaim, error) {
	if err := ctx.Err(); err != nil {
		return memory.ExtractionClaim{}, err
	}
	if !conversation.ValidIdentifier(candidate.SessionID) || candidate.SourceVersion <= 0 || candidate.TranscriptHash == "" || owner == "" || ttl <= 0 {
		return memory.ExtractionClaim{}, memory.ErrInvalidMemory
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	path := s.extractionPath(candidate.SessionID)
	var job extractionJob
	if err := readJSON(path, &job); err == nil {
		if job.Candidate.TranscriptHash == candidate.TranscriptHash && job.Candidate.SourceVersion == candidate.SourceVersion && (job.Status == "succeeded" || job.Status == "succeeded_no_output") {
			return memory.ExtractionClaim{}, memory.ErrAlreadyProcessed
		}
		if job.Status == "running" && time.Now().Before(job.LeaseUntil) {
			return memory.ExtractionClaim{}, memory.ErrLeaseHeld
		}
		if !job.RetryAt.IsZero() && time.Now().Before(job.RetryAt) && job.Candidate.TranscriptHash == candidate.TranscriptHash {
			return memory.ExtractionClaim{}, memory.ErrLeaseHeld
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return memory.ExtractionClaim{}, err
	}
	token, err := randomToken()
	if err != nil {
		return memory.ExtractionClaim{}, err
	}
	claim := memory.ExtractionClaim{Candidate: candidate, OwnerID: owner, Token: token, ExpiresAt: time.Now().Add(ttl)}
	job = extractionJob{Candidate: candidate, Status: "running", OwnerID: owner, Token: token, LeaseUntil: claim.ExpiresAt}
	if err := writeJSONAtomic(path, job); err != nil {
		return memory.ExtractionClaim{}, err
	}
	return claim, nil
}

func (s *Store) CompleteExtraction(ctx context.Context, claim memory.ExtractionClaim, raw memory.RawMemory) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	path := s.extractionPath(claim.Candidate.SessionID)
	job, err := s.ownedExtraction(path, claim)
	if err != nil {
		return err
	}
	if raw.SessionID != claim.Candidate.SessionID || raw.TranscriptHash != claim.Candidate.TranscriptHash || raw.SourceVersion != claim.Candidate.SourceVersion {
		return memory.ErrInvalidMemory
	}
	if raw.ID == "" {
		job.Status = "succeeded_no_output"
		job.OwnerID, job.Token = "", ""
		return writeJSONAtomic(path, job)
	}
	if err := s.appendRawMemory(raw); err != nil {
		return err
	}
	if err := writeJSONAtomic(filepath.Join(s.root, "sources", raw.SessionID+".json"), raw); err != nil {
		return err
	}
	job.Status, job.RawMemoryID = "succeeded", raw.ID
	job.OwnerID, job.Token = "", ""
	return writeJSONAtomic(path, job)
}

func (s *Store) FailExtraction(ctx context.Context, claim memory.ExtractionClaim, cause string, retryAt time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	path := s.extractionPath(claim.Candidate.SessionID)
	job, err := s.ownedExtraction(path, claim)
	if err != nil {
		return err
	}
	job.Status, job.Failure, job.RetryAt = "failed", cause, retryAt
	job.OwnerID, job.Token = "", ""
	return writeJSONAtomic(path, job)
}

func (s *Store) ListConsolidationInputs(ctx context.Context, limit int, now time.Time) ([]memory.RawMemory, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	file, err := os.Open(filepath.Join(s.root, "raw_memories.jsonl"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var result []memory.RawMemory
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		var raw memory.RawMemory
		if err := json.Unmarshal(scanner.Bytes(), &raw); err != nil {
			return nil, fmt.Errorf("decode raw memories: %w", err)
		}
		result = append(result, raw)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].GeneratedAt.Equal(result[j].GeneratedAt) {
			return result[i].ID < result[j].ID
		}
		return result[i].GeneratedAt.After(result[j].GeneratedAt)
	})
	if limit > 0 && len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}

func (s *Store) ClaimConsolidation(ctx context.Context, owner string, ttl time.Duration) (memory.ConsolidationClaim, error) {
	if err := ctx.Err(); err != nil {
		return memory.ConsolidationClaim{}, err
	}
	if owner == "" || ttl <= 0 {
		return memory.ConsolidationClaim{}, memory.ErrInvalidMemory
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	path := s.consolidationLockPath()
	var existing memory.ConsolidationClaim
	if err := readJSON(path, &existing); err == nil {
		if time.Now().Before(existing.ExpiresAt) {
			return memory.ConsolidationClaim{}, memory.ErrLeaseHeld
		}
		_ = os.Rename(path, filepath.Join(s.root, "quarantine", fmt.Sprintf("%d-consolidation.lock", time.Now().UnixNano())))
	} else if !errors.Is(err, os.ErrNotExist) {
		return memory.ConsolidationClaim{}, err
	}
	token, err := randomToken()
	if err != nil {
		return memory.ConsolidationClaim{}, err
	}
	claim := memory.ConsolidationClaim{OwnerID: owner, Token: token, ExpiresAt: time.Now().Add(ttl)}
	data, err := json.MarshalIndent(claim, "", "  ")
	if err != nil {
		return memory.ConsolidationClaim{}, err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return memory.ConsolidationClaim{}, memory.ErrLeaseHeld
	}
	if err != nil {
		return memory.ConsolidationClaim{}, err
	}
	if _, err = file.Write(data); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		_ = os.Remove(path)
		return memory.ConsolidationClaim{}, err
	}
	if closeErr != nil {
		_ = os.Remove(path)
		return memory.ConsolidationClaim{}, closeErr
	}
	return claim, nil
}

func (s *Store) RenewConsolidation(ctx context.Context, claim memory.ConsolidationClaim, ttl time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.verifyConsolidation(claim); err != nil {
		return err
	}
	claim.ExpiresAt = time.Now().Add(ttl)
	return writeJSONAtomic(s.consolidationLockPath(), claim)
}

func (s *Store) ReleaseConsolidation(ctx context.Context, claim memory.ConsolidationClaim) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.verifyConsolidation(claim); err != nil {
		return err
	}
	return os.Remove(s.consolidationLockPath())
}

func (s *Store) CommitSnapshot(ctx context.Context, claim memory.ConsolidationClaim, expectedVersion int, snapshot memory.MemorySnapshot) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.verifyConsolidation(claim); err != nil {
		return err
	}
	var current manifest
	if err := readJSON(filepath.Join(s.root, "manifest.json"), &current); err != nil {
		return err
	}
	if current.FormatVersion != formatVersion || current.ActiveSnapshotVersion != expectedVersion || snapshot.Version != expectedVersion+1 {
		return memory.ErrVersionConflict
	}
	if snapshot.CreatedAt.IsZero() {
		snapshot.CreatedAt = time.Now()
	}
	snapshot.PreviousVersion = expectedVersion
	if err := writeJSONAtomic(filepath.Join(s.root, "snapshots", fmt.Sprintf("memory-%06d.json", snapshot.Version)), snapshot); err != nil {
		return err
	}
	detailed := snapshot.Detailed
	if detailed == "" {
		detailed = snapshot.Summary
	}
	if err := writeFileAtomic(filepath.Join(s.root, "MEMORY.md"), []byte(detailed)); err != nil {
		return err
	}
	if err := writeFileAtomic(filepath.Join(s.root, "memory_summary.md"), []byte(snapshot.Summary)); err != nil {
		return err
	}
	current.ActiveSnapshotVersion = snapshot.Version
	current.InputWatermark = snapshot.InputWatermark
	current.LastConsolidationAt = snapshot.CreatedAt
	if err := writeJSONAtomic(filepath.Join(s.root, "manifest.json"), current); err != nil {
		return err
	}
	return os.Remove(s.consolidationLockPath())
}

func (s *Store) ActiveSnapshot(ctx context.Context) (*memory.MemorySnapshot, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var current manifest
	if err := readJSON(filepath.Join(s.root, "manifest.json"), &current); err != nil {
		return nil, err
	}
	if current.ActiveSnapshotVersion == 0 {
		return nil, nil
	}
	var snapshot memory.MemorySnapshot
	if err := readJSON(filepath.Join(s.root, "snapshots", fmt.Sprintf("memory-%06d.json", current.ActiveSnapshotVersion)), &snapshot); err != nil {
		return nil, err
	}
	return &snapshot, nil
}

func (s *Store) Summary(ctx context.Context) (string, error) {
	snapshot, err := s.ActiveSnapshot(ctx)
	if err != nil || snapshot == nil {
		return "", err
	}
	return snapshot.Summary, nil
}

func (s *Store) extractionPath(sessionID string) string {
	return filepath.Join(s.root, "jobs", "extraction", sessionID+".json")
}

func (s *Store) consolidationLockPath() string {
	return filepath.Join(s.root, "leases", "consolidation.lock")
}

func (s *Store) ownedExtraction(path string, claim memory.ExtractionClaim) (extractionJob, error) {
	var job extractionJob
	if err := readJSON(path, &job); err != nil {
		return extractionJob{}, err
	}
	if job.Status != "running" || job.Token != claim.Token || job.OwnerID != claim.OwnerID || job.Candidate.TranscriptHash != claim.Candidate.TranscriptHash {
		return extractionJob{}, memory.ErrLeaseLost
	}
	return job, nil
}

func (s *Store) appendRawMemory(raw memory.RawMemory) error {
	path := filepath.Join(s.root, "raw_memories.jsonl")
	if existing, err := os.Open(path); err == nil {
		scanner := bufio.NewScanner(existing)
		for scanner.Scan() {
			var item memory.RawMemory
			if json.Unmarshal(scanner.Bytes(), &item) == nil && (item.ID == raw.ID || (item.SessionID == raw.SessionID && item.TranscriptHash == raw.TranscriptHash && item.PromptVersion == raw.PromptVersion)) {
				existing.Close()
				return nil
			}
		}
		if err := scanner.Err(); err != nil {
			existing.Close()
			return err
		}
		existing.Close()
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	data, err := json.Marshal(raw)
	if err == nil {
		_, err = file.Write(append(data, '\n'))
	}
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	return closeErr
}

func (s *Store) verifyConsolidation(claim memory.ConsolidationClaim) error {
	var current memory.ConsolidationClaim
	if err := readJSON(s.consolidationLockPath(), &current); err != nil {
		return memory.ErrLeaseLost
	}
	if current.Token != claim.Token || current.OwnerID != claim.OwnerID || time.Now().After(current.ExpiresAt) {
		return memory.ErrLeaseLost
	}
	return nil
}

func randomToken() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

func readJSON(path string, target any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("decode %s: %w", path, err)
	}
	return nil
}

func writeJSONAtomic(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, data)
}

func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	temporary, err := os.CreateTemp(dir, ".memory-*.tmp")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer os.Remove(name)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}
