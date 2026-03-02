package aws

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
)

const (
	// MetricNamespace is the CloudWatch namespace for OCFP metrics.
	MetricNamespace = "OCFP/AWS"
	// MetricDimensionProvider is the provider dimension name.
	MetricDimensionProvider = "Provider"
	// MetricDimensionOperation is the operation dimension name.
	MetricDimensionOperation = "Operation"
	// MetricDimensionRegion is the region dimension name.
	MetricDimensionRegion = "Region"
	// DefaultMetricBufferSize is the default buffer size for batching metrics.
	DefaultMetricBufferSize = 20
	// DefaultFlushTimeout is the default timeout for flushing metrics.
	DefaultFlushTimeout = 10 * time.Second
	// MaxMetricBatchSize is the maximum batch size for CloudWatch metrics.
	MaxMetricBatchSize = 20
)

// MetricsCollector collects and publishes metrics to CloudWatch.
type MetricsCollector struct {
	client    *cloudwatch.Client
	namespace string
	region    string
	enabled   bool
	mu        sync.RWMutex

	// Buffered metrics
	metrics []types.MetricDatum
	bufSize int
}

// NewMetricsCollector creates a new metrics collector.
func NewMetricsCollector(awsConfig aws.Config, namespace, region string) *MetricsCollector {
	return &MetricsCollector{
		client:    cloudwatch.NewFromConfig(awsConfig),
		namespace: namespace,
		region:    region,
		enabled:   true,
		metrics:   make([]types.MetricDatum, 0),
		bufSize:   DefaultMetricBufferSize,
	}
}

// RecordOperationDuration records the duration of an operation.
func (m *MetricsCollector) RecordOperationDuration(operation string, duration time.Duration) {
	if !m.enabled {
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	m.metrics = append(m.metrics, types.MetricDatum{
		MetricName: aws.String("OperationDuration"),
		Value:      aws.Float64(float64(duration.Milliseconds())),
		Unit:       types.StandardUnitMilliseconds,
		Timestamp:  aws.Time(time.Now()),
		Dimensions: []types.Dimension{
			{
				Name:  aws.String(MetricDimensionProvider),
				Value: aws.String("aws"),
			},
			{
				Name:  aws.String(MetricDimensionOperation),
				Value: aws.String(operation),
			},
			{
				Name:  aws.String(MetricDimensionRegion),
				Value: aws.String(m.region),
			},
		},
	})

	m.checkAndFlush()
}

// RecordOperationCount records an operation occurrence.
func (m *MetricsCollector) RecordOperationCount(operation string, count int) {
	if !m.enabled {
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	m.metrics = append(m.metrics, types.MetricDatum{
		MetricName: aws.String("OperationCount"),
		Value:      aws.Float64(float64(count)),
		Unit:       types.StandardUnitCount,
		Timestamp:  aws.Time(time.Now()),
		Dimensions: []types.Dimension{
			{
				Name:  aws.String(MetricDimensionProvider),
				Value: aws.String("aws"),
			},
			{
				Name:  aws.String(MetricDimensionOperation),
				Value: aws.String(operation),
			},
			{
				Name:  aws.String(MetricDimensionRegion),
				Value: aws.String(m.region),
			},
		},
	})

	m.checkAndFlush()
}

// RecordError records an error occurrence.
func (m *MetricsCollector) RecordError(operation string, errorCode ErrorCode) {
	if !m.enabled {
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	m.metrics = append(m.metrics, types.MetricDatum{
		MetricName: aws.String("ErrorCount"),
		Value:      aws.Float64(1),
		Unit:       types.StandardUnitCount,
		Timestamp:  aws.Time(time.Now()),
		Dimensions: []types.Dimension{
			{
				Name:  aws.String(MetricDimensionProvider),
				Value: aws.String("aws"),
			},
			{
				Name:  aws.String(MetricDimensionOperation),
				Value: aws.String(operation),
			},
			{
				Name:  aws.String(MetricDimensionRegion),
				Value: aws.String(m.region),
			},
			{
				Name:  aws.String("ErrorCode"),
				Value: aws.String(string(errorCode)),
			},
		},
	})

	m.checkAndFlush()
}

// RecordRetry records a retry occurrence.
func (m *MetricsCollector) RecordRetry(operation string, attempt int) {
	if !m.enabled {
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	m.metrics = append(m.metrics, types.MetricDatum{
		MetricName: aws.String("RetryCount"),
		Value:      aws.Float64(float64(attempt)),
		Unit:       types.StandardUnitCount,
		Timestamp:  aws.Time(time.Now()),
		Dimensions: []types.Dimension{
			{
				Name:  aws.String(MetricDimensionProvider),
				Value: aws.String("aws"),
			},
			{
				Name:  aws.String(MetricDimensionOperation),
				Value: aws.String(operation),
			},
			{
				Name:  aws.String(MetricDimensionRegion),
				Value: aws.String(m.region),
			},
		},
	})

	m.checkAndFlush()
}

// RecordCircuitBreakerState records circuit breaker state changes.
func (m *MetricsCollector) RecordCircuitBreakerState(operation string, state CircuitBreakerState) {
	if !m.enabled {
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	var stateValue float64

	switch state {
	case StateClosed:
		stateValue = 0
	case StateOpen:
		stateValue = 1
	case StateHalfOpen:
		stateValue = 0.5
	}

	m.metrics = append(m.metrics, types.MetricDatum{
		MetricName: aws.String("CircuitBreakerState"),
		Value:      aws.Float64(stateValue),
		Unit:       types.StandardUnitNone,
		Timestamp:  aws.Time(time.Now()),
		Dimensions: []types.Dimension{
			{
				Name:  aws.String(MetricDimensionProvider),
				Value: aws.String("aws"),
			},
			{
				Name:  aws.String(MetricDimensionOperation),
				Value: aws.String(operation),
			},
			{
				Name:  aws.String(MetricDimensionRegion),
				Value: aws.String(m.region),
			},
		},
	})

	m.checkAndFlush()
}

// Flush flushes all buffered metrics to CloudWatch.
func (m *MetricsCollector) Flush(ctx context.Context) error {
	m.mu.Lock()
	metricsToFlush := m.metrics
	m.metrics = make([]types.MetricDatum, 0, m.bufSize)
	m.mu.Unlock()

	if len(metricsToFlush) == 0 {
		return nil
	}

	return m.flush(ctx, metricsToFlush)
}

// Enable enables metrics collection.
func (m *MetricsCollector) Enable() {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.enabled = true

	logger.Debug("Metrics collection enabled")
}

// Disable disables metrics collection.
func (m *MetricsCollector) Disable() {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.enabled = false

	logger.Debug("Metrics collection disabled")
}

// IsEnabled returns whether metrics collection is enabled.
func (m *MetricsCollector) IsEnabled() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return m.enabled
}

// checkAndFlush flushes metrics if buffer is full.
// Must be called with lock held.
func (m *MetricsCollector) checkAndFlush() {
	if len(m.metrics) >= m.bufSize {
		go m.flushAsync()
	}
}

// flushAsync flushes metrics asynchronously.
func (m *MetricsCollector) flushAsync() {
	m.mu.Lock()
	metricsToFlush := m.metrics
	m.metrics = make([]types.MetricDatum, 0, m.bufSize)
	m.mu.Unlock()

	if len(metricsToFlush) == 0 {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), DefaultFlushTimeout)
	defer cancel()

	err := m.flush(ctx, metricsToFlush)
	if err != nil {
		logger.Warn("Failed to flush metrics to CloudWatch",
			"error", err.Error(),
			"metric_count", len(metricsToFlush))
	}
}

// flush sends metrics to CloudWatch.
func (m *MetricsCollector) flush(ctx context.Context, metrics []types.MetricDatum) error {
	if len(metrics) == 0 {
		return nil
	}

	for batchIdx := 0; batchIdx < len(metrics); batchIdx += MaxMetricBatchSize {
		end := batchIdx + MaxMetricBatchSize
		if end > len(metrics) {
			end = len(metrics)
		}

		batch := metrics[batchIdx:end]

		_, err := m.client.PutMetricData(ctx, &cloudwatch.PutMetricDataInput{
			Namespace:  aws.String(m.namespace),
			MetricData: batch,
		})
		if err != nil {
			return fmt.Errorf("failed to put metric data: %w", err)
		}

		logger.Debug("Flushed metrics to CloudWatch",
			"count", len(batch),
			"namespace", m.namespace)
	}

	return nil
}

// OperationTimer helps measure operation duration.
type OperationTimer struct {
	collector *MetricsCollector
	operation string
	startTime time.Time
}

// NewOperationTimer creates a new operation timer.
func NewOperationTimer(collector *MetricsCollector, operation string) *OperationTimer {
	return &OperationTimer{
		collector: collector,
		operation: operation,
		startTime: time.Now(),
	}
}

// Stop stops the timer and records the duration.
func (t *OperationTimer) Stop() {
	if t.collector != nil {
		duration := time.Since(t.startTime)
		t.collector.RecordOperationDuration(t.operation, duration)
	}
}

// StopWithError stops the timer, records the duration, and records the error if present.
func (t *OperationTimer) StopWithError(err error) {
	t.Stop()

	if err != nil && t.collector != nil {
		var awsErr *AWSError
		if errors.As(err, &awsErr) {
			t.collector.RecordError(t.operation, awsErr.Code)
		} else {
			t.collector.RecordError(t.operation, ErrCodeInternalError)
		}
	}
}
