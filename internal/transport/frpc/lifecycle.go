package frpc

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/qase-tms/qase-tunnel/internal/translate"
)

type LineWriter interface {
	WriteLine(line string)
}

type FuncWriter func(string)

func (f FuncWriter) WriteLine(line string) { f(line) }

type Spawner func(ctx context.Context, binary, configPath string) *exec.Cmd

func DefaultSpawner(ctx context.Context, binary, configPath string) *exec.Cmd {
	return exec.CommandContext(ctx, binary, "-c", configPath)
}

type Options struct {
	Binary      string
	ConfigPath  string
	Spawner     Spawner
	Output      LineWriter
	Inputs      Inputs
	MaxRestarts int
	BaseBackoff time.Duration
}

type Lifecycle struct {
	opts Options

	mu      sync.Mutex
	cmd     *exec.Cmd
	pid     int
	exited  bool
	exitErr error
}

func New(opts Options) *Lifecycle {
	if opts.Spawner == nil {
		opts.Spawner = DefaultSpawner
	}
	if opts.MaxRestarts == 0 {
		opts.MaxRestarts = 3
	}
	if opts.BaseBackoff == 0 {
		opts.BaseBackoff = time.Second
	}
	return &Lifecycle{opts: opts}
}

func (l *Lifecycle) Run(ctx context.Context) error {
	restarts := 0
	for {
		err := l.runOnce(ctx)

		if ctx.Err() != nil {
			return nil
		}
		if err == nil {
			return nil
		}

		restarts++
		if restarts > l.opts.MaxRestarts {
			return fmt.Errorf("frpc exhausted %d restarts: %w", l.opts.MaxRestarts, err)
		}

		backoff := l.opts.BaseBackoff << (restarts - 1)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff):
		}
	}
}

func (l *Lifecycle) runOnce(ctx context.Context) error {
	cmd := l.opts.Spawner(ctx, l.opts.Binary, l.opts.ConfigPath)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start frpc: %w", err)
	}

	l.mu.Lock()
	l.cmd = cmd
	l.pid = cmd.Process.Pid
	l.mu.Unlock()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); l.streamLines(stdout) }()
	go func() { defer wg.Done(); l.streamLines(stderr) }()

	wg.Wait()
	waitErr := cmd.Wait()

	l.mu.Lock()
	l.exited = true
	l.exitErr = waitErr
	l.mu.Unlock()

	return waitErr
}

func (l *Lifecycle) streamLines(r io.Reader) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 64*1024)
	for scanner.Scan() {
		raw := scanner.Text()
		if isNoiseDebugLine(raw) {
			continue
		}
		safe := redactKnownSecrets(raw, l.opts.Inputs)

		t := translate.Translate(safe)
		if t.Translated() {
			l.write(t.FormatBanner())
		} else {
			l.write(safe)
		}
	}
}

func isNoiseDebugLine(line string) bool {
	return strings.Contains(line, "handle by plugin")
}

func (l *Lifecycle) write(line string) {
	if l.opts.Output != nil {
		l.opts.Output.WriteLine(line)
	}
}

func (l *Lifecycle) Pid() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.pid
}

func redactKnownSecrets(line string, in Inputs) string {
	out := line
	if in.Token != "" {
		out = replaceAll(out, in.Token, "[REDACTED-TOKEN]")
	}
	if in.Secret != "" {
		out = replaceAll(out, in.Secret, "[REDACTED-SECRET]")
	}
	return out
}

func replaceAll(haystack, needle, with string) string {
	if needle == "" {
		return haystack
	}
	for {
		idx := indexOf(haystack, needle)
		if idx < 0 {
			return haystack
		}
		haystack = haystack[:idx] + with + haystack[idx+len(needle):]
	}
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
