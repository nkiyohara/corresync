package daemonapi

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/localipc"
)

// Status negotiates the protocol and exposes the daemon default account.
func (client *Client) Status(ctx context.Context, caller domain.Caller) (Status, error) {
	owner, err := client.InspectOwner(ctx, caller)
	if err != nil {
		return Status{}, err
	}
	return owner.Status(), nil
}

// InspectOwner reads status while retaining an in-memory binding to the exact
// rotating credential that authenticated that daemon generation. A protocol
// mismatch returns both a usable snapshot and ProtocolVersionError only after
// the daemon proves the current-version request was rejected before dispatch.
func (client *Client) InspectOwner(
	ctx context.Context,
	caller domain.Caller,
) (OwnerSnapshot, error) {
	credential, err := localipc.LoadCredential(client.endpoint)
	if err != nil {
		return OwnerSnapshot{}, fmt.Errorf("load daemon credential: %w", err)
	}
	result, err := client.status(ctx, caller, ProtocolVersion, credential)
	if err == nil {
		return OwnerSnapshot{
			status:          result,
			protocolVersion: ProtocolVersion,
			credential:      credential,
		}, nil
	}
	var versionErr *ProtocolVersionError
	if !errors.As(err, &versionErr) || !versionErr.RequestRejected() {
		return OwnerSnapshot{}, err
	}
	incompatible, inspectErr := client.status(
		ctx,
		caller,
		versionErr.DaemonVersion,
		credential,
	)
	if inspectErr != nil {
		return OwnerSnapshot{}, errors.Join(
			err,
			fmt.Errorf("inspect incompatible daemon: %w", inspectErr),
		)
	}
	return OwnerSnapshot{
		status:          incompatible,
		protocolVersion: versionErr.DaemonVersion,
		credential:      credential,
	}, err
}

func (client *Client) status(
	ctx context.Context,
	caller domain.Caller,
	protocolVersion int,
	credential string,
) (Status, error) {
	var result Status
	if err := client.callWithCredential(
		ctx,
		protocolVersion,
		credential,
		MethodStatus,
		caller,
		struct{}{},
		&result,
	); err != nil {
		return Status{}, err
	}
	if result.ProtocolVersion != protocolVersion {
		return Status{}, &ProtocolVersionError{
			ClientVersion: protocolVersion,
			DaemonVersion: result.ProtocolVersion,
		}
	}
	if result.Version == "" || len(result.Version) > 128 ||
		strings.ContainsAny(result.Version, "\r\n\x00") {
		return Status{}, errors.New("daemon returned an invalid version")
	}
	if result.ProcessID < 1 || result.StartedAt.IsZero() {
		return Status{}, errors.New("daemon returned invalid process metadata")
	}
	if result.DefaultAccount != "" {
		if err := result.DefaultAccount.Validate(); err != nil {
			return Status{}, errors.New("daemon returned an invalid default account")
		}
	}
	if err := validateConfigDigest(result.ConfigDigest); err != nil {
		return Status{}, errors.New("daemon returned an invalid config digest")
	}
	return result, nil
}

// Shutdown requests graceful termination after the response is written.
func (client *Client) Shutdown(ctx context.Context, caller domain.Caller) error {
	owner, err := client.InspectOwner(ctx, caller)
	var versionErr *ProtocolVersionError
	if err != nil && (!errors.As(err, &versionErr) || owner.credential == "") {
		return err
	}
	return client.ShutdownOwner(ctx, caller, owner)
}

// ShutdownOwner gracefully stops only the daemon generation captured by
// InspectOwner. Credential rotation prevents a delayed caller from stopping a
// replacement owner.
func (client *Client) ShutdownOwner(
	ctx context.Context,
	caller domain.Caller,
	owner OwnerSnapshot,
) error {
	if owner.protocolVersion < 1 ||
		owner.status.ProtocolVersion != owner.protocolVersion ||
		owner.status.ProcessID < 1 ||
		localipc.ValidateCredential(owner.credential) != nil {
		return errors.New("invalid daemon owner snapshot")
	}
	var result struct {
		Stopping bool `json:"stopping"`
	}
	if err := client.callWithCredential(
		ctx,
		owner.protocolVersion,
		owner.credential,
		MethodShutdown,
		caller,
		struct{}{},
		&result,
	); err != nil {
		return err
	}
	if !result.Stopping {
		return errors.New("daemon did not acknowledge shutdown")
	}
	return nil
}
