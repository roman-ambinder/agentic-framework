package harness

import (
	"testing"
	"time"
)

func TestRetryDefaults(t *testing.T) {
	want := RetryPolicy{MaxAttempts: 3, InitialDelay: time.Second, MaxDelay: 30 * time.Second}
	tests := []struct {
		name string
		in   RetryPolicy
	}{
		{"zero", RetryPolicy{}},
		{"negative", RetryPolicy{MaxAttempts: -1, InitialDelay: -time.Second, MaxDelay: -time.Second}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.in.withDefaults(); got != want {
				t.Fatalf("withDefaults = %+v, want %+v", got, want)
			}
		})
	}
	custom := RetryPolicy{MaxAttempts: 5, InitialDelay: time.Millisecond, MaxDelay: time.Minute}
	if got := custom.withDefaults(); got != custom {
		t.Fatalf("withDefaults changed a full policy: %+v", got)
	}
}

func TestRetryDelaySchedule(t *testing.T) {
	p := RetryPolicy{MaxAttempts: 7}
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 30 * time.Second}
	for i, w := range want {
		if got := p.Delay(i + 1); got != w {
			t.Errorf("Delay(%d) = %s, want %s", i+1, got, w)
		}
	}
}

func TestRetryDelayCustom(t *testing.T) {
	p := RetryPolicy{InitialDelay: 100 * time.Millisecond, MaxDelay: 250 * time.Millisecond}
	want := []time.Duration{100 * time.Millisecond, 200 * time.Millisecond, 250 * time.Millisecond, 250 * time.Millisecond}
	for i, w := range want {
		if got := p.Delay(i + 1); got != w {
			t.Errorf("Delay(%d) = %s, want %s", i+1, got, w)
		}
	}
}

func TestRetryDelayNoOverflow(t *testing.T) {
	p := RetryPolicy{}
	if got := p.Delay(200); got != 30*time.Second {
		t.Fatalf("Delay(200) = %s", got)
	}
}
