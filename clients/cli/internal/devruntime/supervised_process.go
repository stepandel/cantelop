package devruntime

import (
	"os"
	"os/exec"
	"sync"
	"time"
)

const (
	// supervisedProcessStopTimeout is how long Close waits after an interrupt
	// before it kills the process.
	supervisedProcessStopTimeout = 5 * time.Second
	// supervisedProcessWaitDelay bounds how long Wait waits for output pipes a
	// descendant still holds open after the process itself exited.
	supervisedProcessWaitDelay = 5 * time.Second
)

// supervisedProcess is a started child process whose exit closes done.
type supervisedProcess struct {
	command *exec.Cmd
	done    chan struct{}
	once    sync.Once
	waitMu  sync.Mutex
	waitErr error
}

// startSupervisedProcess starts command and reaps it in the background,
// calling exited, when set, once the process has exited.
func startSupervisedProcess(command *exec.Cmd, exited func()) (*supervisedProcess, error) {
	if command.WaitDelay == 0 {
		command.WaitDelay = supervisedProcessWaitDelay
	}
	if err := command.Start(); err != nil {
		return nil, err
	}
	process := &supervisedProcess{command: command, done: make(chan struct{})}
	go func() {
		err := command.Wait()
		if exited != nil {
			exited()
		}
		process.waitMu.Lock()
		process.waitErr = err
		process.waitMu.Unlock()
		close(process.done)
	}()
	return process, nil
}

// Close interrupts the process, kills it if it has not exited within
// supervisedProcessStopTimeout, and returns once it has exited.
func (process *supervisedProcess) Close() {
	process.once.Do(func() {
		_ = process.command.Process.Signal(os.Interrupt)
		select {
		case <-process.done:
		case <-time.After(supervisedProcessStopTimeout):
			_ = process.command.Process.Kill()
			<-process.done
		}
	})
}

// WaitError returns the process's exit error once done is closed.
func (process *supervisedProcess) WaitError() error {
	process.waitMu.Lock()
	defer process.waitMu.Unlock()
	return process.waitErr
}
