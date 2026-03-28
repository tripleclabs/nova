package network

import (
	"testing"
	"time"
)

func TestConditioner_SetAndGet(t *testing.T) {
	c := NewConditioner()
	c.SetRule(LinkRule{
		NodeA:   "node-a",
		NodeB:   "node-b",
		Latency: 50 * time.Millisecond,
		Loss:    0.05,
	})

	rule := c.GetRule("node-a", "node-b")
	if rule == nil {
		t.Fatal("expected rule")
	}
	if rule.Latency != 50*time.Millisecond {
		t.Errorf("latency = %v, want 50ms", rule.Latency)
	}
	if rule.Loss != 0.05 {
		t.Errorf("loss = %f, want 0.05", rule.Loss)
	}

	// Order-independent lookup.
	rule2 := c.GetRule("node-b", "node-a")
	if rule2 == nil {
		t.Fatal("should find rule with reversed node order")
	}
}

func TestConditioner_Partition(t *testing.T) {
	c := NewConditioner()
	c.Partition("a", "b")

	if !c.ShouldDrop("a", "b") {
		t.Error("partitioned link should drop packets")
	}
	if !c.ShouldDrop("b", "a") {
		t.Error("partitioned link should drop in reverse too")
	}
}

func TestConditioner_Heal(t *testing.T) {
	c := NewConditioner()
	c.Partition("a", "b")
	c.Heal("a", "b")

	if c.ShouldDrop("a", "b") {
		t.Error("healed link should not drop")
	}
	if c.GetRule("a", "b") != nil {
		t.Error("rule should be removed after heal")
	}
}

func TestConditioner_Degrade(t *testing.T) {
	c := NewConditioner()
	err := c.Degrade("x", "y", 100*time.Millisecond, 10*time.Millisecond, 0.1)
	if err != nil {
		t.Fatal(err)
	}

	rule := c.GetRule("x", "y")
	if rule == nil {
		t.Fatal("expected rule")
	}
	if rule.Latency != 100*time.Millisecond {
		t.Errorf("latency = %v", rule.Latency)
	}
	if rule.Jitter != 10*time.Millisecond {
		t.Errorf("jitter = %v", rule.Jitter)
	}
}

func TestConditioner_Degrade_InvalidLoss(t *testing.T) {
	c := NewConditioner()
	if err := c.Degrade("a", "b", 0, 0, 1.5); err == nil {
		t.Error("expected error for loss > 1.0")
	}
	if err := c.Degrade("a", "b", 0, 0, -0.1); err == nil {
		t.Error("expected error for loss < 0.0")
	}
}

func TestConditioner_Delay(t *testing.T) {
	c := NewConditioner()

	// No rule — no delay.
	if d := c.Delay("a", "b"); d != 0 {
		t.Errorf("no rule should give 0 delay, got %v", d)
	}

	c.SetRule(LinkRule{NodeA: "a", NodeB: "b", Latency: 50 * time.Millisecond})
	d := c.Delay("a", "b")
	if d != 50*time.Millisecond {
		t.Errorf("delay = %v, want 50ms", d)
	}
}

func TestConditioner_Delay_WithJitter(t *testing.T) {
	c := NewConditioner()
	c.SetRule(LinkRule{
		NodeA:   "a",
		NodeB:   "b",
		Latency: 100 * time.Millisecond,
		Jitter:  20 * time.Millisecond,
	})

	// Run multiple times to verify jitter introduces variance.
	seen := make(map[time.Duration]bool)
	for i := 0; i < 100; i++ {
		d := c.Delay("a", "b")
		seen[d] = true
		if d < 80*time.Millisecond || d > 120*time.Millisecond {
			t.Errorf("delay %v out of expected range [80ms, 120ms]", d)
		}
	}
	if len(seen) < 2 {
		t.Error("jitter should produce varying delays")
	}
}

func TestConditioner_AllRules(t *testing.T) {
	c := NewConditioner()
	c.Partition("a", "b")
	c.Degrade("b", "c", 10*time.Millisecond, 0, 0)

	rules := c.AllRules()
	if len(rules) != 2 {
		t.Errorf("len = %d, want 2", len(rules))
	}
}

func TestConditioner_Reset(t *testing.T) {
	c := NewConditioner()
	c.Partition("a", "b")
	c.Partition("c", "d")
	c.Reset()

	if len(c.AllRules()) != 0 {
		t.Error("reset should clear all rules")
	}
}

func TestConditioner_ShouldDrop_Loss(t *testing.T) {
	c := NewConditioner()
	c.SetRule(LinkRule{NodeA: "a", NodeB: "b", Loss: 1.0})

	// 100% loss should always drop.
	for i := 0; i < 10; i++ {
		if !c.ShouldDrop("a", "b") {
			t.Fatal("100% loss should always drop")
		}
	}

	// 0% loss should never drop.
	c.SetRule(LinkRule{NodeA: "a", NodeB: "b", Loss: 0.0})
	for i := 0; i < 10; i++ {
		if c.ShouldDrop("a", "b") {
			t.Fatal("0% loss should never drop")
		}
	}
}

func TestLinkKey_OrderIndependent(t *testing.T) {
	if linkKey("a", "b") != linkKey("b", "a") {
		t.Error("linkKey should be order-independent")
	}
}
