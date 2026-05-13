// Production adapters wrapping *amqp.Connection / *amqp.Channel as
// AMQPConnection / AMQPChannel. They're pure passthroughs; the
// behavior they implement ("dial a real broker, then forward calls")
// can only be exercised against real RabbitMQ, which the integration
// tests do. This file is excluded from the unit-test coverage gate
// in .testcoverage.yml; the test (rabbitmq integration) CI job is
// what enforces these adapters work.
package rabbitmq

import (
	amqp "github.com/rabbitmq/amqp091-go"
)

// defaultDialer wraps the real *amqp.Connection in our AMQPConnection
// interface so tests can swap it out without changing the surface.
func defaultDialer(url string) (AMQPConnection, error) {
	c, err := amqp.Dial(url)
	if err != nil {
		return nil, err
	}
	return &realConn{c: c}, nil
}

type realConn struct{ c *amqp.Connection }

func (r *realConn) Channel() (AMQPChannel, error) {
	ch, err := r.c.Channel()
	if err != nil {
		return nil, err
	}
	return &realChan{ch: ch}, nil
}
func (r *realConn) Close() error   { return r.c.Close() }
func (r *realConn) IsClosed() bool { return r.c.IsClosed() }

type realChan struct{ ch *amqp.Channel }

func (r *realChan) Qos(prefetchCount, prefetchSize int, global bool) error {
	return r.ch.Qos(prefetchCount, prefetchSize, global)
}
func (r *realChan) Consume(queue, consumer string, autoAck, exclusive, noLocal, noWait bool, args amqp.Table) (<-chan amqp.Delivery, error) {
	return r.ch.Consume(queue, consumer, autoAck, exclusive, noLocal, noWait, args)
}
func (r *realChan) Close() error { return r.ch.Close() }
