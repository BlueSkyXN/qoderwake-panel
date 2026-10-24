package admission

import (
	"context"
	"testing"
	"time"
)

func TestDrainBlocksNewWritesAndWaitsForExisting(t *testing.T) {
	c := New()
	first, e := c.Acquire(true)
	if e != nil {
		t.Fatal(e)
	}
	second, e := c.Acquire(true)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = c.Acquire(true); e == nil {
		t.Fatal("call concurrency limit missing")
	}
	epoch, e := c.Begin("maintenance", time.Minute, first)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = c.Acquire(false); e == nil {
		t.Fatal("maintenance admitted write")
	}
	if e = c.Transition(epoch, "maintenance"); e == nil {
		t.Fatal("active request ignored")
	}
	second.Release()
	if e = c.Wait(context.Background(), epoch); e != nil {
		t.Fatal(e)
	}
	if e = c.Transition(epoch, "maintenance"); e != nil {
		t.Fatal(e)
	}
	c.now = func() time.Time { return time.Now().Add(2 * time.Minute) }
	if c.Status().Mode != "normal" {
		t.Fatal("maintenance expiry failed")
	}
}
func TestFailedDrainRestoresOnlyItsOwnGeneration(t *testing.T) {
	c := New()
	lease, _ := c.Acquire(false)
	epoch, e := c.Begin("test", time.Minute, nil)
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	if e = c.Wait(ctx, epoch); e == nil {
		t.Fatal("active lease ignored")
	}
	c.Complete(epoch)
	if c.Status().Mode != "normal" {
		t.Fatal("failed drain not restored")
	}
	lease.Release()
	next, e := c.Begin("next", time.Minute, nil)
	if e != nil {
		t.Fatal(e)
	}
	c.Complete(epoch)
	if c.Status().Epoch != next || c.Status().Mode != "draining" {
		t.Fatal("stale completion changed current maintenance")
	}
	c.Complete(next)
}
