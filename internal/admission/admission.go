// Package admission coordinates Panel writes and maintenance transitions.
package admission

import (
	"context"
	"qoderwake-panel/internal/apierror"
	"strings"
	"sync"
	"time"
)

type State struct {
	Mode            string   `json:"mode"`
	Epoch           uint64   `json:"epoch"`
	Reason          string   `json:"reason"`
	Since           float64  `json:"since"`
	Expires         *float64 `json:"expires"`
	ActiveMutations int      `json:"activeMutations"`
	ActiveCalls     int      `json:"activeCalls"`
}
type Controller struct {
	mu     sync.Mutex
	state  State
	ttl    time.Duration
	next   uint64
	leases map[uint64]bool
	now    func() time.Time
}
type Lease struct {
	controller *Controller
	token      uint64
}

func New() *Controller {
	return &Controller{state: State{Mode: "normal", Since: stamp(time.Now())}, leases: map[uint64]bool{}, now: time.Now}
}
func stamp(t time.Time) float64 { return float64(t.UnixNano()) / 1e9 }
func (c *Controller) expire() {
	if c.state.Mode == "maintenance" && c.state.Expires != nil && *c.state.Expires <= stamp(c.now()) {
		c.state.Mode = "normal"
		c.state.Epoch++
		c.state.Reason = "maintenance_ttl_expired"
		c.state.Since = stamp(c.now())
		c.state.Expires = nil
	}
}
func (c *Controller) Status() State {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.expire()
	out := c.state
	out.ActiveMutations = len(c.leases)
	for _, call := range c.leases {
		if call {
			out.ActiveCalls++
		}
	}
	return out
}
func (c *Controller) Acquire(call bool) (*Lease, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.expire()
	if c.state.Mode != "normal" {
		return nil, apierror.New(503, "maintenance_mode")
	}
	calls := 0
	for _, v := range c.leases {
		if v {
			calls++
		}
	}
	if call && calls >= 2 {
		return nil, apierror.New(429, "concurrency_limited")
	}
	c.next++
	c.leases[c.next] = call
	return &Lease{c, c.next}, nil
}
func (l *Lease) Release() {
	if l == nil {
		return
	}
	l.controller.mu.Lock()
	defer l.controller.mu.Unlock()
	delete(l.controller.leases, l.token)
}
func (c *Controller) Begin(reason string, ttl time.Duration, lease *Lease) (uint64, error) {
	if strings.TrimSpace(reason) == "" || ttl < 30*time.Second || ttl > time.Hour {
		return 0, apierror.Invalid("invalid_maintenance_request")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.expire()
	if c.state.Mode != "normal" {
		return 0, apierror.New(503, "maintenance_mode")
	}
	if lease != nil {
		if lease.controller != c {
			return 0, apierror.Invalid("invalid_admission_lease")
		}
		if _, ok := c.leases[lease.token]; !ok {
			return 0, apierror.Invalid("invalid_admission_lease")
		}
		delete(c.leases, lease.token)
	}
	c.state.Mode = "draining"
	c.state.Epoch++
	runes := []rune(strings.TrimSpace(reason))
	c.state.Reason = string(runes[:min(200, len(runes))])
	c.state.Since = stamp(c.now())
	c.state.Expires = nil
	c.ttl = ttl
	return c.state.Epoch, nil
}
func (c *Controller) Wait(ctx context.Context, epoch uint64) error {
	timer := time.NewTicker(25 * time.Millisecond)
	defer timer.Stop()
	for {
		c.mu.Lock()
		changed := c.state.Epoch != epoch || c.state.Mode != "draining"
		empty := len(c.leases) == 0
		c.mu.Unlock()
		if changed {
			return apierror.New(409, "maintenance_state_changed")
		}
		if empty {
			return nil
		}
		select {
		case <-ctx.Done():
			return apierror.New(503, "maintenance_drain_timeout")
		case <-timer.C:
		}
	}
}
func (c *Controller) Transition(epoch uint64, mode string) error {
	if mode != "maintenance" && mode != "restarting" {
		return apierror.Invalid("invalid_maintenance_mode")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.expire()
	if c.state.Epoch != epoch || c.state.Mode != "draining" || len(c.leases) > 0 {
		return apierror.New(409, "maintenance_state_changed")
	}
	c.state.Mode = mode
	c.state.Since = stamp(c.now())
	if mode == "maintenance" {
		expires := stamp(c.now().Add(c.ttl))
		c.state.Expires = &expires
	}
	return nil
}
func (c *Controller) Complete(epoch uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state.Epoch == epoch && (c.state.Mode == "draining" || c.state.Mode == "restarting") {
		c.reset()
	}
}
func (c *Controller) reset() {
	c.state.Mode = "normal"
	c.state.Epoch++
	c.state.Reason = ""
	c.state.Since = stamp(c.now())
	c.state.Expires = nil
}
func (c *Controller) Exit() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.expire()
	if c.state.Mode == "normal" {
		return nil
	}
	if c.state.Mode != "maintenance" {
		return apierror.New(409, "maintenance_exit_rejected")
	}
	c.reset()
	return nil
}
