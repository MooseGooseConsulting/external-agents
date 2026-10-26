package agents

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

func init() {
	if os.Getenv("KIMI_E2E") != "1" || os.Getenv("E2E_AGENT") != "kimi-code" {
		return
	}
	Register(&KimiCode{})
	RegisterGate("kimi-code", 1)
}

type KimiCode struct{}

func (q *KimiCode) Name() string               { return "kimi-code" }
func (q *KimiCode) Binary() string             { return "kimi" }
func (q *KimiCode) EntireAgent() string        { return "kimi-code" }
func (q *KimiCode) PromptPattern() string      { return `>` }
func (q *KimiCode) TimeoutMultiplier() float64 { return 2.0 }
func (q *KimiCode) IsExternalAgent() bool      { return true }

func (q *KimiCode) IsTransientError(out Output, _ error) bool {
	combined := strings.ToLower(out.Stdout + out.Stderr)
	patterns := []string{
		"overloaded",
		"rate limit",
		"429",
		"500",
		"503",
		"econnreset",
		"etimedout",
		"timeout",
	}
	for _, pattern := range patterns {
		if strings.Contains(combined, pattern) {
			return true
		}
	}
	return false
}

func (q *KimiCode) Bootstrap() error {
	return nil
}

func (q *KimiCode) RunPrompt(ctx context.Context, dir string, prompt string, opts ...Option) (Output, error) {
	cfg := &runConfig{}
	for _, o := range opts {
		o(cfg)
	}

	bin, err := exec.LookPath(q.Binary())
	if err != nil {
		return Output{}, fmt.Errorf("%s not in PATH: %w", q.Binary(), err)
	}

	args := []string{"-p", prompt}
	displayArgs := []string{"-p", fmt.Sprintf("%q", prompt)}
	if cfg.Model != "" {
		args = append(args, "--model", cfg.Model)
		displayArgs = append(displayArgs, "--model", cfg.Model)
	}

	env := filterEnv(os.Environ(), "ENTIRE_TEST_TTY")
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = 5 * time.Second

	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err = cmd.Run()
	exitCode := 0
	if err != nil {
		exitErr := &exec.ExitError{}
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = -1
		}
	}

	return Output{
		Command:  q.Binary() + " " + strings.Join(displayArgs, " "),
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		ExitCode: exitCode,
	}, err
}

func (q *KimiCode) StartSession(_ context.Context, dir string) (Session, error) {
	return NewTmuxSession(fmt.Sprintf("kimi-e2e-%d", time.Now().UnixNano()), dir, []string{"ENTIRE_TEST_TTY"}, "kimi", "--auto")
}
