package dynstore

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	smithyhttp "github.com/aws/smithy-go/transport/http"
)

// When krons' host filesystem filled, the server answered InternalServerError
// to both a read and a write and the driver wrapped it as an ordinary error, so
// an operator mid-migration read "the store is broken" instead of "the host is
// full". A caller can now tell the two apart (BUGS L293).
func TestServerFaultNamesTheServerAndTheOperation(t *testing.T) {
	err := serverFault("query routd.audit_log", &types.InternalServerError{})
	if !errors.Is(err, ErrServerFault) {
		t.Fatalf("InternalServerError is not marked as a server fault: %v", err)
	}
	if !strings.Contains(err.Error(), "query routd.audit_log") {
		t.Errorf("error does not name the operation: %q", err.Error())
	}
	// The original survives, so a caller that matches on the SDK type still can.
	var internal *types.InternalServerError
	if !errors.As(err, &internal) {
		t.Error("the wrap hid the SDK error it wrapped")
	}
}

// httpFault builds the shape the transport reports when the endpoint answered
// a status the SDK does not model as a typed error.
func httpFault(status int) error {
	return &awshttp.ResponseError{
		ResponseError: &smithyhttp.ResponseError{
			Response: &smithyhttp.Response{Response: &http.Response{StatusCode: status}},
			Err:      errors.New("boom"),
		},
		RequestID: "req-1",
	}
}

// ExtendDB models the fault; a proxy or a crashed server answers a bare 5xx the
// SDK does not type. Both are the server's, and a 4xx is not.
func TestServerFaultReadsTheTransportStatus(t *testing.T) {
	for _, status := range []int{500, 502, 503} {
		if got := serverFault("op", httpFault(status)); !errors.Is(got, ErrServerFault) {
			t.Errorf("status %d not marked as a server fault", status)
		}
	}
	for _, status := range []int{400, 403, 404, 429} {
		if got := serverFault("op", httpFault(status)); errors.Is(got, ErrServerFault) {
			t.Errorf("status %d marked as a server fault; that sends an operator at a healthy host", status)
		}
	}
}

// A refusal, a validation error and a dead connection are NOT the server
// faulting, and marking them so would tell an operator to go look at a healthy
// host. An unfaulted error is returned EXACTLY as it came, so every caller that
// matches on its shape still can.
func TestServerFaultLeavesEveryOtherErrorAlone(t *testing.T) {
	for _, e := range []error{
		errors.New("dial tcp: connection refused"),
		&types.ConditionalCheckFailedException{},
		&types.ResourceNotFoundException{},
		&types.ProvisionedThroughputExceededException{},
	} {
		got := serverFault("op", e)
		if errors.Is(got, ErrServerFault) {
			t.Errorf("%T marked as a server fault", e)
		}
		if got != e {
			t.Errorf("%T was rewrapped: %q", e, got.Error())
		}
	}
	if serverFault("op", nil) != nil {
		t.Error("a nil error became non-nil")
	}
}
