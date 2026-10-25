package pool

import (
	"context"
	"os"
	"testing"
	"time"

	"free-ai-router/internal/provider"
)

func TestEvaluatorInstallsOnlyAcceptedNodes(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("POOL_TEST_FAKE_CORE", "1")

	reg := NewRegistry(10, 2)
	e := NewEvaluator(Config{Source: SourceProxprobe, ProbeWorkers: 2, MaxNodes: 10},
		reg, NewRunner(exe, t.TempDir()), fakeCoreImpl{}, standIn{}, nil, nil)
	e.cfg.LoadPass = func() (Pass, error) {
		return Pass{
			Counts: PassCounts{Parsed: 3, HTTPOK: 3},
			Nodes: []Node{
				{ID: "a", Label: "a", Proto: "vless", Host: "h", Port: 443, Raw: `{"type":"vless"}`},
				{ID: "b", Label: "b", Proto: "vless", Host: "h", Port: 443, Raw: `{"type":"vless"}`},
				{ID: "c", Label: "c", Proto: "vless", Host: "h", Port: 443, Raw: `{"type":"vless"}`},
			},
		}, nil
	}
	e.probeTunnel = func(_ context.Context, tun *Tunnel, _ provider.Model) Verdict {
		// Each node gets its own latency; the refused one must not enter the list.
		switch tun.Node.ID {
		case "a":
			return Verdict{Accepted: true, RTT: 300 * time.Millisecond, ExitIP: "10.0.0.1"}
		case "b":
			return Verdict{Accepted: true, RTT: 100 * time.Millisecond, ExitIP: "10.0.1.2"}
		default:
			return Verdict{AddressRefused: true, Reason: "the provider refuses this address"}
		}
	}

	report := e.Run(context.Background())
	if report.Err != nil {
		t.Fatalf("run: %v", report.Err)
	}
	if report.Parsed != 3 || report.Probed != 3 || report.Accepted != 2 {
		t.Fatalf("report %+v", report)
	}
	snap := reg.Snapshot()
	if snap.Relevant != 2 || snap.Nodes[0].ID != "b" {
		t.Fatalf("registry %+v", snap.Nodes)
	}
}

// TestARunReportsWhenItFinished: the report carries the two stamps the caller turns
// into a duration, and a finished run has both.
func TestARunReportsWhenItFinished(t *testing.T) {
	ev := NewEvaluator(Config{Source: SourceProxprobe},
		NewRegistry(10, 2), NewRunner("", ""), fakeCoreImpl{}, nil, nil, nil)
	ev.cfg.LoadPass = func() (Pass, error) { return Pass{}, nil }

	rep := ev.Run(context.Background())
	if rep.Err == nil {
		t.Fatal("an empty pass must be reported as an error")
	}
	if rep.Started.IsZero() || rep.Finished.IsZero() {
		t.Fatalf("stamps: started %v finished %v", rep.Started, rep.Finished)
	}
	if rep.Finished.Before(rep.Started) {
		t.Fatalf("finished %v before started %v", rep.Finished, rep.Started)
	}
}

// TestEvaluatorReadsThePassInsteadOfTheSubscriptions: with source proxprobe the
// pipeline does not fetch, parse or ping — the pass owns that — and only the
// provider's verdict and the exit address are measured here.
func TestEvaluatorReadsThePassInsteadOfTheSubscriptions(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("POOL_TEST_FAKE_CORE", "1")

	reg := NewRegistry(10, 2)
	e := NewEvaluator(Config{Source: SourceProxprobe, ProbeWorkers: 2, MaxNodes: 10},
		reg, NewRunner(exe, t.TempDir()), fakeCoreImpl{}, standIn{}, nil, nil)
	passes := 0
	e.cfg.RunPass = func(context.Context) error { passes++; return nil }
	e.cfg.LoadPass = func() (Pass, error) {
		return Pass{
			Counts: PassCounts{Parsed: 7, HTTPOK: 3, Unsupported: 2},
			Nodes: []Node{
				{ID: "a", Label: "a", Proto: "vless", Host: "h", Port: 443, Raw: `{"type":"vless"}`},
				{ID: "b", Label: "b", Proto: "vless", Host: "h", Port: 443, Raw: `{"type":"vless"}`},
			},
		}, nil
	}
	e.probeTunnel = func(_ context.Context, tun *Tunnel, _ provider.Model) Verdict {
		if tun.Node.ID == "a" {
			return Verdict{Accepted: true, RTT: 120 * time.Millisecond, ExitIP: "10.0.0.1"}
		}
		return Verdict{AddressRefused: true, Reason: "the provider refuses this address"}
	}

	rep := e.Run(context.Background())
	if rep.Err != nil {
		t.Fatalf("run: %v", rep.Err)
	}
	if rep.Source != SourceProxprobe || !rep.PassRan || passes != 1 {
		t.Fatalf("report %+v, passes %d", rep, passes)
	}
	if rep.Parsed != 7 || rep.HTTPOK != 3 || rep.Unsupported != 2 || rep.Probed != 2 || rep.Accepted != 1 {
		t.Fatalf("counts %+v", rep)
	}
	if snap := reg.Snapshot(); snap.Relevant != 1 || snap.Nodes[0].ID != "a" {
		t.Fatalf("registry %+v", snap.Nodes)
	}
}

// TestEvaluatorKeepsTheListWhenThePassFails: a broken pass is a failed evaluation,
// not an empty list — the servers that are serving keep serving.
func TestEvaluatorKeepsTheListWhenThePassFails(t *testing.T) {
	reg := NewRegistry(10, 2)
	reg.Replace([]Node{{ID: "keep", Label: "keep", Proto: "vless", Raw: "vless://keep"}})
	e := NewEvaluator(Config{Source: SourceProxprobe}, reg, NewRunner("", ""), fakeCoreImpl{}, standIn{}, nil, nil)
	e.cfg.RunPass = func(context.Context) error { return os.ErrDeadlineExceeded }
	e.cfg.LoadPass = func() (Pass, error) {
		t.Fatal("a failed pass must not be read")
		return Pass{}, nil
	}

	rep := e.Run(context.Background())
	if rep.Err == nil || !rep.PassRan {
		t.Fatalf("report %+v", rep)
	}
	if snap := reg.Snapshot(); snap.Relevant != 1 || snap.Nodes[0].ID != "keep" {
		t.Fatalf("the list was destroyed: %+v", snap.Nodes)
	}
}

// TestEvaluatorKeepsOneServerPerExitPool: five servers behind one hosting pool
// share one allowance, so the list keeps the fastest of them and not five copies
// of the same budget.
func TestEvaluatorKeepsOneServerPerExitPool(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("POOL_TEST_FAKE_CORE", "1")

	reg := NewRegistry(10, 2)
	e := NewEvaluator(Config{Source: SourceProxprobe, ProbeWorkers: 3, MaxNodes: 10},
		reg, NewRunner(exe, t.TempDir()), fakeCoreImpl{}, standIn{}, nil, nil)
	e.cfg.LoadPass = func() (Pass, error) {
		return Pass{
			Counts: PassCounts{Parsed: 4, HTTPOK: 4},
			Nodes: []Node{
				{ID: "slow", Label: "slow", Proto: "vless", Host: "h", Port: 443, Raw: `{"type":"vless"}`},
				{ID: "fast", Label: "fast", Proto: "vless", Host: "h", Port: 443, Raw: `{"type":"vless"}`},
				{ID: "other", Label: "other", Proto: "vless", Host: "h", Port: 443, Raw: `{"type":"vless"}`},
			},
		}, nil
	}
	e.probeTunnel = func(_ context.Context, tun *Tunnel, _ provider.Model) Verdict {
		switch tun.Node.ID {
		case "slow":
			return Verdict{Accepted: true, RTT: 900 * time.Millisecond, ExitIP: "198.51.100.7"}
		case "fast":
			return Verdict{Accepted: true, RTT: 90 * time.Millisecond, ExitIP: "198.51.100.9"}
		default:
			return Verdict{Accepted: true, RTT: 200 * time.Millisecond, ExitIP: "203.0.113.7"}
		}
	}

	rep := e.Run(context.Background())
	if rep.Err != nil {
		t.Fatalf("run: %v", rep.Err)
	}
	if rep.Accepted != 3 {
		t.Fatalf("accepted %d, want 3", rep.Accepted)
	}
	snap := reg.Snapshot()
	if snap.Relevant != 2 {
		t.Fatalf("two pools, %d nodes: %+v", snap.Relevant, snap.Nodes)
	}
	if snap.Nodes[0].ID != "fast" || snap.Nodes[1].ID != "other" {
		t.Fatalf("one per pool, fastest first: %+v", snap.Nodes)
	}
}

// TestARefusalBurnsItsWholeExitPool: a "free usage limit" answer is about the
// network, not the node. The node accepted earlier from the same network is worth
// nothing once its sibling was refused, and a later node from that network is not
// tried again.
func TestARefusalBurnsItsWholeExitPool(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("POOL_TEST_FAKE_CORE", "1")

	reg := NewRegistry(10, 2)
	e := NewEvaluator(Config{Source: SourceProxprobe, ProbeWorkers: 1, MaxNodes: 10},
		reg, NewRunner(exe, t.TempDir()), fakeCoreImpl{}, standIn{}, nil, nil)
	e.cfg.LoadPass = func() (Pass, error) {
		return Pass{
			Counts: PassCounts{Parsed: 4, HTTPOK: 4},
			Nodes: []Node{
				{ID: "first", Label: "first", Proto: "vless", Host: "h", Port: 443, Raw: `{"type":"vless"}`},
				{ID: "burn", Label: "burn", Proto: "vless", Host: "h", Port: 443, Raw: `{"type":"vless"}`},
				{ID: "later", Label: "later", Proto: "vless", Host: "h", Port: 443, Raw: `{"type":"vless"}`},
				{ID: "elsewhere", Label: "elsewhere", Proto: "vless", Host: "h", Port: 443, Raw: `{"type":"vless"}`},
			},
		}, nil
	}
	e.probeTunnel = func(_ context.Context, tun *Tunnel, _ provider.Model) Verdict {
		switch tun.Node.ID {
		case "first":
			return Verdict{Accepted: true, RTT: 120 * time.Millisecond, ExitIP: "192.0.2.10"}
		case "burn":
			return Verdict{AddressRefused: true, Reason: "the provider refuses this address", ExitIP: "192.0.2.20"}
		case "later":
			return Verdict{Accepted: true, RTT: 120 * time.Millisecond, ExitIP: "192.0.2.30"}
		default:
			return Verdict{Accepted: true, RTT: 400 * time.Millisecond, ExitIP: "198.51.100.30"}
		}
	}

	rep := e.Run(context.Background())
	if rep.Err != nil {
		t.Fatalf("run: %v", rep.Err)
	}
	snap := reg.Snapshot()
	if snap.Relevant != 1 || snap.Nodes[0].ID != "elsewhere" {
		t.Fatalf("the burned pool survived: %+v", snap.Nodes)
	}
}
