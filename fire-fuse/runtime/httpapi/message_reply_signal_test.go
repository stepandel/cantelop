package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/runtime/messagequeue"
)

func TestReplySignalIsScopedAndReleasesWaiters(t *testing.T) {
	s := newMessageReplySignal()
	first, cancelFirst := s.subscribe("first")
	second, cancelSecond := s.subscribe("second")
	defer cancelSecond()
	s.notify("first")
	select {
	case <-first:
	default:
		t.Fatal("Message waiter not woken")
	}
	select {
	case <-second:
		t.Fatal("unrelated Message woken")
	default:
	}
	// Unsubscribing an old generation cannot remove a newer waiter.
	newer, cancelNewer := s.subscribe("first")
	cancelFirst()
	s.notify("first")
	select {
	case <-newer:
	default:
		t.Fatal("new waiter lost")
	}
	cancelNewer()
	cancelSecond()
	if len(s.waiters) != 0 {
		t.Fatal("waiter state leaked")
	}
}

type replyWaitControl struct {
	MessageDeliveryController
	mu               sync.Mutex
	state            messagequeue.Status
	read             chan struct{}
	once             sync.Once
	signal           *messageReplySignal
	commitDuringRead bool
}

func (c *replyWaitControl) GetMessageDelivery(id string) (messagequeue.Message, error) {
	c.mu.Lock()
	state := c.state
	c.mu.Unlock()
	c.once.Do(func() {
		if c.commitDuringRead {
			c.complete(id)
		}
		close(c.read)
	})
	return messagequeue.Message{ID: id, Status: state, Reply: []byte(`"ready"`)}, nil
}
func (c *replyWaitControl) complete(id string) {
	c.mu.Lock()
	c.state = "handled"
	c.mu.Unlock()
	c.signal.notify(id)
}
func TestReplyWaitDoesNotMissCommit(t *testing.T) {
	for _, duringRead := range []bool{false, true} {
		t.Run(map[bool]string{false: "after read", true: "during read"}[duringRead], func(t *testing.T) {
			signal := newMessageReplySignal()
			control := &replyWaitControl{state: "handling", read: make(chan struct{}), signal: signal, commitDuringRead: duringRead}
			req := httptest.NewRequest(http.MethodGet, "/v1/message-deliveries/test/wait", nil)
			req.SetPathValue("id", "test")
			w := httptest.NewRecorder()
			done := make(chan struct{})
			go func() { getMessageDelivery(control, signal)(w, req); close(done) }()
			<-control.read
			if !duringRead {
				control.complete("test")
			}
			select {
			case <-done:
			case <-time.After(300 * time.Millisecond):
				t.Fatal("reply waited for polling timer")
			}
			if w.Code != 200 || !strings.Contains(w.Body.String(), `"state":"handled"`) {
				t.Fatalf("reply: %d %s", w.Code, w.Body.String())
			}
			if len(signal.waiters) != 0 {
				t.Fatal("waiter leaked")
			}
		})
	}
}
func TestReplyWaitCancellation(t *testing.T) {
	signal := newMessageReplySignal()
	control := &replyWaitControl{state: "handling", read: make(chan struct{}), signal: signal}
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/test/wait", nil).WithContext(ctx)
	req.SetPathValue("id", "test")
	w := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { getMessageDelivery(control, signal)(w, req); close(done) }()
	<-control.read
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cancel blocked")
	}
	if w.Body.Len() != 0 || len(signal.waiters) != 0 {
		t.Fatal("canceled wait retained state or wrote response")
	}
}

type replyExecutionControl struct {
	executionController
	signal  *messageReplySignal
	changed <-chan struct{}
	err     error
	t       *testing.T
}

func (c replyExecutionControl) SaveExecution(e messagequeue.Execution) (messagequeue.Execution, error) {
	select {
	case <-c.changed:
		c.t.Fatal("notification preceded durable commit")
	default:
	}
	return e, c.err
}
func TestReplyNotificationOnlyFollowsSuccessfulExecutionCommit(t *testing.T) {
	for _, failed := range []bool{false, true} {
		signal := newMessageReplySignal()
		changed, cleanup := signal.subscribe("test")
		control := replyExecutionControl{signal: signal, changed: changed, t: t}
		if failed {
			control.err = errors.New("commit failed")
		}
		w := httptest.NewRecorder()
		executions(control, signal)(w, httptest.NewRequest(http.MethodPost, "/v1/message-executions", strings.NewReader(`{"message_id":"test"}`)))
		select {
		case <-changed:
			if failed {
				t.Fatal("failed commit notified")
			}
		default:
			if !failed {
				t.Fatal("successful commit did not notify")
			}
		}
		cleanup()
	}
}

func TestReplyWaitAlreadyTerminalAndTimeoutFallback(t *testing.T) {
	for _, state := range []messagequeue.Status{"handled", "failed", "handling"} {
		t.Run(string(state), func(t *testing.T) {
			signal := newMessageReplySignal()
			control := &replyWaitControl{state: state, read: make(chan struct{}), signal: signal}
			req := httptest.NewRequest(http.MethodGet, "/v1/message-deliveries/test/wait", nil)
			req.SetPathValue("id", "test")
			w := httptest.NewRecorder()
			start := time.Now()
			getMessageDelivery(control, signal)(w, req)
			elapsed := time.Since(start)
			if w.Code != 200 || !strings.Contains(w.Body.String(), `"state":"`+string(state)+`"`) {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			if state == "handling" && elapsed < 900*time.Millisecond {
				t.Fatal("missing notification did not use bounded fallback")
			}
			if state != "handling" && elapsed > 300*time.Millisecond {
				t.Fatal("already terminal Message blocked")
			}
			if len(signal.waiters) != 0 {
				t.Fatal("waiter leaked")
			}
		})
	}
}
