// Package v2 provides a client with included tracing capabilities.
package v2

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/beatlabs/patron/correlation"
	patronerrors "github.com/beatlabs/patron/errors"
	"github.com/beatlabs/patron/log"
	"github.com/beatlabs/patron/trace"
	opentracing "github.com/opentracing/opentracing-go"
	"github.com/opentracing/opentracing-go/ext"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/streadway/amqp"
)

const (
	publisherComponent = "amqp-publisher"
)

var publishDurationMetrics *prometheus.HistogramVec

func init() {
	publishDurationMetrics = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: "client",
			Subsystem: "amqp",
			Name:      "publish_duration_seconds",
			Help:      "AMQP publish completed by the client.",
		},
		[]string{"exchange", "success"},
	)
	prometheus.MustRegister(publishDurationMetrics)
}

// Publisher defines a RabbitMQ publisher with tracing instrumentation.
type Publisher struct {
	cfg        *amqp.Config
	connection *amqp.Connection
	channel    *amqp.Channel
}

// New constructor.
func New(url string, oo ...OptionFunc) (*Publisher, error) {
	if url == "" {
		return nil, errors.New("url is required")
	}

	var err error
	pub := &Publisher{}

	for _, option := range oo {
		err = option(pub)
		if err != nil {
			return nil, err
		}
	}

	var conn *amqp.Connection

	if pub.cfg == nil {
		conn, err = amqp.Dial(url)
	} else {
		conn, err = amqp.DialConfig(url, *pub.cfg)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to open connection: %w", err)
	}

	ch, err := conn.Channel()
	if err != nil {
		return nil, patronerrors.Aggregate(fmt.Errorf("failed to open channel: %w", err), conn.Close())
	}

	pub.connection = conn
	pub.channel = ch
	return pub, nil
}

// Publish a message to a exchange.
func (tc *Publisher) Publish(ctx context.Context, exchange, key string, mandatory, immediate bool, msg amqp.Publishing) error {
	sp, _ := trace.ChildSpan(ctx, trace.ComponentOpName(publisherComponent, exchange),
		publisherComponent, ext.SpanKindProducer, opentracing.Tag{Key: "exchange", Value: exchange})

	if msg.Headers == nil {
		msg.Headers = amqp.Table{}
	}

	c := amqpHeadersCarrier(msg.Headers)

	if err := sp.Tracer().Inject(sp.Context(), opentracing.TextMap, c); err != nil {
		log.FromContext(ctx).Errorf("failed to inject tracing headers: %v", err)
	}
	msg.Headers[correlation.HeaderID] = correlation.IDFromContext(ctx)

	start := time.Now()
	err := tc.channel.Publish(exchange, key, mandatory, immediate, msg)

	observePublish(sp, start, exchange, err)
	if err != nil {
		return fmt.Errorf("failed to publish message: %w", err)
	}

	return nil
}

// Close the channel and connection.
func (tc *Publisher) Close() error {
	return patronerrors.Aggregate(tc.channel.Close(), tc.connection.Close())
}

type amqpHeadersCarrier map[string]interface{}

// Set implements Set() of opentracing.TextMapWriter.
func (c amqpHeadersCarrier) Set(key, val string) {
	c[key] = val
}

func observePublish(span opentracing.Span, start time.Time, exchange string, err error) {
	trace.SpanComplete(span, err)
	publishDurationMetrics.WithLabelValues(exchange, strconv.FormatBool(err != nil)).Observe(time.Since(start).Seconds())
}
