package main

import "context"

// readCommandInput lets a command stop waiting for stdin when it is canceled.
// An arbitrary reader cannot be interrupted; its read may finish later, or
// remain blocked until the CLI exits after the command's cleanup.
func readCommandInput(ctx context.Context, read func() ([]byte, error)) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if ctx.Done() == nil {
		return read()
	}
	type result struct {
		data []byte
		err  error
	}
	ready := make(chan result, 1)
	go func() {
		data, err := read()
		ready <- result{data: data, err: err}
	}()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case result := <-ready:
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return result.data, result.err
	}
}
