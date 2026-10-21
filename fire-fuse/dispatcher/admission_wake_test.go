package dispatcher

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/controlclient"
)

type admissionWakeControl struct {
	*executionTestControl
	mu            sync.Mutex
	queued        int
	idle          chan struct{}
	finished      chan struct{}
	notifications chan uint64
}

func (c *admissionWakeControl) ClaimNextMessageDelivery(context.Context) (*controlclient.MessageDelivery, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.queued == 0 {
		select {
		case c.idle <- struct{}{}:
		default:
		}
		return nil, nil
	}
	c.queued--
	return &controlclient.MessageDelivery{ID: "message", WorkspaceID: "missing", AcceptedAt: time.Now()}, nil
}
func (c *admissionWakeControl) WaitForMessageDeliveries(ctx context.Context, after uint64) (uint64, error) {
	select {
	case generation := <-c.notifications:
		return generation, nil
	case <-ctx.Done():
		return after, ctx.Err()
	}
}
func (c *admissionWakeControl) GetWorkspace(context.Context, string) (controlclient.Workspace, error) {
	return controlclient.Workspace{}, errors.New("workspace unavailable")
}
func (c *admissionWakeControl) FailMessageDelivery(context.Context, string, string, string) error {
	c.finished <- struct{}{}
	return nil
}
func (c *admissionWakeControl) RequeueHandlingMessageDeliveries(context.Context) (int64, error) {
	return 0, nil
}
func (c *admissionWakeControl) ListExecutions(context.Context) ([]controlclient.MessageExecution, error) {
	return nil, nil
}

func TestAdmissionNotificationWakesIdleWorkersAndDrainsQueue(t *testing.T) {
	_, base, mailbox, _ := executionFixture(t)
	c := &admissionWakeControl{executionTestControl: base, idle: make(chan struct{}, 20), finished: make(chan struct{}, 8), notifications: make(chan uint64, 1)}
	d, err := NewExecutionDispatcher(c, mailbox, idleOutputCollector{}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	d.interval = time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()
	defer func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(time.Second):
			t.Error("dispatcher failed to stop")
		}
	}()
	// Every worker has observed an empty queue before enqueue notification.
	for i := 0; i < 4; i++ {
		select {
		case <-c.idle:
		case <-time.After(time.Second):
			t.Fatal("workers did not become idle")
		}
	}
	c.mu.Lock()
	c.queued = 8
	c.mu.Unlock()
	c.notifications <- 1
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	for i := 0; i < 8; i++ {
		select {
		case <-c.finished:
		case <-deadline.C:
			t.Fatal("notification did not drain queue without polling")
		}
	}
}
