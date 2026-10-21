package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/runtime/messagequeue"
)

type notificationEnqueueControl struct {
	MessageDeliveryController
	beforeSave func()
	err        error
}

func (c notificationEnqueueControl) EnqueueMessageDelivery(string, string, string, string, int, json.RawMessage, bool) (messagequeue.Message, bool, error) {
	c.beforeSave()
	return messagequeue.Message{}, true, c.err
}

func TestMessageNotificationFollowsSuccessfulEnqueue(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{false: "saved", true: "failed"}[failed], func(t *testing.T) {
			signal := newMessageAdmissionSignal()
			_, changed := signal.subscribe()
			control := notificationEnqueueControl{beforeSave: func() {
				select {
				case <-changed:
					t.Fatal("notification preceded persistence")
				default:
				}
			}}
			if failed {
				control.err = errors.New("storage failure")
			}
			w := httptest.NewRecorder()
			enqueueMessageDelivery(control, signal)(w, httptest.NewRequest(http.MethodPost, "/v1/message-deliveries", strings.NewReader(`{"id":"msg_test"}`)))
			generation, _ := signal.subscribe()
			if failed && generation != 0 {
				t.Fatal("failed enqueue notified workers")
			}
			if !failed && generation != 1 {
				t.Fatal("successful enqueue did not notify workers")
			}
		})
	}
}

func TestMessageNotificationGenerationSurvivesMissedWait(t *testing.T) {
	signal := newMessageAdmissionSignal()
	_, first := signal.subscribe()
	_, second := signal.subscribe()
	signal.notify()
	for _, waiter := range []<-chan struct{}{first, second} {
		select {
		case <-waiter:
		default:
			t.Fatal("waiter missed broadcast")
		}
	}
	// Enqueue before the HTTP wait begins must also return immediately.
	w := httptest.NewRecorder()
	waitMessageDeliveryNotification(signal)(w, httptest.NewRequest(http.MethodGet, "/v1/message-delivery-notifications?after=0", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"generation":1`) {
		t.Fatalf("notification: %d %s", w.Code, w.Body.String())
	}
}

func TestMessageNotificationWaitCancellationAndTimeout(t *testing.T) {
	signal := newMessageAdmissionSignal()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w := httptest.NewRecorder()
	waitMessageDeliveryNotification(signal)(w, httptest.NewRequest(http.MethodGet, "/v1/message-delivery-notifications?after=0", nil).WithContext(ctx))
	if w.Body.Len() != 0 {
		t.Fatal("canceled wait returned a notification")
	}
	started := time.Now()
	w = httptest.NewRecorder()
	waitMessageDeliveryNotification(signal)(w, httptest.NewRequest(http.MethodGet, "/v1/message-delivery-notifications?after=0", nil))
	if time.Since(started) < 900*time.Millisecond || !strings.Contains(w.Body.String(), `"generation":0`) {
		t.Fatal("idle wait did not use bounded timeout")
	}
}
