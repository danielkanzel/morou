package balancer

import (
	"testing"

	"github.com/modelrouter/router/internal/config"
	"github.com/modelrouter/router/internal/health"
)

func backends(n int) []*health.Backend {
	out := make([]*health.Backend, n)
	for i := 0; i < n; i++ {
		out[i] = health.NewBackend("m", "http://h"+string(rune('0'+i))+":1")
	}
	return out
}

func TestRandomEmpty(t *testing.T) {
	b := New(config.LBRandom)
	if got := b.Pick("m", nil); got != nil {
		t.Fatalf("expected nil for empty pool, got %v", got)
	}
}

func TestRandomReturnsMember(t *testing.T) {
	b := New(config.LBRandom)
	pool := backends(3)
	for i := 0; i < 50; i++ {
		got := b.Pick("m", pool)
		if !contains(pool, got) {
			t.Fatalf("pick %v not in pool", got)
		}
	}
}

func TestRoundRobinCycles(t *testing.T) {
	b := New(config.LBRoundRobin)
	pool := backends(3)
	seen := make([]*health.Backend, 0, 6)
	for i := 0; i < 6; i++ {
		seen = append(seen, b.Pick("m", pool))
	}
	// Expect strict cycling: 0,1,2,0,1,2.
	if seen[0] == seen[1] || seen[1] == seen[2] || seen[0] == seen[2] {
		t.Fatalf("first cycle not distinct: %v", seen[:3])
	}
	if seen[0] != seen[3] || seen[1] != seen[4] || seen[2] != seen[5] {
		t.Fatalf("did not cycle: %v", seen)
	}
}

func TestRoundRobinPerModelCursor(t *testing.T) {
	b := New(config.LBRoundRobin)
	poolA := backends(2)
	poolB := backends(2)
	// Independent cursors per model.
	a1 := b.Pick("a", poolA)
	bb1 := b.Pick("b", poolB)
	a2 := b.Pick("a", poolA)
	if a1 == a2 {
		t.Fatalf("model a did not advance: %v == %v", a1, a2)
	}
	if bb1 != poolB[0] {
		t.Fatalf("model b should start at index 0")
	}
}

func TestLessQueuePicksSmallest(t *testing.T) {
	b := New(config.LBLessQueue)
	pool := backends(3)
	pool[0].SetQueueForTest(10)
	pool[1].SetQueueForTest(2)
	pool[2].SetQueueForTest(7)
	got := b.Pick("m", pool)
	if got != pool[1] {
		t.Fatalf("expected smallest-queue backend, got %v", got)
	}
}

func contains(pool []*health.Backend, b *health.Backend) bool {
	for _, p := range pool {
		if p == b {
			return true
		}
	}
	return false
}
