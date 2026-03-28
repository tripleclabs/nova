package novatest

import "time"

// LinkOption configures a Degrade call.
type LinkOption func(*linkOpts)

type linkOpts struct {
	latency time.Duration
	jitter  time.Duration
	loss    float64
}

// WithLatency sets the one-way latency for a degraded link.
func WithLatency(d time.Duration) LinkOption {
	return func(o *linkOpts) { o.latency = d }
}

// WithJitter sets the jitter range for a degraded link.
func WithJitter(d time.Duration) LinkOption {
	return func(o *linkOpts) { o.jitter = d }
}

// WithLoss sets the packet loss probability (0.0–1.0) for a degraded link.
func WithLoss(loss float64) LinkOption {
	return func(o *linkOpts) { o.loss = loss }
}
