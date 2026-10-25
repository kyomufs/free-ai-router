package pool

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The runner is exercised against the test binary itself acting as a core. That
// keeps the lifecycle test free of any dependency on a real core while still
// proving the contract that matters: the port in the rendered config is the port
// that ends up listening, and stopping really stops.
func TestMain(m *testing.M) {
	if os.Getenv("POOL_TEST_FAKE_CORE") == "1" {
		fakeCore()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func fakeCore() {
	if os.Getenv("POOL_TEST_FAKE_CORE_FAIL") == "1" {
		os.Exit(3)
	}
	var cfgPath, dir string
	for i, arg := range os.Args {
		if arg == "-f" && i+1 < len(os.Args) {
			cfgPath = os.Args[i+1]
		}
		if arg == "-d" && i+1 < len(os.Args) {
			dir = os.Args[i+1]
		}
	}
	body, err := os.ReadFile(cfgPath)
	if err != nil {
		os.Exit(4)
	}
	var cfg struct {
		MixedPort int `json:"mixed-port"`
	}
	if err := json.Unmarshal(body, &cfg); err != nil || cfg.MixedPort == 0 {
		os.Exit(5)
	}
	l, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", itoa(cfg.MixedPort)))
	if err != nil {
		os.Exit(6)
	}
	for {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		// The fake core speaks the one thing the pool needs from a core: an HTTP
		// CONNECT. Anything less would leave the serving path untested, and the
		// serving path is where the provider's TLS check lives.
		go serveFakeProxy(conn, dir)
	}
}

// serveFakeProxy answers CONNECT and splices the connection to the target, and
// records what it was asked to reach so a test can prove the traffic went through
// the tunnel rather than around it.
func serveFakeProxy(c net.Conn, dir string) {
	defer c.Close()
	br := bufio.NewReader(c)
	req, err := http.ReadRequest(br)
	if err != nil || req.Method != http.MethodConnect {
		return
	}
	if dir != "" {
		f, err := os.OpenFile(filepath.Join(dir, "connects.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err == nil {
			fmt.Fprintln(f, req.Host)
			f.Close()
		}
	}
	up, err := net.Dial("tcp", req.Host)
	if err != nil {
		fmt.Fprint(c, "HTTP/1.1 502 Bad Gateway\r\n\r\n")
		return
	}
	defer up.Close()
	fmt.Fprint(c, "HTTP/1.1 200 Connection Established\r\n\r\n")
	go func() {
		_, _ = io.Copy(up, br)
		up.Close()
	}()
	_, _ = io.Copy(c, up)
}

// tunnelConnects returns the addresses the tunnel was asked to reach.
func tunnelConnects(t *testing.T, dir string) []string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(dir, "connects.log"))
	if err != nil {
		return nil
	}
	var out []string
	for _, line := range strings.Split(strings.TrimSpace(string(body)), "\n") {
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

type fakeCoreImpl struct{ fail bool }

func (fakeCoreImpl) ID() string           { return "fakecore" }
func (fakeCoreImpl) Supports(n Node) bool { return n.Proto == "vless" }

func (fakeCoreImpl) Config(n Node, port int) ([]byte, error) {
	return json.Marshal(map[string]any{"mixed-port": port})
}

func TestRunnerStartsAndStops(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("POOL_TEST_FAKE_CORE", "1")

	r := NewRunner(exe, t.TempDir())
	tn, err := r.Start(context.Background(), fakeCoreImpl{}, Node{Proto: "vless", Host: "example.com", Port: 443, Raw: "vless://x"})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if tn.Port < DefaultPortLow || tn.Port > DefaultPortHigh {
		t.Fatalf("port %d outside the pool range", tn.Port)
	}
	conn, err := net.DialTimeout("tcp", tn.Proxy.Host, time.Second)
	if err != nil {
		t.Fatalf("the rendered port is not listening: %v", err)
	}
	conn.Close()
	if !tn.Alive() {
		t.Fatal("tunnel reports dead right after start")
	}

	tn.Stop(0)
	if _, err := net.DialTimeout("tcp", tn.Proxy.Host, 300*time.Millisecond); err == nil {
		t.Fatal("core still listening after stop")
	}
	if tn.Alive() {
		t.Fatal("core still alive after stop")
	}
}

func TestRunnerRejectsACoreThatDies(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("POOL_TEST_FAKE_CORE", "1")
	t.Setenv("POOL_TEST_FAKE_CORE_FAIL", "1")

	r := NewRunner(exe, t.TempDir())
	tn, err := r.Start(context.Background(), fakeCoreImpl{}, Node{Proto: "vless", Host: "example.com", Port: 443, Raw: "vless://x"})
	if err == nil {
		tn.Stop(0)
		t.Fatal("a core that exits immediately must not produce a tunnel")
	}
}

func TestRunnerRejectsUnsupportedProtocol(t *testing.T) {
	r := NewRunner("/nonexistent", "")
	if _, err := r.Start(context.Background(), fakeCoreImpl{}, Node{Proto: "ssrcustom"}); err == nil {
		t.Fatal("an unsupported protocol must be refused before any process starts")
	}
}

// The renderer's contract is that the core itself accepts its output. Validation
// is delegated to the real binary; without it the test says so instead of
// pretending.
func TestMihomoAcceptsRenderedConfigs(t *testing.T) {
	bin := os.Getenv("POOL_TEST_CORE_BIN")
	if bin == "" {
		found, err := exec.LookPath("mihomo")
		if err != nil {
			t.Skip("mihomo not available: set POOL_TEST_CORE_BIN to run the renderer check")
		}
		bin = found
	}
	cases := []struct{ name, raw string }{
		{"vless grpc", `{"name":"node","type":"vless","server":"sca12.myfymain.com","port":8880,"uuid":"8dc7722c-2767-4eea-a28b-2f8daacc07e3","network":"grpc","grpc-opts":{"grpc-service-name":""}}`},
		{"vless ws tls", `{"name":"node","type":"vless","server":"172.64.33.162","port":443,"uuid":"c35050ce-01ab-45a9-91b8-54cec637d0a8","network":"ws","tls":true,"servername":"octopusss5.info","ws-opts":{"path":"/udfyfws","headers":{"Host":"octopusss5.info"}}}`},
		{"vless reality", `{"name":"node","type":"vless","server":"104.16.72.101","port":443,"uuid":"7e544a9d-1111-2222-3333-444455556666","network":"ws","tls":true,"servername":"shegeftihaaa.net","client-fingerprint":"chrome","reality-opts":{"public-key":"jNXHt1yRo0vDuchQlIP6Z0ZvjT3KtzVI-T4E7RoLJS0"}}`},
		{"trojan", `{"name":"node","type":"trojan","server":"t.example","port":443,"password":"secret","sni":"t.example"}`},
		{"hysteria2", `{"name":"node","type":"hysteria2","server":"hy.example","port":8443,"password":"pw","sni":"hy.example"}`},
	}
	core := NewMihomo()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			n := Node{Label: c.name, Raw: c.raw}
			cfg, err := core.Config(n, 38201)
			if err != nil {
				t.Fatalf("render: %v", err)
			}
			dir := t.TempDir()
			path := dir + "/config.json"
			if err := os.WriteFile(path, cfg, 0o600); err != nil {
				t.Fatal(err)
			}
			out, err := exec.Command(bin, "-t", "-d", dir, "-f", path).CombinedOutput()
			if err != nil {
				t.Fatalf("mihomo rejected the config: %v\n%s\nconfig: %s", err, out, cfg)
			}
		})
	}
}
