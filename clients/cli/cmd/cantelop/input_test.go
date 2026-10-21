package main

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"
)

type notifyingInput struct {
	io.Reader
	started chan struct{}
	once    sync.Once
}

func (input *notifyingInput) Read(data []byte) (int, error) {
	input.once.Do(func() { close(input.started) })
	return input.Reader.Read(data)
}

func TestCommandsCancelWhileWaitingForInput(t *testing.T) {
	for _, test := range []struct {
		name string
		run  func(context.Context, io.Reader) error
	}{
		{"secret", func(ctx context.Context, input io.Reader) error {
			// A nil session makes any attempted mutation fail this test.
			return runConfigSet(ctx, "secret", []string{"API_TOKEN"}, "", input, io.Discard, nil)
		}},
		{"App creation confirmation", func(ctx context.Context, input io.Reader) error {
			approved, err := confirmAppCreation(ctx, input, io.Discard, "vera")
			if approved {
				return errors.New("canceled App creation was approved")
			}
			return err
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			reader, writer := io.Pipe()
			defer reader.Close()
			defer writer.Close()
			input := &notifyingInput{Reader: reader, started: make(chan struct{})}
			done := make(chan error, 1)
			go func() { done <- test.run(ctx, input) }()
			select {
			case <-input.started:
			case <-time.After(time.Second):
				t.Fatal("command did not start reading input")
			}
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("command returned %v, want context.Canceled", err)
				}
			case <-time.After(time.Second):
				t.Fatal("canceled command remained blocked waiting for stdin")
			}
		})
	}
}
