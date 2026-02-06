package messaging

import (
	"context"
	"fmt"
	"log"
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
}

// NewConnection creates a new RabbitMQ connection with retry logic.
func NewConnection(uri string) (*Connection, error) {
	conn := &Connection{
		uri:        uri,
		propagator: otel.GetTextMapPropagator(),
		tracer:     otel.Tracer(tracerName),
	}

	for i := 0; i < maxRetries; i++ {
		err := conn.connect()
		if err == nil {
			log.Println("RabbitMQ connected successfully.")
			go conn.handleReconnect()
			return conn, nil
		}
		log.Printf("Failed to connect to RabbitMQ (attempt %d/%d): %v. Retrying in %v...", i+1, maxRetries, err, reconnectDelay)
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
	for range c.closed {
		log.Println("RabbitMQ connection closed. Attempting to reconnect...")
		for i := 0; i < maxRetries; i++ {
			err := c.connect()
			if err == nil {
				log.Println("RabbitMQ reconnected successfully.")
				break
			}
			log.Printf("Failed to reconnect to RabbitMQ (attempt %d/%d): %v. Retrying in %v...", i+1, maxRetries, err, reconnectDelay)
			time.Sleep(reconnectDelay)
		}
		if c.Conn == nil || c.Conn.IsClosed() {
			log.Fatalf("Permanent failure to reconnect to RabbitMQ after %d retries. Exiting.", maxRetries)
		}
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

	headers := make(amqp.Table)
	carrier := make(propagation.MapCarrier)
	for k, v := range headers {
		if s, ok := v.(string); ok {
			carrier[k] = s
		}
	}
	c.propagator.Inject(ctx, carrier)

	return c.Channel.PublishWithContext(
		ctx,
		exchange,
		routingKey,
		false, // mandatory
		false, // immediate
		amqp.Publishing{
			Headers:     headers,
			ContentType: "application/protobuf", // Assuming Protobuf based on PRD
			Body:        msg,
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
			// Extract OpenTelemetry trace context
			carrier := propagation.MapCarrier{}
			for k, v := range d.Headers {
				if s, ok := v.(string); ok {
					carrier[k] = s
				}
			}
			msgCtx := c.propagator.Extract(ctx, carrier)

			spanCtx, span := c.tracer.Start(msgCtx, "RabbitMQ Consume")

			err := handler(spanCtx, d.Body)
			if err != nil {
				log.Printf("Error processing message: %v. Nacking message...", err)
				d.Nack(false, true) // Requeue
			} else {
				d.Ack(false)
			}
			span.End()
		}
		log.Println("RabbitMQ consumer stopped.")
	}()

	return nil
}

// Close closes the RabbitMQ connection and channel.
func (c *Connection) Close() error {
	if c.Channel != nil && !c.Channel.IsClosed() {
		if err := c.Channel.Close(); err != nil {
			log.Printf("Error closing RabbitMQ channel: %v", err)
		}
	}
	if c.Conn != nil && !c.Conn.IsClosed() {
		if err := c.Conn.Close(); err != nil {
			return fmt.Errorf("error closing RabbitMQ connection: %w", err)
		}
	}
	log.Println("RabbitMQ connection and channel closed.")
	return nil
}
