package registry

import (
	"testing"
	"time"
)

func TestAgentPresentAtTTLBoundary(t *testing.T) {
	now := time.Now()
	ttl := 45 * time.Second

	tests := []struct {
		name       string
		lastSeenAt time.Time
		want       bool
	}{
		{"exactly at ttl", now.Add(-ttl), true},
		{"just under ttl", now.Add(-ttl + time.Nanosecond), true},
		{"just over ttl", now.Add(-ttl - time.Nanosecond), false},
		{"well within ttl", now.Add(-1 * time.Second), true},
		{"well past ttl", now.Add(-5 * time.Minute), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := Agent{LastSeenAt: tt.lastSeenAt}
			if got := a.Present(now, ttl); got != tt.want {
				t.Errorf("Present() = %v, want %v", got, tt.want)
			}
		})
	}
}
