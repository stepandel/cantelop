package microvm

import (
	"encoding/json"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/runtime/messagequeue"
)

type MessageDeliveryStore interface {
	EnqueueMessageDelivery(messagequeue.Message) (messagequeue.Message, bool, error)
	GetMessageDelivery(string) (messagequeue.Message, error)
	ClaimNextMessageDelivery(time.Time) (messagequeue.Message, bool, error)
	RequeueHandlingMessageDeliveries(time.Time) (int64, error)
	RequeueMessageDelivery(string, time.Time) (messagequeue.Message, error)
	SaveMessageReply(string, json.RawMessage, time.Time) (messagequeue.Message, error)
	FailMessageDelivery(string, string, string, time.Time) (messagequeue.Message, error)
	ListMessageDeliveryEvents(messagequeue.EventQuery) (messagequeue.EventPage, error)
	PruneMessageDeliveryEventsBefore(time.Time) (int64, error)
}

func (s *SQLiteStore) EnqueueMessageDelivery(value messagequeue.Message) (messagequeue.Message, bool, error) {
	return s.messageDeliveries.Enqueue(value)
}

func (s *SQLiteStore) GetMessageDelivery(id string) (messagequeue.Message, error) {
	return s.messageDeliveries.Get(id)
}

func (s *SQLiteStore) ClaimNextMessageDelivery(at time.Time) (messagequeue.Message, bool, error) {
	return s.messageDeliveries.ClaimNext(at)
}

func (s *SQLiteStore) RequeueHandlingMessageDeliveries(at time.Time) (int64, error) {
	return s.messageDeliveries.RequeueHandling(at)
}

func (s *SQLiteStore) RequeueMessageDelivery(id string, at time.Time) (messagequeue.Message, error) {
	return s.messageDeliveries.Requeue(id, at)
}

func (s *SQLiteStore) SaveMessageReply(id string, reply json.RawMessage, at time.Time) (messagequeue.Message, error) {
	return s.messageDeliveries.SaveReply(id, reply, at)
}

func (s *SQLiteStore) FailMessageDelivery(id, sessionID, errorCode string, at time.Time) (messagequeue.Message, error) {
	return s.messageDeliveries.Fail(id, sessionID, errorCode, at)
}

func (s *SQLiteStore) ListMessageDeliveryEvents(query messagequeue.EventQuery) (messagequeue.EventPage, error) {
	return s.messageDeliveries.ListEvents(query)
}

func (s *SQLiteStore) PruneMessageDeliveryEventsBefore(cutoff time.Time) (int64, error) {
	return s.messageDeliveries.PruneTerminalEventsBefore(cutoff)
}

func (s *Service) messageDeliveries() (MessageDeliveryStore, error) {
	store, ok := s.store.(MessageDeliveryStore)
	if !ok {
		return nil, ErrStateStore
	}
	return store, nil
}

func (s *Service) EnqueueMessageDelivery(
	id, workspaceID, logicalSessionID, requestID string,
	keepAliveSeconds int,
	payload json.RawMessage,
	replyRequested bool,
) (messagequeue.Message, bool, error) {
	store, err := s.messageDeliveries()
	if err != nil {
		return messagequeue.Message{}, false, err
	}
	at := s.now().UTC()
	return store.EnqueueMessageDelivery(messagequeue.Message{
		ID: id, WorkspaceID: workspaceID, LogicalSessionID: logicalSessionID,
		RequestID:        requestID,
		KeepAliveSeconds: keepAliveSeconds, Payload: payload, ReplyRequested: replyRequested,
		Status: messagequeue.StatusAccepted, WorkState: messagequeue.WorkPending,
		AcceptedAt: at, UpdatedAt: at,
	})
}

func (s *Service) SaveMessageReply(id string, reply json.RawMessage) (messagequeue.Message, error) {
	store, err := s.messageDeliveries()
	if err != nil {
		return messagequeue.Message{}, err
	}
	return store.SaveMessageReply(id, reply, s.now().UTC())
}

func (s *Service) GetMessageDelivery(id string) (messagequeue.Message, error) {
	store, err := s.messageDeliveries()
	if err != nil {
		return messagequeue.Message{}, err
	}
	return store.GetMessageDelivery(id)
}

func (s *Service) ClaimNextMessageDelivery() (messagequeue.Message, bool, error) {
	store, err := s.messageDeliveries()
	if err != nil {
		return messagequeue.Message{}, false, err
	}
	return store.ClaimNextMessageDelivery(s.now().UTC())
}

func (s *Service) RequeueHandlingMessageDeliveries() (int64, error) {
	store, err := s.messageDeliveries()
	if err != nil {
		return 0, err
	}
	return store.RequeueHandlingMessageDeliveries(s.now().UTC())
}

func (s *Service) RequeueMessageDelivery(id string) (messagequeue.Message, error) {
	store, err := s.messageDeliveries()
	if err != nil {
		return messagequeue.Message{}, err
	}
	return store.RequeueMessageDelivery(id, s.now().UTC())
}

func (s *Service) FailMessageDelivery(id, sessionID, errorCode string) (messagequeue.Message, error) {
	store, err := s.messageDeliveries()
	if err != nil {
		return messagequeue.Message{}, err
	}
	return store.FailMessageDelivery(id, sessionID, errorCode, s.now().UTC())
}

func (s *Service) ListMessageDeliveryEvents(query messagequeue.EventQuery) (messagequeue.EventPage, error) {
	store, err := s.messageDeliveries()
	if err != nil {
		return messagequeue.EventPage{}, err
	}
	return store.ListMessageDeliveryEvents(query)
}

func (s *Service) PruneMessageDeliveryEventsBefore(cutoff time.Time) (int64, error) {
	store, err := s.messageDeliveries()
	if err != nil {
		return 0, err
	}
	return store.PruneMessageDeliveryEventsBefore(cutoff)
}
