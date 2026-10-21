package guestassignment

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"time"
)

const (
	sdkBridgeTargetPort         = 19093
	sdkBridgeFallbackTargetPort = 19094
	sdkBridgeDialTimeout        = 30 * time.Second
	sdkBridgeRetryInterval      = 5 * time.Millisecond
	sdkBridgeMaxConnections     = 64
)

type sdkBridge interface {
	Wait(context.Context) error
	Close() error
}

type sdkBridgeStarter func(context.Context, string, int, int) (sdkBridge, error)

type runtimeSDKBridge struct {
	listener net.Listener
	target   string
	cancel   context.CancelFunc
	done     chan error
	close    sync.Once
}

func startSDKBridge(
	ctx context.Context,
	guestAddress string,
	listenerPort int,
	targetPort int,
) (sdkBridge, error) {
	listener, err := net.Listen(
		"tcp4",
		net.JoinHostPort(guestAddress, strconv.Itoa(listenerPort)),
	)
	if err != nil {
		return nil, fmt.Errorf("listen on SDK gateway port: %w", err)
	}
	bridgeContext, cancel := context.WithCancel(ctx)
	bridge := &runtimeSDKBridge{
		listener: listener,
		target:   net.JoinHostPort("127.0.0.1", strconv.Itoa(targetPort)),
		cancel:   cancel,
		done:     make(chan error, 1),
	}
	go func() {
		bridge.done <- bridge.serve(bridgeContext)
	}()
	return bridge, nil
}

func (b *runtimeSDKBridge) serve(ctx context.Context) error {
	connections := make(chan struct{}, sdkBridgeMaxConnections)
	for {
		client, err := b.listener.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return fmt.Errorf("accept SDK gateway connection: %w", err)
		}
		select {
		case connections <- struct{}{}:
			go func() {
				defer func() { <-connections }()
				b.forward(ctx, client)
			}()
		default:
			_ = client.Close()
		}
	}
}

func (b *runtimeSDKBridge) forward(ctx context.Context, client net.Conn) {
	defer client.Close()
	deadline := time.Now().Add(sdkBridgeDialTimeout)
	var workload net.Conn
	for workload == nil {
		if ctx.Err() != nil || time.Now().After(deadline) {
			return
		}
		connection, err := (&net.Dialer{Timeout: 50 * time.Millisecond}).DialContext(
			ctx,
			"tcp4",
			b.target,
		)
		if err == nil {
			workload = connection
			break
		}
		timer := time.NewTimer(sdkBridgeRetryInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
	defer workload.Close()

	copied := make(chan struct{}, 2)
	copyStream := func(destination, source net.Conn) {
		_, _ = io.Copy(destination, source)
		if writer, ok := destination.(*net.TCPConn); ok {
			_ = writer.CloseWrite()
		}
		copied <- struct{}{}
	}
	go copyStream(workload, client)
	go copyStream(client, workload)
	<-copied
	<-copied
}

func (b *runtimeSDKBridge) Wait(ctx context.Context) error {
	select {
	case err := <-b.done:
		return err
	case <-ctx.Done():
		_ = b.Close()
		return context.Cause(ctx)
	}
}

func (b *runtimeSDKBridge) Close() error {
	var err error
	b.close.Do(func() {
		b.cancel()
		err = b.listener.Close()
	})
	if errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}
