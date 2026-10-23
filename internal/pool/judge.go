package pool

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"free-ai-router/internal/egress"
	"free-ai-router/internal/provider"
)

// Prober decides whether a node is usable by asking the provider the same
// question a client will ask it.
//
// An open port proves nothing: the provider is the only party that can say
// whether it accepts traffic from an address, and the answer is the whole point of
// the stage.
type Prober struct {
	Provider provider.Provider
	Model    provider.Model
	// Key is consulted per probe so that rotation applies here too. Nil means no
	// key, which is the anonymous mode.
	Key func() string
	// Timeout bounds one probe. Zero means twelve seconds.
	Timeout time.Duration
	// TraceURL overrides the exit-address echo, for tests.
	TraceURL string
}

// Verdict is what one node is worth.
type Verdict struct {
	// Accepted reports that the node may enter the relevant list.
	Accepted bool
	// AddressRefused reports that the provider answered "not from this network",
	// which is the one verdict a different exit can change.
	AddressRefused bool
	Status         int
	RTT            time.Duration
	ExitIP         string
	Reason         string
}

// Run probes one node through its tunnel.
func (p Prober) Run(ctx context.Context, t *Tunnel) Verdict {
	if t == nil || t.Proxy == nil {
		return Verdict{Reason: "no tunnel"}
	}
	rt := &http.Transport{Proxy: http.ProxyURL(t.Proxy)}
	defer rt.CloseIdleConnections()
	return p.Against(ctx, rt)
}

// Against is the probe against an arbitrary round tripper, which is what makes it
// testable without a process.
func (p Prober) Against(ctx context.Context, rt http.RoundTripper) Verdict {
	if p.Provider == nil {
		return Verdict{Reason: "no provider"}
	}
	timeout := p.Timeout
	if timeout <= 0 {
		timeout = 12 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// The request is built by the provider, not by us: the pool must measure the
	// wire contract a client meets, or it measures nothing useful.
	prepared, err := p.Provider.Build(provider.Request{
		Model:     p.Model,
		Messages:  []provider.Message{{Role: "user", Text: "hi"}},
		Stream:    true,
		MaxTokens: 1,
	})
	if err != nil {
		return Verdict{Reason: "build: " + err.Error()}
	}
	key := ""
	if p.Key != nil {
		key = p.Key()
	}
	provider.ApplyCredential(p.Provider, prepared, key)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, prepared.URL, bytes.NewReader(prepared.Body))
	if err != nil {
		return Verdict{Reason: "request: " + err.Error()}
	}
	for name, values := range prepared.Headers {
		for _, value := range values {
			req.Header.Add(name, value)
		}
	}

	start := time.Now()
	resp, err := (&http.Client{Transport: rt}).Do(req)
	if err != nil {
		return Verdict{Reason: "the tunnel failed: " + err.Error()}
	}
	rtt := time.Since(start)
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<10))
	resp.Body.Close()

	v := Verdict{Status: resp.StatusCode, RTT: rtt}

	// The exit address is measured after the verdict, because a blocked echo would
	// otherwise reject a server that answers the provider perfectly well.
	if exitIP, err := egress.Observe(ctx, rt, p.TraceURL); err == nil {
		v.ExitIP = exitIP
	}

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		v.Accepted = true
		return v
	}

	failure := p.Provider.Classify(resp, body)
	if failure.AddressRejected {
		v.AddressRefused = true
		v.Reason = "the provider refuses this address"
		return v
	}

	// Any other refusal is about the key, the quota or the model, not the address.
	// The node is worth keeping: this stage asks one question only.
	v.Accepted = true
	v.Reason = fmt.Sprintf("inconclusive: %s", resp.Status)
	return v
}
