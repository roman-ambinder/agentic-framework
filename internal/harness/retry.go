package harness

import "time"

// Defaults of RetryPolicy for zero (or negative) fields.
const (
	defaultMaxAttempts  = 3
	defaultInitialDelay = time.Second
	defaultMaxDelay     = 30 * time.Second
)

// RetryPolicy controls retries of failed model calls.
type RetryPolicy struct {
	// MaxAttempts is the total number of attempts, including the first.
	// 0 means 3. 1 means no retries.
	MaxAttempts int
	// InitialDelay is the wait after the first failed attempt. 0 means 1s.
	InitialDelay time.Duration
	// MaxDelay caps every wait. 0 means 30s.
	MaxDelay time.Duration
}

// withDefaults returns p with zero and negative fields replaced by the defaults.
func (p RetryPolicy) withDefaults() RetryPolicy {
	if p.MaxAttempts <= 0 {
		p.MaxAttempts = defaultMaxAttempts
	}
	if p.InitialDelay <= 0 {
		p.InitialDelay = defaultInitialDelay
	}
	if p.MaxDelay <= 0 {
		p.MaxDelay = defaultMaxDelay
	}
	return p
}

// Delay returns how long to wait after failed attempt n (n >= 1):
// min(InitialDelay * 2^(n-1), MaxDelay). Zero fields use the defaults.
func (p RetryPolicy) Delay(attempt int) time.Duration {
	p = p.withDefaults()
	d := p.InitialDelay
	for i := 1; i < attempt && d < p.MaxDelay; i++ {
		d *= 2
	}
	return min(d, p.MaxDelay)
}
