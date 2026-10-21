package wipdremote

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"

	"github.com/procrastivity/wip/internal/d115"
	"github.com/procrastivity/wip/internal/wipd"
	"github.com/procrastivity/wip/internal/wipdseed"
)

// ConfigureD115CreateLocal wires the owner-mediated prepare/import/submit
// lifecycle to authenticated local IPC. No signer is accepted by the daemon.
func ConfigureD115CreateLocal(server *wipd.Server, profileRoot string) error {
	if server == nil || profileRoot == "" {
		return errors.New("wipdremote: D115 lifecycle dependencies are required")
	}
	if err := server.ConfigureD115Lifecycle(d115Lifecycle{profileRoot: profileRoot}); err != nil {
		return err
	}
	scheduleD115StartupRecovery(profileRoot)
	return nil
}

type d115Lifecycle struct{ profileRoot string }

var d115RecoveryScheduled sync.Map

func scheduleD115StartupRecovery(profileRoot string) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if _, err := wipdseed.RecoverD115StartupAttempts(ctx, profileRoot); err != nil {
			log.Printf("wipdremote: D115 startup recovery incomplete: %v", err)
		}
	}()
}

func (handler d115Lifecycle) Prepare(ctx context.Context, peer *wipd.PeerProcess, grant string) (wipd.D115LocalAttempt, error) {
	if ctx == nil || peer == nil {
		return wipd.D115LocalAttempt{}, wipd.ErrUnsupportedCheckoutContext
	}
	binding, err := peer.D115CheckoutCorrelation(ctx)
	if err != nil {
		return wipd.D115LocalAttempt{}, err
	}
	state, err := wipdseed.PrepareD115CreateLocal(handler.profileRoot, grant, binding)
	return localAttempt(state), err
}

func (handler d115Lifecycle) ImportProof(_ context.Context, commandID string, proof []byte) (wipd.D115LocalAttempt, error) {
	state, err := wipdseed.ImportD115OwnerProof(handler.profileRoot, commandID, proof)
	return localAttempt(state), err
}

func (handler d115Lifecycle) Submit(ctx context.Context, peer *wipd.PeerProcess, commandID string) ([]byte, error) {
	if ctx == nil || peer == nil {
		return nil, wipd.ErrUnsupportedCheckoutContext
	}
	binding, err := peer.D115CheckoutCorrelation(ctx)
	if err != nil {
		return nil, err
	}
	receipt, err := wipdseed.SubmitPreparedD115CreateLocal(ctx, handler.profileRoot, binding, commandID)
	if err != nil {
		disposition := "refused"
		switch {
		case errors.Is(err, wipdseed.ErrD115OutcomeUnknown):
			disposition = "outcome-unknown"
			scheduleD115Recovery(handler.profileRoot, binding, commandID)
		case errors.Is(err, wipdseed.ErrD115DefinitelyUnsent):
			disposition = "definitely-unsent"
		case errors.Is(err, wipdseed.ErrD115ReceiptDurable):
			disposition = "receipt-durable"
			scheduleD115Recovery(handler.profileRoot, binding, commandID)
		}
		return nil, &wipd.D115SubmitDispositionError{Disposition: disposition}
	}
	return d115.EncodeReceipt(receipt)
}

func scheduleD115Recovery(profileRoot, binding, commandID string) {
	key := profileRoot + "\x00" + binding + "\x00" + commandID
	if _, loaded := d115RecoveryScheduled.LoadOrStore(key, struct{}{}); loaded {
		return
	}
	go func() {
		defer d115RecoveryScheduled.Delete(key)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, _ = wipdseed.RecoverD115Attempt(ctx, profileRoot, binding, commandID)
	}()
}

func (handler d115Lifecycle) Status(_ context.Context, commandID string) (wipd.D115LocalAttempt, error) {
	state, err := wipdseed.D115CreateLocalStatus(handler.profileRoot, commandID)
	return localAttempt(state), err
}

func (handler d115Lifecycle) Cancel(_ context.Context, commandID string) (wipd.D115LocalAttempt, error) {
	state, err := wipdseed.CancelD115CreateLocal(handler.profileRoot, commandID)
	return localAttempt(state), err
}

func localAttempt(state wipdseed.D115LocalState) wipd.D115LocalAttempt {
	return wipd.D115LocalAttempt{AttemptID: state.AttemptID, State: string(state.State), SigningRequest: state.SigningRequest, Receipt: state.Receipt}
}
