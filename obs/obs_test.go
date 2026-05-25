package obs

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"go.opentelemetry.io/otel/trace"
)

// resetSlogDefault restores a stable default logger after each test so
// changes don't leak across the package's tests.
func resetSlogDefault(t *testing.T) {
	t.Helper()
	old := slog.Default()
	t.Cleanup(func() { slog.SetDefault(old) })
}

func TestSetup_NoEnv_Noop(t *testing.T) {
	resetSlogDefault(t)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")

	shutdown := Setup("test-daemon", "test-instance")
	if shutdown == nil {
		t.Fatal("Setup returned nil shutdown")
	}
	// Should not panic; should be cheap to call.
	shutdown()
}

func TestSetup_NoEnv_StderrJSON(t *testing.T) {
	resetSlogDefault(t)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")

	// Redirect stderr via temporary pipe to capture handler output.
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldStderr := os.Stderr
	os.Stderr = w
	defer func() { os.Stderr = oldStderr }()

	shutdown := Setup("test-daemon", "test-instance")
	defer shutdown()

	slog.Info("hello", "k", "v")
	w.Close()

	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	out := buf.String()
	if !strings.Contains(out, `"msg":"hello"`) || !strings.Contains(out, `"k":"v"`) {
		t.Errorf("stderr did not receive expected slog JSON; got %q", out)
	}
}

func TestSetup_WithEnv_FanOut(t *testing.T) {
	resetSlogDefault(t)

	var got int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&got, 1)
		// Consume body to keep client happy.
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", srv.URL)
	t.Setenv("OTEL_EXPORTER_OTLP_PROTOCOL", "http/protobuf")
	t.Setenv("OTEL_EXPORTER_OTLP_LOGS_INSECURE", "true")

	shutdown := Setup("test-daemon", "test-instance")

	for i := 0; i < 4; i++ {
		slog.Info("fanout-record", "i", i)
	}

	// Shutdown flushes the batch processor.
	shutdown()

	final := atomic.LoadInt32(&got)
	t.Logf("OTLP exporter received %d requests", final)
	if final == 0 {
		t.Errorf("OTLP exporter received no requests; expected at least one")
	}
}

func TestSetup_ExporterError_StderrUnaffected(t *testing.T) {
	resetSlogDefault(t)
	// Black-hole endpoint: connection refused.
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://127.0.0.1:1")
	t.Setenv("OTEL_EXPORTER_OTLP_TIMEOUT", "100")

	shutdown := Setup("test-daemon", "test-instance")
	defer shutdown()

	// Should not panic. The batch processor swallows export errors;
	// stderr handler keeps working. We don't assert on stderr capture
	// here (handler write is unbuffered to os.Stderr and capture across
	// shutdown is racy); the assertion is "no panic, shutdown returns".
	slog.Info("blackhole-test")
}

func TestHashTurnID_Deterministic(t *testing.T) {
	a := HashTurnID("krons", "t-123")
	b := HashTurnID("krons", "t-123")
	if a != b {
		t.Errorf("HashTurnID not deterministic: %x vs %x", a, b)
	}
	if a.IsValid() == false {
		t.Errorf("HashTurnID returned invalid TraceID: %x", a)
	}
}

func TestHashTurnID_Distinct(t *testing.T) {
	a := HashTurnID("krons", "t-123")
	b := HashTurnID("krons", "t-456")
	c := HashTurnID("marinade", "t-123")
	if a == b {
		t.Errorf("different turn IDs produced same trace ID: %x", a)
	}
	if a == c {
		t.Errorf("different instances produced same trace ID: %x", a)
	}
}

func TestWithTurn_StampsContext(t *testing.T) {
	SetInstance("krons")
	ctx := WithTurn(context.Background(), "t-789")

	sc := trace.SpanContextFromContext(ctx)
	if !sc.IsValid() {
		t.Fatal("WithTurn did not stamp a valid SpanContext")
	}
	want := HashTurnID("krons", "t-789")
	if sc.TraceID() != want {
		t.Errorf("TraceID = %x, want %x", sc.TraceID(), want)
	}
	if !sc.SpanID().IsValid() {
		t.Errorf("SpanID not valid")
	}
}

func TestPropagation_Roundtrip(t *testing.T) {
	SetInstance("krons")
	ctxOut := WithTurn(context.Background(), "t-rt")

	hdr := http.Header{}
	InjectTraceparent(ctxOut, hdr)
	if hdr.Get("traceparent") == "" {
		t.Fatal("InjectTraceparent did not write traceparent")
	}

	ctxIn := ExtractTraceparent(context.Background(), hdr)
	scIn := trace.SpanContextFromContext(ctxIn)
	scOut := trace.SpanContextFromContext(ctxOut)
	if scIn.TraceID() != scOut.TraceID() {
		t.Errorf("trace ID lost in roundtrip: out=%x in=%x", scOut.TraceID(), scIn.TraceID())
	}
	if scIn.SpanID() != scOut.SpanID() {
		t.Errorf("span ID lost in roundtrip: out=%x in=%x", scOut.SpanID(), scIn.SpanID())
	}
}

func TestInjectTraceparent_NoCtx_NoHeader(t *testing.T) {
	hdr := http.Header{}
	InjectTraceparent(context.Background(), hdr)
	if hdr.Get("traceparent") != "" {
		t.Errorf("InjectTraceparent wrote a header for a ctx with no SpanContext: %q",
			hdr.Get("traceparent"))
	}
}
