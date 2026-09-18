package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

type ProbeMessage struct {
	TestID string `json:"test_id"`
	Label  string `json:"label,omitempty"`
	Nonce  string `json:"nonce,omitempty"`
}

type Observation struct {
	CountryCode   string            `json:"country_code,omitempty"`
	CountryName   string            `json:"country_name,omitempty"`
	GeoIPStatus   string            `json:"geoip_status,omitempty"`
	GeoIPProvider string            `json:"geoip_provider,omitempty"`
	Time          time.Time         `json:"time"`
	TestID        string            `json:"test_id,omitempty"`
	Kind          string            `json:"kind"`
	SourceIP      string            `json:"source_ip"`
	SourcePort    string            `json:"source_port,omitempty"`
	Listener      string            `json:"listener,omitempty"`
	Transport     string            `json:"transport,omitempty"`
	Host          string            `json:"host,omitempty"`
	Method        string            `json:"method,omitempty"`
	Path          string            `json:"path,omitempty"`
	Protocol      string            `json:"protocol,omitempty"`
	Details       map[string]string `json:"details,omitempty"`
}

type Result struct {
	Name        string       `json:"name"`
	PathClass   string       `json:"path_class"`
	Success     bool         `json:"success"`
	Observation *Observation `json:"observation,omitempty"`
	LocalAddr   string       `json:"local_addr,omitempty"`
	Resolved    []string     `json:"resolved,omitempty"`
	Error       string       `json:"error,omitempty"`
	Notes       string       `json:"notes,omitempty"`
}

func main() {
	var (
		domain       = flag.String("domain", "", "zone apex, for example example.com")
		testID       = flag.String("test-id", randomID(), "shared test ID")
		socksURL     = flag.String("socks5", "", "optional SOCKS5 URL, e.g. socks5://127.0.0.1:1080")
		httpProxyRaw = flag.String("http-proxy", "", "optional explicit HTTP proxy URL, e.g. http://127.0.0.1:7890; otherwise uses HTTP_PROXY/HTTPS_PROXY")
		timeout      = flag.Duration("timeout", 8*time.Second, "per-test timeout")
		skipDirect   = flag.Bool("skip-direct", false, "do not intentionally send direct HTTP/TCP/UDP/STUN probes")
		verbose      = flag.Bool("verbose", false, "output full JSON instead of a formatted summary")
	)
	flag.BoolVar(verbose, "v", false, "alias for --verbose")
	flag.Parse()
	if *domain == "" {
		fmt.Fprintln(os.Stderr, "-domain is required")
		os.Exit(2)
	}

	mainHost := "leak." + *domain
	baseURL := "https://" + mainHost
	results := []Result{}

	results = append(results, resolveTest(mainHost, *timeout))
	httpProxyFunc, httpProxyLabel, proxyErr := chooseHTTPProxy(*httpProxyRaw)
	if proxyErr != nil {
		results = append(results, Result{Name: "http-proxy-setup", PathClass: "proxy-aware", Error: proxyErr.Error()})
	} else {
		results = append(results, httpTest("http-"+httpProxyLabel, "proxy-aware", baseURL, *testID, "client-http-"+httpProxyLabel, httpProxyFunc, nil, *timeout))
		results = append(results, httpTest("http-v4-"+httpProxyLabel, "proxy-aware", "https://v4."+*domain, *testID, "client-http-v4-"+httpProxyLabel, httpProxyFunc, nil, *timeout))
		results = append(results, httpTest("http-v6-"+httpProxyLabel, "proxy-aware", "https://v6."+*domain, *testID, "client-http-v6-"+httpProxyLabel, httpProxyFunc, nil, *timeout))
	}

	if *socksURL != "" {
		dialer, err := newSOCKS5Dialer(*socksURL, *timeout)
		if err != nil {
			results = append(results, Result{Name: "socks5-setup", PathClass: "proxy-aware", Error: err.Error()})
		} else {
			results = append(results, httpTest("http-socks5", "proxy-aware", baseURL, *testID, "client-http-socks5", nil, dialer, *timeout))
			results = append(results, tcpTest("tcp-socks5", "proxy-aware", mainHost+":9001", *testID, "client-tcp-socks5", dialer, *timeout))
		}
	}

	if !*skipDirect {
		results = append(results, httpTest("http-direct", "proxy-unaware", baseURL, *testID, "client-http-direct", nil, (&net.Dialer{Timeout: *timeout}).DialContext, *timeout))
		results = append(results, tcpTest("tcp-direct", "proxy-unaware", mainHost+":9001", *testID, "client-tcp-direct", (&net.Dialer{Timeout: *timeout}).DialContext, *timeout))
		results = append(results, udpTest(mainHost+":9002", *testID, *timeout))
		results = append(results, stunTest("stun."+*domain+":3478", *timeout))
	}

	if *verbose {
		report := map[string]any{
			"test_id": *testID,
			"domain":  *domain,
			"results": results,
			"interpretation": []string{
				"proxy-aware tests deliberately use HTTP_PROXY/HTTPS_PROXY or the explicit SOCKS5 connection.",
				"proxy-unaware tests deliberately open ordinary sockets. A system-wide TUN/VPN should still capture them; an application-only proxy normally will not.",
				"The direct probes can reveal the current non-proxied public IP to your own server. Use -skip-direct when that is not desired.",
			},
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(report)
		return
	}
	printSummary(*domain, *testID, results)
}

func chooseHTTPProxy(raw string) (func(*http.Request) (*url.URL, error), string, error) {
	if strings.TrimSpace(raw) == "" {
		return http.ProxyFromEnvironment, "env-proxy", nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, "explicit-proxy", err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, "explicit-proxy", errors.New("HTTP proxy URL must use http:// or https://")
	}
	if u.Host == "" {
		return nil, "explicit-proxy", errors.New("HTTP proxy URL has no host")
	}
	return http.ProxyURL(u), "explicit-proxy", nil
}

func resolveTest(host string, timeout time.Duration) Result {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return Result{Name: "system-dns-resolution", PathClass: "local-observation", Error: err.Error()}
	}
	out := make([]string, 0, len(ips))
	for _, ip := range ips {
		out = append(out, ip.String())
	}
	return Result{Name: "system-dns-resolution", PathClass: "local-observation", Success: true, Resolved: out, Notes: "This shows returned addresses, not which recursive DNS resolver contacted the authoritative server."}
}

func httpTest(name, pathClass, base, testID, label string, proxy func(*http.Request) (*url.URL, error), dialContext func(context.Context, string, string) (net.Conn, error), timeout time.Duration) Result {
	transport := &http.Transport{
		Proxy:                 proxy,
		DialContext:           dialContext,
		TLSHandshakeTimeout:   timeout,
		ResponseHeaderTimeout: timeout,
		ForceAttemptHTTP2:     true,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
	}
	client := &http.Client{Transport: transport, Timeout: timeout}
	u, _ := url.Parse(base + "/api/http")
	q := u.Query()
	q.Set("test_id", testID)
	q.Set("label", label)
	u.RawQuery = q.Encode()
	req, _ := http.NewRequest(http.MethodGet, u.String(), nil)
	resp, err := client.Do(req)
	if err != nil {
		return Result{Name: name, PathClass: pathClass, Error: err.Error()}
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return Result{Name: name, PathClass: pathClass, Error: err.Error()}
	}
	var obs Observation
	if err := json.Unmarshal(body, &obs); err != nil {
		return Result{Name: name, PathClass: pathClass, Error: fmt.Sprintf("decode response: %v; body=%q", err, body)}
	}
	return Result{Name: name, PathClass: pathClass, Success: resp.StatusCode == http.StatusOK, Observation: &obs}
}

func tcpTest(name, pathClass, address, testID, label string, dialContext func(context.Context, string, string) (net.Conn, error), timeout time.Duration) Result {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	conn, err := dialContext(ctx, "tcp", address)
	if err != nil {
		return Result{Name: name, PathClass: pathClass, Error: err.Error()}
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))
	msg, _ := json.Marshal(ProbeMessage{TestID: testID, Label: label, Nonce: randomID()})
	if _, err := conn.Write(append(msg, '\n')); err != nil {
		return Result{Name: name, PathClass: pathClass, Error: err.Error()}
	}
	line, err := bufio.NewReader(io.LimitReader(conn, 64<<10)).ReadBytes('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return Result{Name: name, PathClass: pathClass, Error: err.Error()}
	}
	var obs Observation
	if err := json.Unmarshal(line, &obs); err != nil {
		return Result{Name: name, PathClass: pathClass, Error: fmt.Sprintf("decode response: %v", err)}
	}
	return Result{Name: name, PathClass: pathClass, Success: true, Observation: &obs, LocalAddr: conn.LocalAddr().String()}
}

func udpTest(address, testID string, timeout time.Duration) Result {
	conn, err := net.DialTimeout("udp", address, timeout)
	if err != nil {
		return Result{Name: "udp-direct", PathClass: "proxy-unaware", Error: err.Error()}
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))
	msg, _ := json.Marshal(ProbeMessage{TestID: testID, Label: "client-udp-direct", Nonce: randomID()})
	if _, err := conn.Write(msg); err != nil {
		return Result{Name: "udp-direct", PathClass: "proxy-unaware", Error: err.Error()}
	}
	buf := make([]byte, 2048)
	n, err := conn.Read(buf)
	if err != nil {
		return Result{Name: "udp-direct", PathClass: "proxy-unaware", Error: err.Error(), LocalAddr: conn.LocalAddr().String()}
	}
	var obs Observation
	if err := json.Unmarshal(buf[:n], &obs); err != nil {
		// The server may truncate its equal-sized anti-amplification response.
		return Result{Name: "udp-direct", PathClass: "proxy-unaware", Success: true, LocalAddr: conn.LocalAddr().String(), Notes: "Server received the datagram; inspect dashboard for the full observation."}
	}
	return Result{Name: "udp-direct", PathClass: "proxy-unaware", Success: true, Observation: &obs, LocalAddr: conn.LocalAddr().String()}
}

func stunTest(address string, timeout time.Duration) Result {
	conn, err := net.DialTimeout("udp", address, timeout)
	if err != nil {
		return Result{Name: "stun-direct", PathClass: "proxy-unaware", Error: err.Error()}
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))
	request := make([]byte, 20)
	binary.BigEndian.PutUint16(request[0:2], 0x0001)
	binary.BigEndian.PutUint16(request[2:4], 0)
	binary.BigEndian.PutUint32(request[4:8], 0x2112A442)
	_, _ = rand.Read(request[8:20])
	if _, err := conn.Write(request); err != nil {
		return Result{Name: "stun-direct", PathClass: "proxy-unaware", Error: err.Error()}
	}
	buf := make([]byte, 1500)
	n, err := conn.Read(buf)
	if err != nil {
		return Result{Name: "stun-direct", PathClass: "proxy-unaware", Error: err.Error(), LocalAddr: conn.LocalAddr().String()}
	}
	mapped, err := parseSTUNXORMapped(buf[:n])
	if err != nil {
		return Result{Name: "stun-direct", PathClass: "proxy-unaware", Error: err.Error(), LocalAddr: conn.LocalAddr().String()}
	}
	obs := &Observation{Kind: "stun", SourceIP: mapped.IP.String(), SourcePort: fmt.Sprint(mapped.Port), Transport: "udp", Protocol: "stun-xor-mapped"}
	return Result{Name: "stun-direct", PathClass: "proxy-unaware", Success: true, Observation: obs, LocalAddr: conn.LocalAddr().String()}
}

func parseSTUNXORMapped(msg []byte) (*net.UDPAddr, error) {
	const cookie uint32 = 0x2112A442
	if len(msg) < 20 || binary.BigEndian.Uint16(msg[0:2]) != 0x0101 || binary.BigEndian.Uint32(msg[4:8]) != cookie {
		return nil, errors.New("invalid STUN response")
	}
	end := 20 + int(binary.BigEndian.Uint16(msg[2:4]))
	if end > len(msg) {
		return nil, errors.New("truncated STUN response")
	}
	for pos := 20; pos+4 <= end; {
		typ := binary.BigEndian.Uint16(msg[pos : pos+2])
		ln := int(binary.BigEndian.Uint16(msg[pos+2 : pos+4]))
		start := pos + 4
		if start+ln > end {
			return nil, errors.New("invalid STUN attribute")
		}
		if typ == 0x0020 && ln >= 8 {
			family := msg[start+1]
			port := int(binary.BigEndian.Uint16(msg[start+2:start+4]) ^ uint16(cookie>>16))
			if family == 0x01 && ln >= 8 {
				v := binary.BigEndian.Uint32(msg[start+4:start+8]) ^ cookie
				ip := make(net.IP, 4)
				binary.BigEndian.PutUint32(ip, v)
				return &net.UDPAddr{IP: ip, Port: port}, nil
			}
			if family == 0x02 && ln >= 20 {
				mask := make([]byte, 16)
				binary.BigEndian.PutUint32(mask[:4], cookie)
				copy(mask[4:], msg[8:20])
				ip := make(net.IP, 16)
				for i := 0; i < 16; i++ {
					ip[i] = msg[start+4+i] ^ mask[i]
				}
				return &net.UDPAddr{IP: ip, Port: port}, nil
			}
		}
		pos = start + ((ln + 3) &^ 3)
	}
	return nil, errors.New("XOR-MAPPED-ADDRESS not found")
}

func newSOCKS5Dialer(raw string, timeout time.Duration) (func(context.Context, string, string) (net.Conn, error), error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	if u.Scheme != "socks5" && u.Scheme != "socks5h" {
		return nil, errors.New("SOCKS URL must use socks5:// or socks5h://")
	}
	proxyAddr := u.Host
	if !strings.Contains(proxyAddr, ":") {
		proxyAddr += ":1080"
	}
	var username, password string
	if u.User != nil {
		username = u.User.Username()
		password, _ = u.User.Password()
	}
	return func(ctx context.Context, network, target string) (net.Conn, error) {
		if network != "tcp" && network != "tcp4" && network != "tcp6" {
			return nil, fmt.Errorf("SOCKS5 dialer only supports TCP CONNECT, got %s", network)
		}
		d := net.Dialer{Timeout: timeout}
		conn, err := d.DialContext(ctx, "tcp", proxyAddr)
		if err != nil {
			return nil, err
		}
		if err := socks5Handshake(conn, target, username, password, timeout); err != nil {
			conn.Close()
			return nil, err
		}
		return conn, nil
	}, nil
}

func socks5Handshake(conn net.Conn, target, username, password string, timeout time.Duration) error {
	_ = conn.SetDeadline(time.Now().Add(timeout))
	methods := []byte{0x00}
	if username != "" {
		methods = append(methods, 0x02)
	}
	if _, err := conn.Write(append([]byte{0x05, byte(len(methods))}, methods...)); err != nil {
		return err
	}
	resp := make([]byte, 2)
	if _, err := io.ReadFull(conn, resp); err != nil {
		return err
	}
	if resp[0] != 0x05 || resp[1] == 0xff {
		return errors.New("SOCKS5: no acceptable authentication method")
	}
	if resp[1] == 0x02 {
		if len(username) > 255 || len(password) > 255 {
			return errors.New("SOCKS5 credentials too long")
		}
		auth := []byte{0x01, byte(len(username))}
		auth = append(auth, username...)
		auth = append(auth, byte(len(password)))
		auth = append(auth, password...)
		if _, err := conn.Write(auth); err != nil {
			return err
		}
		if _, err := io.ReadFull(conn, resp); err != nil {
			return err
		}
		if resp[1] != 0x00 {
			return errors.New("SOCKS5 authentication failed")
		}
	} else if resp[1] != 0x00 {
		return fmt.Errorf("SOCKS5 unsupported auth method %d", resp[1])
	}

	host, portText, err := net.SplitHostPort(target)
	if err != nil {
		return err
	}
	port, err := net.LookupPort("tcp", portText)
	if err != nil {
		return err
	}
	req := []byte{0x05, 0x01, 0x00}
	if ip := net.ParseIP(host); ip != nil {
		if ip4 := ip.To4(); ip4 != nil {
			req = append(req, 0x01)
			req = append(req, ip4...)
		} else {
			req = append(req, 0x04)
			req = append(req, ip.To16()...)
		}
	} else {
		if len(host) > 255 {
			return errors.New("target hostname too long")
		}
		req = append(req, 0x03, byte(len(host)))
		req = append(req, host...)
	}
	req = append(req, byte(port>>8), byte(port))
	if _, err := conn.Write(req); err != nil {
		return err
	}
	head := make([]byte, 4)
	if _, err := io.ReadFull(conn, head); err != nil {
		return err
	}
	if head[0] != 0x05 || head[1] != 0x00 {
		return fmt.Errorf("SOCKS5 CONNECT failed, code=%d", head[1])
	}
	var addressLen int
	switch head[3] {
	case 0x01:
		addressLen = 4
	case 0x04:
		addressLen = 16
	case 0x03:
		b := make([]byte, 1)
		if _, err := io.ReadFull(conn, b); err != nil {
			return err
		}
		addressLen = int(b[0])
	default:
		return errors.New("SOCKS5 invalid reply address type")
	}
	_, err = io.CopyN(io.Discard, conn, int64(addressLen+2))
	return err
}

func randomID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

func printSummary(domain, testID string, results []Result) {
	fmt.Println("🔬  Proxy Leak Lab — CLI 探测报告")
	fmt.Println("══════════════════════════════════════════════")
	fmt.Printf("  Test ID  %s\n", testID)
	fmt.Printf("  Domain   %s\n", domain)

	groups := []struct {
		key   string
		title string
	}{
		{"proxy-aware", "📡 proxy-aware — 经代理路径"},
		{"proxy-unaware", "🔌 proxy-unaware — 直连路径"},
		{"local-observation", "🌐 local-observation — 本地观测"},
	}

	for _, g := range groups {
		var items []Result
		for _, r := range results {
			if r.PathClass == g.key {
				items = append(items, r)
			}
		}
		if len(items) == 0 {
			continue
		}
		fmt.Printf("\n%s\n", g.title)
		fmt.Println("──────────────────────────────────────────────────")
		for _, r := range items {
			printResult(r)
		}
	}

	fmt.Println("\n💡 解读")
	fmt.Println("──────────────────────────────────────────────────")
	fmt.Println("  • proxy-aware 走 HTTP_PROXY / HTTPS_PROXY 或 SOCKS5 代理")
	fmt.Println("  • proxy-unaware 直连 socket，TUN/VPN 应能捕获，应用级代理不能")
	fmt.Println("  • 使用 -v / --verbose 输出完整 JSON")
}

func printResult(r Result) {
	mark := "✅"
	if !r.Success {
		mark = "❌"
	}
	if r.Error != "" {
		fmt.Printf("  %s %-26s 失败: %s\n", mark, r.Name, r.Error)
		return
	}
	if r.Observation != nil {
		fmt.Printf("  %s %-26s 源 %s ── %s\n", mark, r.Name, r.Observation.SourceIP, formatGeo(r.Observation))
		return
	}
	if len(r.Resolved) > 0 {
		fmt.Printf("  %s %-26s 解析到 %s\n", mark, r.Name, strings.Join(r.Resolved, ", "))
		if r.Notes != "" {
			fmt.Printf("  %-28s%s\n", "", r.Notes)
		}
		return
	}
	if r.Notes != "" {
		fmt.Printf("  %s %-26s %s\n", mark, r.Name, r.Notes)
		return
	}
	fmt.Printf("  %s %-26s\n", mark, r.Name)
}

func formatGeo(o *Observation) string {
	if o == nil {
		return "未知"
	}
	if o.GeoIPStatus == "ok" {
		name := o.CountryName
		if name == "" {
			name = o.CountryCode
		}
		if o.CountryCode != "" && name != o.CountryCode {
			return fmt.Sprintf("%s (%s)", name, o.CountryCode)
		}
		return name
	}
	return o.GeoIPStatus
}
