package network

import (
	"fmt"
	"math/rand"
	"sync"
	"time"
)

// LinkRule describes network conditions between two nodes.
type LinkRule struct {
	NodeA   string        `json:"node_a"`
	NodeB   string        `json:"node_b"`
	Latency time.Duration `json:"latency"`  // One-way added latency.
	Jitter  time.Duration `json:"jitter"`   // Random +/- range around latency.
	Loss    float64       `json:"loss"`     // Packet loss probability (0.0–1.0).
	Down    bool          `json:"down"`     // Hard partition — all packets dropped.
}

// linkKey produces a canonical key for a node pair (order-independent).
func linkKey(a, b string) string {
	if a > b {
		a, b = b, a
	}
	return a + "<->" + b
}

// Conditioner manages network chaos rules between nodes.
type Conditioner struct {
	mu    sync.RWMutex
	rules map[string]*LinkRule // keyed by linkKey
}

// NewConditioner creates a new network conditioner.
func NewConditioner() *Conditioner {
	return &Conditioner{rules: make(map[string]*LinkRule)}
}

// SetRule applies a network condition rule between two nodes.
func (c *Conditioner) SetRule(rule LinkRule) {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := linkKey(rule.NodeA, rule.NodeB)
	c.rules[key] = &rule
}

// GetRule returns the rule for a node pair, or nil if none.
func (c *Conditioner) GetRule(a, b string) *LinkRule {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.rules[linkKey(a, b)]
}

// RemoveRule removes any rule between two nodes.
func (c *Conditioner) RemoveRule(a, b string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.rules, linkKey(a, b))
}

// AllRules returns a snapshot of all active rules.
func (c *Conditioner) AllRules() []LinkRule {
	c.mu.RLock()
	defer c.mu.RUnlock()
	rules := make([]LinkRule, 0, len(c.rules))
	for _, r := range c.rules {
		rules = append(rules, *r)
	}
	return rules
}

// Reset removes all rules.
func (c *Conditioner) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.rules = make(map[string]*LinkRule)
}

// ShouldDrop returns true if a packet on this link should be dropped.
func (c *Conditioner) ShouldDrop(a, b string) bool {
	rule := c.GetRule(a, b)
	if rule == nil {
		return false
	}
	if rule.Down {
		return true
	}
	if rule.Loss > 0 {
		return rand.Float64() < rule.Loss
	}
	return false
}

// Delay returns how long a packet should be delayed on this link.
func (c *Conditioner) Delay(a, b string) time.Duration {
	rule := c.GetRule(a, b)
	if rule == nil {
		return 0
	}
	d := rule.Latency
	if rule.Jitter > 0 {
		jitter := time.Duration(rand.Int63n(int64(rule.Jitter)*2)) - rule.Jitter
		d += jitter
		if d < 0 {
			d = 0
		}
	}
	return d
}

// Partition creates a hard partition between two nodes.
func (c *Conditioner) Partition(a, b string) {
	c.SetRule(LinkRule{NodeA: a, NodeB: b, Down: true})
}

// Heal removes a partition (and all conditions) between two nodes.
func (c *Conditioner) Heal(a, b string) {
	c.RemoveRule(a, b)
}

// Degrade sets latency, jitter, and loss on a link.
func (c *Conditioner) Degrade(a, b string, latency, jitter time.Duration, loss float64) error {
	if loss < 0 || loss > 1 {
		return fmt.Errorf("loss must be between 0.0 and 1.0, got %f", loss)
	}
	c.SetRule(LinkRule{
		NodeA:   a,
		NodeB:   b,
		Latency: latency,
		Jitter:  jitter,
		Loss:    loss,
	})
	return nil
}
