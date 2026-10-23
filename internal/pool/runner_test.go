package pool

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"os/exec"
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
	var cfgPath string
	for i, arg := range os.Args {
		if arg == "-f" && i+1 < len(os.Args) {
			cfgPath = os.Args[i+1]
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
		conn.Close()
	}
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
		{"vless grpc", "vless://8dc7722c-2767-4eea-a28b-2f8daacc07e3@sca12.myfymain.com:8880?mode=gun&security=none&encryption=none&type=grpc#🇮🇹"},
		{"vless ws tls", "vless://c35050ce-01ab-45a9-91b8-54cec637d0a8@172.64.33.162:443?path=%2Fudfyfws&security=tls&encryption=none&host=octopusss5.info&type=ws&sni=octopusss5.info#🇫🇷"},
		{"vless reality", "vless://7e544a9d-1111-2222-3333-444455556666@104.16.72.101:443?security=reality&type=ws&sni=shegeftihaaa.net&pbk=jNXHt1yRo0vDuchQlIP6Z0ZvjT3KtzVI-T4E7RoLJS0&fp=chrome#Tel"},
		{"vless xhttp", "vless://1de37726-744b-46e1-9101-c926b3402e82@h.example:443?type=xhttp&security=tls&path=%2Fx#x"},
		{"trojan", "trojan://secret@t.example:443?security=tls&sni=t.example#🇩🇪"},
		{"hysteria2", "hysteria2://pw@hy.example:8443?sni=hy.example#hy"},
	}
	core := NewMihomo()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			n, err := ParseLink(c.raw)
			if err != nil {
				t.Fatal(err)
			}
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
