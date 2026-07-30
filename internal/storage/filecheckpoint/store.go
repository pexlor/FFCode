package filecheckpoint

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"sync"

	"FFCode/internal/agent"
)

var identifierPattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
var generationPattern = regexp.MustCompile(`^generation-([0-9]{20})\.json$`)

const generationsToKeep = 2

type Store struct {
	root string
	mu   sync.Mutex
}

type manifest struct {
	Generation uint64 `json:"generation"`
}

func New(root string) (*Store, error) {
	if root == "" {
		return nil, errors.New("checkpoint root is required")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("create checkpoint root: %w", err)
	}
	return &Store{root: root}, nil
}

func (s *Store) Load(ctx context.Context, sessionID string) (agent.RunCheckpoint, error) {
	if err := ctx.Err(); err != nil {
		return agent.RunCheckpoint{}, err
	}
	if !validIdentifier(sessionID) {
		return agent.RunCheckpoint{}, errors.New("invalid checkpoint session ID")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.load(sessionID)
}

func (s *Store) Save(ctx context.Context, checkpoint agent.RunCheckpoint) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !validIdentifier(checkpoint.SessionID) {
		return errors.New("invalid checkpoint session ID")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	dir := filepath.Join(s.root, checkpoint.SessionID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create checkpoint session directory: %w", err)
	}
	current, err := s.loadManifest(checkpoint.SessionID)
	if err != nil && !errors.Is(err, agent.ErrCheckpointNotFound) {
		return err
	}
	checkpoint.Generation = current.Generation + 1
	generationName := fmt.Sprintf("generation-%020d.json", checkpoint.Generation)
	if err := writeJSONAtomic(filepath.Join(dir, generationName), checkpoint); err != nil {
		return err
	}
	if err := writeJSONAtomic(filepath.Join(dir, "manifest.json"), manifest{Generation: checkpoint.Generation}); err != nil {
		return err
	}
	return pruneGenerations(dir, checkpoint.Generation)
}

func (s *Store) load(sessionID string) (agent.RunCheckpoint, error) {
	current, err := s.loadManifest(sessionID)
	if err != nil {
		return agent.RunCheckpoint{}, err
	}
	path := filepath.Join(s.root, sessionID, fmt.Sprintf("generation-%020d.json", current.Generation))
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return agent.RunCheckpoint{}, agent.ErrCheckpointNotFound
	}
	if err != nil {
		return agent.RunCheckpoint{}, fmt.Errorf("read checkpoint generation: %w", err)
	}
	var checkpoint agent.RunCheckpoint
	if err := json.Unmarshal(data, &checkpoint); err != nil {
		return agent.RunCheckpoint{}, fmt.Errorf("decode checkpoint generation: %w", err)
	}
	return checkpoint, nil
}

func (s *Store) loadManifest(sessionID string) (manifest, error) {
	data, err := os.ReadFile(filepath.Join(s.root, sessionID, "manifest.json"))
	if errors.Is(err, os.ErrNotExist) {
		return manifest{}, agent.ErrCheckpointNotFound
	}
	if err != nil {
		return manifest{}, fmt.Errorf("read checkpoint manifest: %w", err)
	}
	var current manifest
	if err := json.Unmarshal(data, &current); err != nil {
		return manifest{}, fmt.Errorf("decode checkpoint manifest: %w", err)
	}
	return current, nil
}

func writeJSONAtomic(path string, value any) error {
	dir := filepath.Dir(path)
	temporary, err := os.CreateTemp(dir, ".checkpoint-*.tmp")
	if err != nil {
		return fmt.Errorf("create checkpoint temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	encoder := json.NewEncoder(temporary)
	if err := encoder.Encode(value); err != nil {
		temporary.Close()
		return fmt.Errorf("encode checkpoint: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("publish checkpoint: %w", err)
	}
	directory, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func validIdentifier(value string) bool {
	return identifierPattern.MatchString(value) && value != "." && value != ".."
}

func pruneGenerations(dir string, current uint64) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		match := generationPattern.FindStringSubmatch(entry.Name())
		if len(match) != 2 {
			continue
		}
		generation, err := strconv.ParseUint(match[1], 10, 64)
		if err != nil || generation+generationsToKeep > current {
			continue
		}
		if err := os.Remove(filepath.Join(dir, entry.Name())); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove old checkpoint generation: %w", err)
		}
	}
	return nil
}
