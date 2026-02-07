package telemetry_test

import (
	"context"
	"testing"

	"SkyEye/internal/telemetry"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSetupTracer(t *testing.T) {
	// This is a basic test to ensure the tracer setup function runs without
	// panicking and returns a valid shutdown function. A real test would require
	// an OpenTelemetry collector or an in-memory exporter.
	
	// The test will fail if it cannot connect to an OTLP endpoint.
	// For local testing, an OTel collector should be running.
	// In CI, this test might need to be skipped or run with a mock exporter.
	t.Skip("Skipping tracer test, which requires a running OTLP collector")

	ctx := context.Background()
	shutdown, err := telemetry.SetupTracer(ctx, "test-service", "1.0.0")

	require.NoError(t, err)
	assert.NotNil(t, shutdown)

	// Verify shutdown function can be called without error
	err = shutdown(ctx)
	assert.NoError(t, err)
}
