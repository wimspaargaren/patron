// Package sqs provides a set of common interfaces and structs for publishing messages to AWS SQS. Implementations
// in this package also include distributed tracing capabilities by default.
package sqs

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/sqs"
	"github.com/aws/aws-sdk-go/service/sqs/sqsiface"
	"github.com/beatlabs/patron/log"
	"github.com/beatlabs/patron/trace"
	"github.com/opentracing/opentracing-go"
	"github.com/opentracing/opentracing-go/ext"
	"github.com/prometheus/client_golang/prometheus"
)

const (
	publisherComponent      = "sqs-publisher"
	attributeDataTypeString = "String"
)

var publishDurationMetrics *prometheus.HistogramVec

func init() {
	publishDurationMetrics = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: "client",
			Subsystem: "sqs",
			Name:      "publish_duration_seconds",
			Help:      "AWS SQS publish completed by the client.",
		},
		[]string{"queue", "success"},
	)
	prometheus.MustRegister(publishDurationMetrics)
}

// Publisher is a wrapper with added distributed tracing capabilities.
type Publisher struct {
	api sqsiface.SQSAPI
}

// New creates a new SQS publisher.
func New(api sqsiface.SQSAPI) (Publisher, error) {
	if api == nil {
		return Publisher{}, errors.New("missing api")
	}

	return Publisher{api: api}, nil
}

// Publish tries to publish a new message to SQS. It also stores tracing information.
func (p Publisher) Publish(ctx context.Context, msg *sqs.SendMessageInput) (messageID string, err error) {
	span, _ := trace.ChildSpan(ctx, trace.ComponentOpName(publisherComponent, *msg.QueueUrl), publisherComponent, ext.SpanKindProducer)

	if err := injectHeaders(span, msg); err != nil {
		log.FromContext(ctx).Errorf("failed to inject trace headers: %v", err)
	}

	start := time.Now()
	out, err := p.api.SendMessageWithContext(ctx, msg)
	observePublish(span, start, *msg.QueueUrl, err)
	if err != nil {
		return "", fmt.Errorf("failed to publish message: %w", err)
	}

	if out.MessageId == nil {
		return "", errors.New("tried to publish a message but no message ID returned")
	}

	return *out.MessageId, nil
}

type sqsHeadersCarrier map[string]interface{}

// Set implements Set() of opentracing.TextMapWriter.
func (c sqsHeadersCarrier) Set(key, val string) {
	c[key] = val
}

// injectHeaders injects the SQS headers carrier's headers into the message's attributes.
func injectHeaders(span opentracing.Span, input *sqs.SendMessageInput) error {
	carrier := sqsHeadersCarrier{}
	if err := span.Tracer().Inject(span.Context(), opentracing.TextMap, &carrier); err != nil {
		return fmt.Errorf("failed to inject tracing headers: %w", err)
	}
	if input.MessageAttributes == nil {
		input.MessageAttributes = make(map[string]*sqs.MessageAttributeValue)
	}

	for k, v := range carrier {
		input.MessageAttributes[k] = &sqs.MessageAttributeValue{
			DataType:    aws.String(attributeDataTypeString),
			StringValue: aws.String(v.(string)),
		}
	}
	return nil
}

func observePublish(span opentracing.Span, start time.Time, queue string, err error) {
	trace.SpanComplete(span, err)
	publishDurationMetrics.WithLabelValues(queue, strconv.FormatBool(err != nil)).Observe(time.Since(start).Seconds())
}
