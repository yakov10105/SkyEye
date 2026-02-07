package messaging

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

const (
	reconnectDelay = 5 * time.Second
	maxRetries     = 10
	tracerName     = "skyeye/messaging"
)

// Connection holds the AMQP connection and channel
type Connection struct {
	Conn       *amqp.Connection
	Channel    *amqp.Channel
	uri        string
	closed     chan *amqp.Error
	propagator propagation.TextMapPropagator
	tracer     trace.Tracer
	logger     *slog.Logger
}

// NewConnection creates a new RabbitMQ connection with retry logic.
func NewConnection(uri string) (*Connection, error) {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	conn := &Connection{
		uri:        uri,
		propagator: otel.GetTextMapPropagator(),
		tracer:     otel.Tracer(tracerName),
		logger:     logger,
	}

	for i := 0; i < maxRetries; i++ {
		err := conn.connect()
		if err == nil {
			conn.logger.Info("RabbitMQ connected successfully.")
			go conn.handleReconnect()
			return conn, nil
		}
		conn.logger.Error("Failed to connect to RabbitMQ", "attempt", i+1, "max_attempts", maxRetries, "error", err, "retry_in", reconnectDelay)
		time.Sleep(reconnectDelay)
	}
	return nil, fmt.Errorf("failed to connect to RabbitMQ after %d retries", maxRetries)
}

// connect establishes the AMQP connection and channel.
func (c *Connection) connect() error {
	var err error
	c.Conn, err = amqp.Dial(c.uri)
	if err != nil {
		return fmt.Errorf("failed to dial RabbitMQ: %w", err)
	}

	c.Channel, err = c.Conn.Channel()
	if err != nil {
		c.Conn.Close()
		return fmt.Errorf("failed to open channel: %w", err)
	}

	c.closed = make(chan *amqp.Error)
	c.Conn.NotifyClose(c.closed)
	return nil
}

// handleReconnect listens for connection closures and attempts to reconnect.
func (c *Connection) handleReconnect() {
	for err := range c.closed {
		if err != nil {
			c.logger.Warn("RabbitMQ connection closed", "error", err.Error())
		} else {
			c.logger.Warn("RabbitMQ connection closed gracefully")
		}

		c.logger.Info("Attempting to reconnect to RabbitMQ...")
		for i := 0; i < maxRetries; i++ {
			err := c.connect()
			if err == nil {
				c.logger.Info("RabbitMQ reconnected successfully.")
				return // Exit the reconnect loop on success
			}
			c.logger.Error("Failed to reconnect to RabbitMQ", "attempt", i+1, "max_attempts", maxRetries, "error", err, "retry_in", reconnectDelay)
			time.Sleep(reconnectDelay)
		}
		c.logger.Error("Fatal: could not reconnect to RabbitMQ after multiple retries. Further action may be needed.")
		// In a real app, you might want to exit, or have a more robust health check system.
		// For now, we will wait for the next connection closure and try again.
	}
}

// Publish sends a message to the specified exchange.
func (c *Connection) Publish(ctx context.Context, exchange, routingKey string, msg []byte) error {
	if c.Conn == nil || c.Conn.IsClosed() {
		return fmt.Errorf("RabbitMQ connection is closed")
	}
	if c.Channel == nil {
		return fmt.Errorf("RabbitMQ channel is not open")
	}

	carrier := make(propagation.MapCarrier)
	c.propagator.Inject(ctx, carrier)

	headers := make(amqp.Table)
	for k, v := range carrier {
		headers[k] = v
	}

	return c.Channel.PublishWithContext(
		ctx,
		exchange,
		routingKey,
		false, // mandatory
		false, // immediate
		amqp.Publishing{
			Headers:     headers,
			ContentType: "application/protobuf",
			Body:        msg,
			DeliveryMode: amqp.Persistent, // Ensure messages are not lost on restart
		},
	)
}

// Consume starts consuming messages from a queue.
func (c *Connection) Consume(ctx context.Context, queue string, handler func(context.Context, []byte) error) error {
	if c.Conn == nil || c.Conn.IsClosed() {
		return fmt.Errorf("RabbitMQ connection is closed")
	}
	if c.Channel == nil {
		return fmt.Errorf("RabbitMQ channel is not open")
	}

	msgs, err := c.Channel.Consume(
		queue,
		"",    // consumer
		false, // auto-ack
		false, // exclusive
		false, // no-local
		false, // no-wait
		nil,   // args
	)
	if err != nil {
		return fmt.Errorf("failed to register consumer: %w", err)
	}

	go func() {
		for d := range msgs {
			carrier := make(propagation.MapCarrier)
			for k, v := range d.Headers {
				if s, ok := v.(string); ok {
					carrier[k] = s
				}
			}
			msgCtx := c.propagator.Extract(ctx, carrier)

			_, span := c.tracer.Start(msgCtx, "RabbitMQ Consume", trace.WithSpanKind(trace.SpanKindConsumer))
			
			err := handler(msgCtx, d.Body)
			if err != nil {
				c.logger.Error("Error processing message, nacking", "error", err)
				d.Nack(false, false) // Nack and don't requeue, send to DLX if configured
			} else {
				d.Ack(false)
			}
			span.End()
		}
		c.logger.Info("RabbitMQ consumer stopped.")
	}()

	return nil
}


// Close closes the RabbitMQ connection and channel.
func (c *Connection) Close() error {
	if c.Conn != nil && !c.Conn.IsClosed() {
		// Stop listening for close notifications before explicitly closing
		// to avoid triggering the reconnect logic.
		close(c.closed)
		
		if err := c.Channel.Close(); err != nil {
			c.logger.Error("Error closing RabbitMQ channel", "error", err)
		}
		if err := c.Conn.Close(); err != nil {
			return fmt.Errorf("error closing RabbitMQ connection: %w", err)
		}
		c.logger.Info("RabbitMQ connection and channel closed successfully.")
		return nil
	}
	c.logger.Info("RabbitMQ connection already closed.")
	return nil
}
