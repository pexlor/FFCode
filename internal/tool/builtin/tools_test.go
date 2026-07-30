package builtin

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestReadFileRejectsNegativeRange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sample.txt")
	if err := os.WriteFile(path, []byte("one\ntwo\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		args map[string]any
	}{
		{name: "negative offset", args: map[string]any{"file_path": path, "offset": -1}},
		{name: "negative limit", args: map[string]any{"file_path": path, "limit": -1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := (&ReadFileTool{}).Execute(context.Background(), tt.args)
			if !result.IsError {
				t.Fatalf("expected input error, got %+v", result)
			}
		})
	}
}

func TestReadFileReturnsRequestedLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sample.txt")
	if err := os.WriteFile(path, []byte("one\ntwo\nthree\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result := (&ReadFileTool{}).Execute(context.Background(), map[string]any{
		"file_path": path,
		"offset":    1,
		"limit":     2,
	})
	if result.IsError || result.Output != "2\ttwo\n3\tthree" {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestWriteAndEditFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "sample.txt")
	writer := &WriteFileTool{}
	result := writer.Execute(context.Background(), map[string]any{"file_path": path, "content": "hello"})
	if result.IsError {
		t.Fatalf("write failed: %+v", result)
	}

	editor := &EditFileTool{}
	result = editor.Execute(context.Background(), map[string]any{
		"file_path":  path,
		"old_string": "hello",
		"new_string": "world",
	})
	if result.IsError {
		t.Fatalf("edit failed: %+v", result)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "world" {
		t.Fatalf("content = %q, want world", data)
	}
}

func TestEditFileRequiresUniqueMatchUnlessReplaceAll(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sample.txt")
	if err := os.WriteFile(path, []byte("x\nx\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	editor := &EditFileTool{}
	result := editor.Execute(context.Background(), map[string]any{
		"file_path":  path,
		"old_string": "x",
		"new_string": "y",
	})
	if !result.IsError || !strings.Contains(result.Output, "occurs 2 times") {
		t.Fatalf("expected ambiguous replacement error, got %+v", result)
	}
	result = editor.Execute(context.Background(), map[string]any{
		"file_path":   path,
		"old_string":  "x",
		"new_string":  "y",
		"replace_all": true,
	})
	if result.IsError {
		t.Fatalf("replace_all failed: %+v", result)
	}
}

func TestGlobAndGrepRespectResultLimits(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"a.go", "b.go", "c.txt"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("TODO\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	globResult := (&GlobTool{}).Execute(context.Background(), map[string]any{
		"pattern":     "*.go",
		"path":        root,
		"max_results": 1,
	})
	if globResult.IsError || !strings.Contains(globResult.Output, "Results truncated") {
		t.Fatalf("unexpected glob result: %+v", globResult)
	}
	grepResult := (&GrepTool{}).Execute(context.Background(), map[string]any{
		"pattern":     "TODO",
		"path":        root,
		"max_results": 1,
	})
	if grepResult.IsError || !strings.Contains(grepResult.Output, "Results truncated") {
		t.Fatalf("unexpected grep result: %+v", grepResult)
	}
}

func TestWriteFileDoesNotWriteAfterCancellation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sample.txt")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result := (&WriteFileTool{}).Execute(ctx, map[string]any{"file_path": path, "content": "content"})
	if !result.IsError {
		t.Fatalf("expected cancellation error, got %+v", result)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("file exists after canceled write: %v", err)
	}
}

func TestBashExecutesAndBoundsOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses bash syntax")
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is not installed")
	}
	tool := &BashTool{executable: bash, commandPrefix: []string{"-lc"}, maxOutputBytes: 4, defaultTimeout: time.Second}
	result := tool.Execute(context.Background(), map[string]any{"command": "printf 123456"})
	if result.IsError || !strings.Contains(result.Output, "output truncated after 4 bytes") {
		t.Fatalf("unexpected bounded output result: %+v", result)
	}
}

func TestBashReturnsTimeoutError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses bash syntax")
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is not installed")
	}
	tool := &BashTool{executable: bash, commandPrefix: []string{"-lc"}, maxOutputBytes: 1024, defaultTimeout: time.Second}
	result := tool.Execute(context.Background(), map[string]any{"command": "sleep 1", "timeout_ms": 10})
	if !result.IsError || !strings.Contains(result.Output, "timed out") {
		t.Fatalf("unexpected timeout result: %+v", result)
	}
}
