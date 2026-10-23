package pool

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Subscription is one fetched list, with its own outcome.
//
// A Subscription that failed is reported rather than dropped: an operator whose
// mirror went dark needs to see which one, and a failed subscription must not be
// able to empty a pool that is otherwise fine.
type Subscription struct {
	URL     string
	Nodes   []Node
	Skipped []string
	Err     error
}

// FetchAll reads every subscription in parallel.
//
// Fetching runs on the host, never through the pool: a node cannot be measured
// before it exists.
func FetchAll(ctx context.Context, urls []string, timeout time.Duration, client *http.Client) []Subscription {
	if client == nil {
		client = http.DefaultClient
	}
	out := make([]Subscription, len(urls))
	var wg sync.WaitGroup
	for i, u := range urls {
		wg.Add(1)
		go func(i int, u string) {
			defer wg.Done()
			out[i] = Subscription{URL: u}
			body, err := fetchWithRetry(ctx, client, u, timeout)
			if err != nil {
				out[i].Err = err
				return
			}
			plain, ok := DecodeBody(body)
			if !ok {
				out[i].Err = fmt.Errorf("not a share-link list")
				return
			}
			nodes, skipped, err := ParseList(bytes.NewReader(plain))
			out[i].Nodes, out[i].Skipped, out[i].Err = nodes, skipped, err
		}(i, u)
	}
	wg.Wait()
	return out
}

func fetchWithRetry(ctx context.Context, client *http.Client, url string, timeout time.Duration) ([]byte, error) {
	backoff := []time.Duration{0, time.Second, 3 * time.Second}
	var lastErr error
	for _, delay := range backoff {
		if delay > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(delay):
			}
		}
		reqCtx, cancel := context.WithTimeout(ctx, timeout)
		req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
		if err != nil {
			cancel()
			return nil, err
		}
		resp, err := client.Do(req)
		if err != nil {
			cancel()
			lastErr = err
			continue
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		resp.Body.Close()
		cancel()
		if err != nil {
			lastErr = err
			continue
		}
		if resp.StatusCode != http.StatusOK {
			lastErr = fmt.Errorf("status %d", resp.StatusCode)
			continue
		}
		return body, nil
	}
	return nil, lastErr
}

// DecodeBody returns the link list and whether the body is one at all.
//
// The check exists because a mirror that goes down answers with an HTML error
// page and a status of 200 often enough to matter. Treating that page as an empty
// list would quietly wipe the pool.
func DecodeBody(b []byte) ([]byte, bool) {
	trimmed := bytes.TrimSpace(b)
	if len(trimmed) == 0 {
		return nil, false
	}
	if trimmed[0] == '<' {
		return nil, false
	}
	if looksLikeLinks(trimmed) {
		return trimmed, true
	}
	dec, err := decodeBase64(string(trimmed))
	if err != nil || !looksLikeLinks(dec) {
		return nil, false
	}
	return dec, true
}

func looksLikeLinks(b []byte) bool {
	head := b
	if len(head) > 4096 {
		head = head[:4096]
	}
	return bytes.Contains(head, []byte("://"))
}

// Merge combines the subscriptions into one node list.
//
// limit caps the result (0 means no cap) and exists because the TCP stage is the
// only expensive part that scales with the list; a subscription with ten thousand
// lines must not turn one evaluation into an hour.
func Merge(subs []Subscription, limit int) ([]Node, int) {
	seen := make(map[string]bool)
	nodes := make([]Node, 0, 64)
	unsupported := 0
	for _, s := range subs {
		unsupported += len(s.Skipped)
		for _, n := range s.Nodes {
			if seen[n.ID] {
				continue
			}
			seen[n.ID] = true
			nodes = append(nodes, n)
			if limit > 0 && len(nodes) >= limit {
				return nodes, unsupported
			}
		}
	}
	return nodes, unsupported
}

// describeSkip keeps an unparsable line readable in a log without dumping the
// whole list.
func describeSkip(line string) string {
	line = strings.TrimSpace(line)
	if len(line) > 64 {
		return line[:64] + "…"
	}
	return line
}
