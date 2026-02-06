package messaging_test

import (
	"context"
	"fmt"
	"log"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/ory/dockertest/v3"
	"github.com/ory/dockertest/v3/docker"
	"SkyEye/internal/messaging" // Import our messaging package

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.7.0"
	otelpropagation "go.opentelemetry.io/otel/propagation"
)

var (
	rabbitMQConn *messaging.Connection
	rabbitMQURI  string
	pool         *dockertest.Pool
	dockerResource *dockertest.Resource
)

func TestMain(m *testing.M) {
	var err error

	// Setup OpenTelemetry for testing
	setupOpenTelemetry()

	// Connect to Docker
	pool, err = dockertest.NewPool("")
	if err != nil {
		log.Fatalf("Could not connect to docker: %s", err)
	}

	// Pull RabbitMQ image and run container
	dockerResource, err = pool.RunWithOptions(&dockertest.RunOptions{
		Repository: "rabbitmq",
		Tag:        "3.13-management-alpine",
		ExposedPorts: []string{"5672/tcp", "15672/tcp"},
		Env: []string{
			"RABBITMQ_DEFAULT_USER=guest",
			"RABBITMQ_DEFAULT_PASS=guest",
		},
	}, func(config *docker.HostConfig) {
		// Set AutoRemove to true so that stopped container goes away by itself.
		config.AutoRemove = true
		config.RestartPolicy = docker.RestartPolicy{Name: "no"}
	})
	if err != nil {
		log.Fatalf("Could not start resource: %s", err)
	}

	// Set the URI for RabbitMQ
	rabbitMQURI = fmt.Sprintf("amqp://guest:guest@localhost:%s/", dockerResource.GetPort("5672/tcp"))
		
	// Retry connecting to RabbitMQ
	if err = pool.Retry(func() error {
		var connErr error
		rabbitMQConn, connErr = messaging.NewConnection(rabbitMQURI)
		if connErr != nil {
			return connErr
		}
		// Try to open a channel to confirm connection is fully ready
		// Accessing the exported Channel field
		if rabbitMQConn.Channel == nil { 
			return fmt.Errorf("channel not initialized")
		}
		return nil // Connection and channel are ready
	}); err != nil {
		log.Fatalf("Could not connect to RabbitMQ after retries: %s", err)
	}

	// Run tests
	code := m.Run()

	// Clean up
	if err = pool.Purge(dockerResource); err != nil {
		log.Fatalf("Could not purge resource: %s", err)
	}

	os.Exit(code)
}

func setupOpenTelemetry() {
	exporter, err := stdouttrace.New(stdouttrace.WithPrettyPrint())
	if err != nil {
		log.Fatalf("Failed to create stdout exporter: %v", err)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
		sdktrace.WithBatchProcessor(sdktrace.NewSimpleProcessor(exporter)),
		sdktrace.WithResource(resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceNameKey.String("test-messaging-service"),
			attribute.String("environment", "test"),
		)),
	)
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(otelpropagation.TraceContext{})
}

func TestPublishAndConsume(t *testing.T) {
	ctx := context.Background()
	queueName := "test_queue"
	messageBody := []byte("Hello, RabbitMQ!")

	// Declare a queue using the exported Channel
	_, err := rabbitMQConn.Channel.QueueDeclare(
		queueName,
		false, // durable
		false, // delete when unused
		false, // exclusive
		false, // no-wait
		nil,   // arguments
	)
	if err != nil {
		t.Fatalf("Failed to declare a queue: %v", err)
	}

	var wg sync.WaitGroup
	wg.Add(1)

	// Start consuming
	consumedMessages := make(chan []byte, 1)
	err = rabbitMQConn.Consume(ctx, queueName, func(msgCtx context.Context, body []byte) error {
		defer wg.Done()
		_, span := otel.Tracer("test").Start(msgCtx, "test consume handler")
		defer span.End()

		consumedMessages <- body
		return nil
	})
	if err != nil {
		t.Fatalf("Failed to start consumer: %v", err)
	}

	// Publish a message
	parentCtx, parentSpan := otel.Tracer("test").Start(ctx, "test publish operation")
	err = rabbitMQConn.Publish(parentCtx, "", queueName, messageBody)
	parentSpan.End()
	if err != nil {
		t.Fatalf("Failed to publish message: %v", err)
	}

	select {
	case consumed := <-consumedMessages:
		if string(consumed) != string(messageBody) {
			t.Errorf("Consumed message mismatch: got %s, want %s", string(consumed), string(messageBody))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Message consumption timed out")
	}

	wg.Wait() // Wait for the consumer handler to finish
}

func TestConnectionClose(t *testing.T) {
	// Re-establish a connection for this test
	tempConn, err := messaging.NewConnection(rabbitMQURI)
	if err != nil {
		t.Fatalf("Failed to create temporary connection: %v", err)
	}

	err = tempConn.Close()
	if err != nil {
		t.Errorf("Failed to close connection: %v", err)
	}

	// Verify that subsequent operations fail
	err = tempConn.Publish(context.Background(), "", "some_queue", []byte("test"))
	if err == nil {
		t.Errorf("Expected publish to fail on closed connection, but it succeeded")
	}
	expectedErr := "RabbitMQ connection is closed"
	if err != nil && err.Error() != expectedErr {
		t.Errorf("Expected error '%s', got '%s'", expectedErr, err.Error())
	}
}

// TODO: Add tests for retry logic and DLX (if implemented)
