package myHttp

import (
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"
)

// standard Prometheus HTTP latency buckets in seconds.
var DefaultBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1.0, 2.5, 5.0, 10.0}

// prevent arbitrary client methods and  malformed syntax from corrupting
// the Prometheus text exposition format.
func MethodLabel(method string) string {
	switch method {
	case http.MethodGet:
		return "GET"
	case http.MethodPost:
		return "POST"
	case http.MethodPut:
		return "PUT"
	case http.MethodDelete:
		return "DELETE"
	case http.MethodHead:
		return "HEAD"
	case http.MethodOptions:
		return "OPTIONS"
	case http.MethodPatch:
		return "PATCH"
	default:
		return "UNKNOWN"
	}
}

type ResponseWriterInterceptor struct {
	http.ResponseWriter
	StatusCode   int
	BytesWritten int64
	wroteHeader  bool
}

func NewResponseWriterInterceptor(w http.ResponseWriter) *ResponseWriterInterceptor {
	return &ResponseWriterInterceptor{
		ResponseWriter: w,
		StatusCode:     http.StatusOK, // default to 200 OK if WriteHeader is not explicitly invoked
	}
}

func (r *ResponseWriterInterceptor) WriteHeader(statusCode int) {
	if !r.wroteHeader {
		r.StatusCode = statusCode
		r.wroteHeader = true
		r.ResponseWriter.WriteHeader(statusCode)
	}
}

func (r *ResponseWriterInterceptor) Write(b []byte) (int, error) {
	if !r.wroteHeader {
		r.wroteHeader = true
	}
	n, err := r.ResponseWriter.Write(b)
	r.BytesWritten += int64(n)
	return n, err
}

type metricKey struct {
	Method string
	Route  string
	Code   int
}

type histogramEntry struct {
	buckets []uint64 // cumulative counts corresponding to each bucket boundary
	count   uint64
	sum     float64
}

type MetricsCollector struct {
	mu            sync.RWMutex
	buckets       []float64
	requestsTotal map[metricKey]uint64
	durations     map[metricKey]*histogramEntry
}

func NewMetricsCollector(customBuckets ...float64) *MetricsCollector {
	buckets := DefaultBuckets
	if len(customBuckets) > 0 {
		buckets = make([]float64, len(customBuckets))
		copy(buckets, customBuckets)
		sort.Float64s(buckets)
	}

	return &MetricsCollector{
		buckets:       buckets,
		requestsTotal: make(map[metricKey]uint64),
		durations:     make(map[metricKey]*histogramEntry),
	}
}

func (c *MetricsCollector) Observe(method, route string, code int, duration time.Duration) {
	key := metricKey{
		Method: method,
		Route:  route,
		Code:   code,
	}
	secs := duration.Seconds()

	c.mu.Lock()
	defer c.mu.Unlock()

	c.requestsTotal[key]++

	entry, exists := c.durations[key]
	if !exists {
		entry = &histogramEntry{
			buckets: make([]uint64, len(c.buckets)),
		}
		c.durations[key] = entry
	}

	for i, bound := range c.buckets {
		if secs <= bound {
			entry.buckets[i]++
		}
	}
	entry.count++
	entry.sum += secs
}

func (c *MetricsCollector) WritePrometheus(w io.Writer) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	keys := make([]metricKey, 0, len(c.requestsTotal))
	for k := range c.requestsTotal {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].Route != keys[j].Route {
			return keys[i].Route < keys[j].Route
		}
		if keys[i].Method != keys[j].Method {
			return keys[i].Method < keys[j].Method
		}
		return keys[i].Code < keys[j].Code
	})

	_, _ = fmt.Fprintln(w, "# HELP http_requests_total Total number of HTTP requests processed.")
	_, _ = fmt.Fprintln(w, "# TYPE http_requests_total counter")
	for _, k := range keys {
		count := c.requestsTotal[k]
		_, _ = fmt.Fprintf(w, "http_requests_total{code=\"%d\",method=\"%s\",route=\"%s\"} %d\n",
			k.Code, k.Method, k.Route, count)
	}
	_, _ = fmt.Fprintln(w)

	_, _ = fmt.Fprintln(w, "# HELP http_request_duration_seconds Latency histogram of HTTP requests in seconds.")
	_, _ = fmt.Fprintln(w, "# TYPE http_request_duration_seconds histogram")
	for _, k := range keys {
		entry, ok := c.durations[k]
		if !ok {
			continue
		}

		for i, bound := range c.buckets {
			leStr := strconv.FormatFloat(bound, 'g', -1, 64)
			_, _ = fmt.Fprintf(w, "http_request_duration_seconds_bucket{code=\"%d\",le=\"%s\",method=\"%s\",route=\"%s\"} %d\n",
				k.Code, leStr, k.Method, k.Route, entry.buckets[i])
		}

		_, _ = fmt.Fprintf(w, "http_request_duration_seconds_bucket{code=\"%d\",le=\"+Inf\",method=\"%s\",route=\"%s\"} %d\n",
			k.Code, k.Method, k.Route, entry.count)
		_, _ = fmt.Fprintf(w, "http_request_duration_seconds_sum{code=\"%d\",method=\"%s\",route=\"%s\"} %.6f\n",
			k.Code, k.Method, k.Route, entry.sum)
		_, _ = fmt.Fprintf(w, "http_request_duration_seconds_count{code=\"%d\",method=\"%s\",route=\"%s\"} %d\n",
			k.Code, k.Method, k.Route, entry.count)
	}
}
