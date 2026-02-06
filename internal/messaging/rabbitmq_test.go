package messaging_test

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"SkyEye/internal/messaging"

	"github.com/ory/dockertest/v3"
	"github.com/ory/dockertest/v3/docker"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.21.0"
)

var (
	rabbitMQConn *messaging.Connection
	rabbitMQURI  string
	pool         *dockertest.Pool
	dockerRes    *dockertest.Resource
	exporter     *memoryExporter
	logger       *slog.Logger
)

type memoryExporter struct {
	mu    sync.Mutex
	spans []sdktrace.ReadOnlySpan
}

func (e *memoryExporter) ExportSpans(ctx context.Context, spans []sdktrace.ReadOnlySpan) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.spans = append(e.spans, spans...)
	return nil
}

func (e *memoryExporter) Shutdown(ctx context.Context) error {
	e.reset()
	return nil
}

func (e *memoryExporter) getSpans() []sdktrace.ReadOnlySpan {
	e.mu.Lock()
	defer e.mu.Unlock()
	spans := make([]sdktrace.ReadOnlySpan, len(e.spans))
	copy(spans, e.spans)
	return spans
}

func (e *memoryExporter) reset() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.spans = nil
}

func TestMain(m *testing.M) {
	logger = slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))
	var err error

	exporter = &memoryExporter{}
	setupOpenTelemetry(exporter)

	pool, err = dockertest.NewPool("")
	if err != nil {
		logger.Error("Could not connect to docker", "error", err)
		os.Exit(1)
	}

	opts := &dockertest.RunOptions{
		Repository:   "rabbitmq",
		Tag:          "3.13-management-alpine",
		ExposedPorts: []string{"5672/tcp"},
		Env:          []string{"RABBITMQ_DEFAULT_USER=guest", "RABBITMQ_DEFAULT_PASS=guest"},
	}

	dockerRes, err = pool.RunWithOptions(opts, func(config *docker.HostConfig) {
		config.AutoRemove = true
		config.RestartPolicy = docker.RestartPolicy{Name: "no"}
	})
	if err != nil {
		logger.Error("Could not start resource", "error", err)
		os.Exit(1)
	}

	rabbitMQURI = fmt.Sprintf("amqp://guest:guest@%s", dockerRes.GetHostPort("5672/tcp"))
	logger.Info("RabbitMQ running at", "uri", rabbitMQURI)

	if err = pool.Retry(func() error {
		var connErr error
		rabbitMQConn, connErr = messaging.NewConnection(rabbitMQURI)
		return connErr
	}); err != nil {
		logger.Error("Could not connect to RabbitMQ", "error", err)
		if pErr := pool.Purge(dockerRes); pErr != nil {
			logger.Error("Could not purge resource", "error", pErr)
		}
		os.Exit(1)
	}

	code := m.Run()

	if err = pool.Purge(dockerRes); err != nil {
		logger.Error("Could not purge resource", "error", err)
	}

	os.Exit(code)
}

func setupOpenTelemetry(exp sdktrace.SpanExporter) {
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp),
		sdktrace.WithResource(resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceNameKey.String("test-messaging-service"),
		)),
	)
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))
}

func TestPublishAndConsumeWithTracePropagation(t *testing.T) {
	exporter.reset()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	queueName := "test_trace_queue"
	messageBody := []byte("Hello, Tracing!")

	_, err := rabbitMQConn.Channel.QueueDeclare(queueName, true, false, false, false, nil)
	require.NoError(t, err, "Failed to declare queue")

	var wg sync.WaitGroup
	wg.Add(1)

	handler := func(msgCtx context.Context, body []byte) error {
		defer wg.Done()
		assert.Equal(t, messageBody, body)
		_, span := otel.Tracer("test-consumer").Start(msgCtx, "consume-handler")
		defer span.End()
		return nil
	}

	err = rabbitMQConn.Consume(ctx, queueName, handler)
	require.NoError(t, err, "Consume call failed")

	parentCtx, parentSpan := otel.Tracer("test-publisher").Start(ctx, "publish-operation")
	err = rabbitMQConn.Publish(parentCtx, "", queueName, messageBody)
	require.NoError(t, err, "Publish call failed")
	parentSpan.End()

	wg.Wait()

	spans := exporter.getSpans()
	require.Len(t, spans, 3, "Expected exactly three spans to be created")

	var producerSpan, consumerSpan, handlerSpan sdktrace.ReadOnlySpan
	for _, s := range spans {
		switch s.Name() {
		case "publish-operation":
			producerSpan = s
		case "RabbitMQ Consume":
			consumerSpan = s
		case "consume-handler":
			handlerSpan = s
		}
	}

	require.NotNil(t, producerSpan, "Producer span not found")
	require.NotNil(t, consumerSpan, "Consumer span not found")
	require.NotNil(t, handlerSpan, "Handler span not found")

	assert.Equal(t, producerSpan.SpanContext().TraceID(), consumerSpan.SpanContext().TraceID())
	assert.Equal(t, producerSpan.SpanContext().SpanID(), consumerSpan.Parent().SpanID())

	assert.Equal(t, consumerSpan.SpanContext().TraceID(), handlerSpan.SpanContext().TraceID())
	assert.Equal(t, consumerSpan.SpanContext().SpanID(), handlerSpan.Parent().SpanID())
}

func TestConnectionClose(t *testing.T) {
	tempConn, err := messaging.NewConnection(rabbitMQURI)
	require.NoError(t, err, "Failed to create temporary connection for close test")

	err = tempConn.Close()
	assert.NoError(t, err, "Failed to close connection")

	err = tempConn.Publish(context.Background(), "", "some_queue", []byte("test"))
	assert.Error(t, err, "Expected publish to fail on closed connection")
	assert.Equal(t, "RabbitMQ connection is closed", err.Error())
}
