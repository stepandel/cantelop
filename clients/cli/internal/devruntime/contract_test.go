package devruntime

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stepandel/cantelop/fire-fuse/messageprotocol"
	"github.com/stepandel/cantelop/internal/runtimecontract"
)

type pendingContractSandboxFactory struct{}

func (pendingContractSandboxFactory) Start(ctx context.Context, _ string) (localSandbox, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestLocalPublicSDKContract(t *testing.T) {
	runtimecontract.Run(t, func(t *testing.T, seed runtimecontract.Seed) http.Handler {
		runtime := newTestLocalRuntime(t, pendingContractSandboxFactory{})
		if seed.State != "" {
			var input messageprotocol.Request
			if err := json.Unmarshal([]byte(runtimecontract.Body), &input); err != nil {
				t.Fatal(err)
			}
			input.Reply = true
			now := time.Now().UTC()
			runtime.messages[input.Message.ID] = &localMessageRecord{request: input, state: seed.State, errorCode: seed.ErrorCode, reply: seed.Reply, acceptedAt: now, startedAt: &now, handledAt: &now}
		}
		return runtime
	})
}
