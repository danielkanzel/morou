package health

import "testing"

func TestParseVLLMWaiting(t *testing.T) {
	body := `# HELP vllm:num_requests_waiting Number of requests waiting.
# TYPE vllm:num_requests_waiting gauge
vllm:num_requests_waiting{model_name="x"} 3.0
vllm:num_requests_waiting{model_name="y"} 2.0
vllm:num_requests_running{model_name="x"} 1.0
`
	n, ok := parseVLLMWaiting(body)
	if !ok {
		t.Fatal("expected to find metric")
	}
	if n != 5 {
		t.Fatalf("got %d, want 5", n)
	}
}

func TestParseVLLMWaitingMissing(t *testing.T) {
	if _, ok := parseVLLMWaiting("some_other_metric 1\n"); ok {
		t.Fatal("expected not found")
	}
}

func TestParseVLLMWaitingNoPrefixFalseMatch(t *testing.T) {
	// A metric that merely starts with the same text must not match.
	body := "vllm:num_requests_waiting_total 9\n"
	if _, ok := parseVLLMWaiting(body); ok {
		t.Fatal("should not match a different metric name with same prefix")
	}
}

func TestParseSGLangQueue(t *testing.T) {
	cases := []string{
		`{"num_requests_waiting": 4}`,
		`{"waiting_queue_size": 4}`,
		`{"internal_states": [{"queue_size": 4}]}`,
	}
	for _, c := range cases {
		n, ok := parseSGLangQueue(c)
		if !ok || n != 4 {
			t.Fatalf("body %q -> %d,%v want 4,true", c, n, ok)
		}
	}
}

func TestParseSGLangQueueMissing(t *testing.T) {
	if _, ok := parseSGLangQueue(`{"foo": 1}`); ok {
		t.Fatal("expected not found")
	}
	if _, ok := parseSGLangQueue(`not json`); ok {
		t.Fatal("expected parse failure -> not found")
	}
}

func TestHealthySnapshot(t *testing.T) {
	m := NewMonitor(map[string][]string{
		"a": {"http://h1:1", "http://h2:1"},
	}, Options{})
	if m.HasHealthy("a") {
		t.Fatal("backends should start unhealthy")
	}
	pool := m.pools["a"]
	pool[0].SetHealthyForTest(true)
	bs, ok := m.Healthy("a")
	if !ok || len(bs) != 1 {
		t.Fatalf("expected 1 healthy, got %d (ok=%v)", len(bs), ok)
	}
	if _, ok := m.Healthy("missing"); ok {
		t.Fatal("unknown model should return ok=false")
	}
}
