package pool

import (
	"context"
	"net"
	"sort"
	"strconv"
	"sync"
	"time"
)

// ScanTCP keeps the endpoints that accept a connection and ranks them by how
// fast they did.
//
// It runs before any core is started on purpose: a dead endpoint is free to
// discover with a dial, and a process per dead server would dominate the whole
// evaluation.
func ScanTCP(ctx context.Context, nodes []Node, workers int, timeout time.Duration) []Node {
	if workers < 1 {
		workers = 1
	}
	sem := make(chan struct{}, workers)
	var (
		mu    sync.Mutex
		alive []Node
		wg    sync.WaitGroup
	)
	for _, n := range nodes {
		wg.Add(1)
		sem <- struct{}{}
		go func(n Node) {
			defer wg.Done()
			defer func() { <-sem }()
			start := time.Now()
			conn, err := (&net.Dialer{Timeout: timeout}).DialContext(ctx, "tcp", dialAddress(n))
			if err != nil {
				return
			}
			conn.Close()
			n.TCPRTT = time.Since(start)
			mu.Lock()
			alive = append(alive, n)
			mu.Unlock()
		}(n)
	}
	wg.Wait()
	sort.Slice(alive, func(i, j int) bool { return alive[i].TCPRTT < alive[j].TCPRTT })
	return alive
}

// dialAddress brackets IPv6 literals. net.JoinHostPort does it; going through it
// is also what stops a bare IPv6 host from being read as host:port.
func dialAddress(n Node) string { return net.JoinHostPort(n.Host, strconv.Itoa(n.Port)) }
