package opencode

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

const (
	inlinePromptMaxBytes = 64 * 1024
	stderrRingCapBytes   = 32 * 1024
	promptFileMessage    = "Read and strictly execute the complete task specification in the attached file."
)

type processRunSpec struct {
	BinaryPath   string
	WorkDir      string
	ModelArg     string
	PromptText   string
	PromptName   string
	SessionID    string
	SessionTitle string
	IsV2         bool
	Env          []string
	SessionDir   string
	Timeout      time.Duration
	IdleTimeout  time.Duration
}

type processOutcome struct {
	Stdout            []byte
	Stderr            string
	ExitCode          int
	TerminationReason string
	Err               error
}

type ringBuffer struct {
	mu  sync.Mutex
	buf []byte
	cap int
}

func newRingBuffer(capBytes int) *ringBuffer {
	return &ringBuffer{
		cap: capBytes,
		buf: make([]byte, 0, capBytes),
	}
}

func (r *ringBuffer) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(p) >= r.cap {
		r.buf = append(r.buf[:0], p[len(p)-r.cap:]...)
		return len(p), nil
	}
	if len(r.buf)+len(p) > r.cap {
		overflow := len(r.buf) + len(p) - r.cap
		copy(r.buf, r.buf[overflow:])
		r.buf = r.buf[:len(r.buf)-overflow]
	}
	r.buf = append(r.buf, p...)
	return len(p), nil
}

func (r *ringBuffer) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return string(r.buf)
}

func buildProcessArgs(spec processRunSpec) ([]string, error) {
	promptMsg := spec.PromptText
	promptFilePath := ""

	if len(spec.PromptText) > inlinePromptMaxBytes {
		name := spec.PromptName
		if name == "" {
			name = "prompt.md"
		}
		promptFilePath = filepath.Join(spec.SessionDir, name)
		if err := writeSecureFile(promptFilePath, []byte(spec.PromptText), 0o600); err != nil {
			return nil, fmt.Errorf("落盘大 Prompt 至 %s 失败: %w", promptFilePath, err)
		}
		promptMsg = promptFileMessage
	}

	if spec.IsV2 {
		args := []string{
			"run",
			"--standalone",
			"--format", "json",
			"--agent", ReadOnlyAgentName,
			"--model", spec.ModelArg,
		}
		if spec.SessionID == "" {
			args = append(args, "--archived")
			if title := strings.TrimSpace(spec.SessionTitle); title != "" {
				args = append(args, "--title", title)
			}
		}
		if promptFilePath != "" {
			args = append(args, "--file", promptFilePath)
		}
		if spec.SessionID != "" {
			args = append(args, "--session", spec.SessionID)
		}
		args = append(args, "--", promptMsg)
		return args, nil
	}

	args := []string{
		"run", promptMsg,
		"--format", "json",
		"--pure",
		"--agent", ReadOnlyAgentName,
		"--model", spec.ModelArg,
		"--dir", spec.WorkDir,
	}
	if promptFilePath != "" {
		args = append(args, "--file", promptFilePath)
	}
	if spec.SessionID != "" {
		args = append(args, "--session", spec.SessionID)
	}
	return args, nil
}

// runOpenCodeProcess 在单一 LockOSThread 协程内完成 Start -> 并发消费双管道 -> 同线程 Wait，
// 防止 Go 调度器回收 M 线程误触发 Linux Pdeathsig；超时/取消/Idle 看门狗通过 -pgid 秒杀整个进程树。
func runOpenCodeProcess(ctx context.Context, spec processRunSpec) processOutcome {
	if ctx == nil {
		ctx = context.Background()
	}
	if ctx.Err() != nil {
		return processOutcome{
			TerminationReason: "canceled",
			Err:               &ClassifiedError{Class: ErrorClassCanceled, Message: "上下文已取消", Cause: ctx.Err()},
		}
	}

	args, err := buildProcessArgs(spec)
	if err != nil {
		return processOutcome{
			TerminationReason: "setup_failed",
			Err:               err,
		}
	}

	outCh := make(chan processOutcome, 1)
	go func() {
		// Linux Pdeathsig 绑定启动子进程的 OS 线程（M）：必须在同一 LockOSThread 协程完成 Start 与 Wait
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()

		cmd := exec.Command(spec.BinaryPath, args...)
		cmd.Dir = spec.WorkDir
		cmd.Env = spec.Env
		cmd.SysProcAttr = &syscall.SysProcAttr{
			Setpgid:   true,
			Pdeathsig: syscall.SIGKILL,
		}

		stdoutPipe, pipeErr := cmd.StdoutPipe()
		if pipeErr != nil {
			outCh <- processOutcome{TerminationReason: "pipe_error", Err: pipeErr}
			return
		}
		stderrPipe, pipeErr := cmd.StderrPipe()
		if pipeErr != nil {
			outCh <- processOutcome{TerminationReason: "pipe_error", Err: pipeErr}
			return
		}

		if startErr := cmd.Start(); startErr != nil {
			outCh <- processOutcome{TerminationReason: "start_failed", Err: startErr}
			return
		}

		pgid := cmd.Process.Pid
		var lastActivityUnixNano atomic.Int64
		lastActivityUnixNano.Store(time.Now().UnixNano())

		var stdoutBuf bytes.Buffer
		stderrRing := newRingBuffer(stderrRingCapBytes)

		var pipeWG sync.WaitGroup
		pipeWG.Add(2)

		go func() {
			defer pipeWG.Done()
			buf := make([]byte, 32*1024)
			for {
				n, rErr := stdoutPipe.Read(buf)
				if n > 0 {
					lastActivityUnixNano.Store(time.Now().UnixNano())
					stdoutBuf.Write(buf[:n])
				}
				if rErr != nil {
					if rErr != io.EOF {
						_, _ = stderrRing.Write([]byte("\n[stdout read err] " + rErr.Error()))
					}
					return
				}
			}
		}()

		go func() {
			defer pipeWG.Done()
			buf := make([]byte, 8*1024)
			for {
				n, rErr := stderrPipe.Read(buf)
				if n > 0 {
					lastActivityUnixNano.Store(time.Now().UnixNano())
					_, _ = stderrRing.Write(buf[:n])
				}
				if rErr != nil {
					return
				}
			}
		}()

		var (
			killOnce          sync.Once
			terminationReason atomic.Value
			stopWatch         = make(chan struct{})
			watchWG           sync.WaitGroup
		)
		terminationReason.Store("")

		killGroup := func(reason string) {
			killOnce.Do(func() {
				terminationReason.Store(reason)
				_ = syscall.Kill(-pgid, syscall.SIGKILL)
			})
		}

		watchWG.Add(1)
		go func() {
			defer watchWG.Done()
			var timeoutTimer *time.Timer
			var timeoutCh <-chan time.Time
			if spec.Timeout > 0 {
				timeoutTimer = time.NewTimer(spec.Timeout)
				defer timeoutTimer.Stop()
				timeoutCh = timeoutTimer.C
			}

			var idleTicker *time.Ticker
			var idleCh <-chan time.Time
			if spec.IdleTimeout > 0 {
				tickInterval := spec.IdleTimeout / 4
				if tickInterval < 50*time.Millisecond {
					tickInterval = 50 * time.Millisecond
				}
				if tickInterval > 250*time.Millisecond {
					tickInterval = 250 * time.Millisecond
				}
				idleTicker = time.NewTicker(tickInterval)
				defer idleTicker.Stop()
				idleCh = idleTicker.C
			}

			for {
				select {
				case <-stopWatch:
					return
				case <-ctx.Done():
					killGroup("canceled")
					return
				case <-timeoutCh:
					killGroup("timeout")
					return
				case <-idleCh:
					last := time.Unix(0, lastActivityUnixNano.Load())
					if time.Since(last) >= spec.IdleTimeout {
						killGroup("idle_timeout")
						return
					}
				}
			}
		}()

		pipeWG.Wait()
		waitErr := cmd.Wait()
		close(stopWatch)
		watchWG.Wait()

		reason, _ := terminationReason.Load().(string)
		exitCode := 0
		if waitErr != nil {
			if exitErr, ok := waitErr.(*exec.ExitError); ok {
				exitCode = exitErr.ExitCode()
			} else {
				exitCode = -1
			}
		}

		var finalErr error
		switch reason {
		case "canceled":
			finalErr = &ClassifiedError{Class: ErrorClassCanceled, Message: "OpenCode 会话被取消", Cause: ctx.Err()}
		case "timeout":
			finalErr = &ClassifiedError{Class: ErrorClassTimeout, Message: fmt.Sprintf("OpenCode 会话超时（>%v）", spec.Timeout), Cause: waitErr}
		case "idle_timeout":
			finalErr = &ClassifiedError{Class: ErrorClassIdleTimeout, Message: fmt.Sprintf("OpenCode 会话空闲超时（>%v 无输出活动）", spec.IdleTimeout), Cause: waitErr}
		default:
			if waitErr != nil {
				finalErr = waitErr
			}
		}

		outCh <- processOutcome{
			Stdout:            stdoutBuf.Bytes(),
			Stderr:            stderrRing.String(),
			ExitCode:          exitCode,
			TerminationReason: reason,
			Err:               finalErr,
		}
	}()

	return <-outCh
}
