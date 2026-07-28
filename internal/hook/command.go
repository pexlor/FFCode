package hook

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

type commandHandler struct {
	spec CommandSpec
}

// NewCommandHandler creates an external command handler. The dispatcher still
// applies its own timeout and output limits; values on spec narrow those limits
// for this command.
func NewCommandHandler(spec CommandSpec) Handler {
	return &commandHandler{spec: cloneCommandSpec(spec)}
}

func (h *commandHandler) Name() string {
	if h == nil {
		return "command"
	}
	return h.spec.Command
}

func (h *commandHandler) Handle(ctx context.Context, input Input) (Output, error) {
	if h == nil || strings.TrimSpace(h.spec.Command) == "" {
		return Output{}, errors.New("hook command is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return Output{}, ErrHookTimeout
		}
		return Output{}, err
	}
	command := commandContext(ctx, h.spec)
	directory := strings.TrimSpace(h.spec.Dir)
	if directory == "" {
		directory = strings.TrimSpace(h.spec.WorkingDirectory)
	}
	if directory != "" {
		if !filepath.IsAbs(directory) && strings.TrimSpace(input.Workspace) != "" {
			directory = filepath.Join(input.Workspace, directory)
		}
		command.Dir = directory
	} else if strings.TrimSpace(input.Workspace) != "" {
		command.Dir = input.Workspace
	}
	command.Env = commandEnvironment(h.spec.Env, input)
	payload, err := json.Marshal(input)
	if err != nil {
		return Output{}, fmt.Errorf("encode hook input: %w", err)
	}
	command.Stdin = bytes.NewReader(append(payload, '\n'))

	stdout, err := command.StdoutPipe()
	if err != nil {
		return Output{}, fmt.Errorf("create hook stdout pipe: %w", err)
	}
	stderr, err := command.StderrPipe()
	if err != nil {
		return Output{}, fmt.Errorf("create hook stderr pipe: %w", err)
	}
	if err := command.Start(); err != nil {
		return Output{}, fmt.Errorf("start hook command %q: %w", h.spec.Command, err)
	}

	limit := h.spec.MaxOutputBytes
	if limit <= 0 {
		limit = DefaultMaxOutputBytes
	}
	capture := newLimitedCapture(limit)
	copyDone := make(chan error, 2)
	go func() { _, copyErr := io.Copy(capture.stdoutWriter(), stdout); copyDone <- copyErr }()
	go func() { _, copyErr := io.Copy(capture.stderrWriter(), stderr); copyDone <- copyErr }()
	waitDone := make(chan error, 1)
	go func() { waitDone <- command.Wait() }()

	var waitErr error
	select {
	case waitErr = <-waitDone:
	case <-capture.exceeded:
		killCommand(command)
		waitErr = <-waitDone
		for range 2 {
			<-copyDone
		}
		stdoutText, stderrText, _ := capture.result()
		output, parseErr := parseCommandOutput(stdoutText)
		if output.Reason == "" {
			output.Reason = strings.TrimSpace(stderrText)
		}
		output.Truncated = true
		return output, errors.Join(ErrOutputLimit, parseErr)
	case <-ctx.Done():
		killCommand(command)
		waitErr = <-waitDone
		for range 2 {
			<-copyDone
		}
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return Output{}, ErrHookTimeout
		}
		return Output{}, ctx.Err()
	}
	for range 2 {
		<-copyDone
	}
	stdoutText, stderrText, truncated := capture.result()
	output, parseErr := parseCommandOutput(stdoutText)
	if truncated {
		output.Truncated = true
		if output.Reason == "" {
			output.Reason = strings.TrimSpace(stderrText)
		}
		return output, errors.Join(ErrOutputLimit, parseErr)
	}
	if waitErr != nil {
		diagnostic := strings.TrimSpace(stderrText)
		if diagnostic == "" {
			diagnostic = strings.TrimSpace(stdoutText)
		}
		if diagnostic != "" {
			return output, errors.Join(fmt.Errorf("hook command %q failed: %w: %s", h.spec.Command, waitErr, diagnostic), parseErr)
		}
		return output, errors.Join(fmt.Errorf("hook command %q failed: %w", h.spec.Command, waitErr), parseErr)
	}
	if output.Reason == "" && strings.TrimSpace(stderrText) != "" {
		output.Reason = strings.TrimSpace(stderrText)
	}
	return output, parseErr
}

func commandContext(_ context.Context, spec CommandSpec) *exec.Cmd {
	command := strings.TrimSpace(spec.Command)
	var result *exec.Cmd
	if spec.Shell || (len(spec.Args) == 0 && commandNeedsShell(command)) {
		result = exec.Command("/bin/sh", "-c", command)
	} else {
		result = exec.Command(command, spec.Args...)
	}
	configureCommand(result)
	return result
}

func commandNeedsShell(command string) bool {
	return strings.ContainsAny(command, " \t\n|&;<>()$`\\*?[]{}~")
}

func commandEnvironment(extra map[string]string, input Input) []string {
	environment := append([]string(nil), os.Environ()...)
	for key, value := range extra {
		environment = replaceEnvironmentValue(environment, key, value)
	}
	reserved := [][2]string{
		{"MYCODE_HOOK_EVENT", string(input.Event)},
		{"MYCODE_HOOK_DEPTH", strconv.Itoa(input.Depth)},
		{"MYCODE_HOOK_SESSION_ID", input.SessionID},
		{"MYCODE_HOOK_TOOL_NAME", input.ToolName},
		{"MYCODE_HOOK_TOOL_USE_ID", input.ToolUseID},
		{"MYCODE_WORKSPACE", input.Workspace},
		// FFCODE aliases keep hook scripts portable across the binary and
		// repository names used by this project.
		{"FFCODE_HOOK_EVENT", string(input.Event)},
		{"FFCODE_HOOK_DEPTH", strconv.Itoa(input.Depth)},
	}
	for _, entry := range reserved {
		environment = replaceEnvironmentValue(environment, entry[0], entry[1])
	}
	return environment
}

func replaceEnvironmentValue(environment []string, key, value string) []string {
	prefix := key + "="
	kept := environment[:0]
	for _, entry := range environment {
		if strings.HasPrefix(entry, prefix) {
			continue
		}
		kept = append(kept, entry)
	}
	return append(kept, prefix+value)
}

func parseCommandOutput(stdout string) (Output, error) {
	trimmed := strings.TrimSpace(stdout)
	if trimmed == "" {
		return Output{}, nil
	}
	if trimmed[0] != '{' {
		return Output{Output: trimmed}, nil
	}
	if !json.Valid([]byte(trimmed)) {
		return Output{Output: trimmed}, fmt.Errorf("%w: malformed command JSON object", ErrInvalidOutput)
	}
	var output Output
	decoder := json.NewDecoder(strings.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&output); err != nil {
		return Output{Output: trimmed}, fmt.Errorf("%w: decode command JSON: %v", ErrInvalidOutput, err)
	}
	return output, nil
}

type limitedCapture struct {
	mu        sync.Mutex
	remaining int
	stdout    bytes.Buffer
	stderr    bytes.Buffer
	truncated bool
	exceeded  chan struct{}
	once      sync.Once
}

func newLimitedCapture(limit int) *limitedCapture {
	return &limitedCapture{remaining: limit, exceeded: make(chan struct{})}
}

func (c *limitedCapture) stdoutWriter() io.Writer { return captureWriter{capture: c, stderr: false} }

func (c *limitedCapture) stderrWriter() io.Writer { return captureWriter{capture: c, stderr: true} }

func (c *limitedCapture) result() (string, string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stdout.String(), c.stderr.String(), c.truncated
}

type captureWriter struct {
	capture *limitedCapture
	stderr  bool
}

func (w captureWriter) Write(data []byte) (int, error) {
	if w.capture == nil {
		return len(data), nil
	}
	w.capture.mu.Lock()
	defer w.capture.mu.Unlock()
	written := len(data)
	if w.capture.remaining <= 0 {
		w.capture.markExceeded()
		return written, ErrOutputLimit
	}
	portion := data
	if len(portion) > w.capture.remaining {
		portion = portion[:w.capture.remaining]
		w.capture.truncated = true
	}
	var err error
	if w.stderr {
		_, err = w.capture.stderr.Write(portion)
	} else {
		_, err = w.capture.stdout.Write(portion)
	}
	w.capture.remaining -= len(portion)
	if len(portion) < len(data) {
		w.capture.markExceeded()
		if err == nil {
			err = ErrOutputLimit
		}
	}
	return written, err
}

func (c *limitedCapture) markExceeded() {
	c.truncated = true
	c.once.Do(func() { close(c.exceeded) })
}
