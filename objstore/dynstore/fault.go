package dynstore

import (
	"errors"
	"fmt"
	"net/http"

	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

// ErrServerFault marks an error the STORE SERVER produced, as opposed to one
// the driver or the caller caused.
//
// The distinction is the whole point. When krons' host filesystem filled, the
// server answered `InternalServerError` with a RequestID to both a read and a
// write; PostgreSQL could not write, and the server said so in the only
// vocabulary it has. The driver then wrapped it as `query routd.audit_log: …`,
// which reads as a client fault, so an operator mid-migration saw "the store is
// broken" when the true fact was "the host is full" (BUGS L293).
//
// A caller that tests for this knows the request was well formed and that
// retrying the same call will fail the same way until somebody fixes the
// server or its host.
var ErrServerFault = errors.New("the store server failed")

// serverFault names the operation and marks a server-side failure. It returns
// err unchanged when the endpoint did not fault, so a condition refusal, a
// validation error and a dead connection keep the shapes their callers match
// on.
func serverFault(op string, err error) error {
	if err == nil || !isServerFault(err) {
		return err
	}
	return fmt.Errorf("%s: %w: %w", op, ErrServerFault, err)
}

// isServerFault reports whether the endpoint answered with a fault of its own.
//
// Two shapes, because the SDK models one of them and not the other: DynamoDB's
// own `InternalServerError`, and any 5xx the transport saw. ExtendDB answers
// the first; a proxy or a crashed server answers the second.
func isServerFault(err error) bool {
	var internal *types.InternalServerError
	if errors.As(err, &internal) {
		return true
	}
	var resp *awshttp.ResponseError
	if errors.As(err, &resp) {
		return resp.HTTPStatusCode() >= http.StatusInternalServerError
	}
	return false
}
