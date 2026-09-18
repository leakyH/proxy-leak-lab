package main

import (
	"bufio"
	"crypto/subtle"
	_ "embed"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const maxEvents = 5000

// Event records only network-observation metadata. Sensitive headers and bodies
// are deliberately not persisted.
type Event struct {
	GeoIPInfo
	Time       time.Time         `json:"time"`
	TestID     string            `json:"test_id,omitempty"`
	Kind       string            `json:"kind"`
	SourceIP   string            `json:"source_ip"`
	SourcePort string            `json:"source_port,omitempty"`
	Listener   string            `json:"listener,omitempty"`
	Transport  string            `json:"transport,omitempty"`
	Host       string            `json:"host,omitempty"`
	Method     string            `json:"method,omitempty"`
	Path       string            `json:"path,omitempty"`
	Protocol   string            `json:"protocol,omitempty"`
	Details    map[string]string `json:"details,omitempty"`
}

type Store struct {
	mu       sync.RWMutex
	events   []Event
	dataFile string
}

func NewStore(dataFile string) *Store {
	return &Store{events: make([]Event, 0, 512), dataFile: dataFile}
}

func (s *Store) Add(e Event) {
	s.mu.Lock()
	if len(s.events) >= maxEvents {
		copy(s.events, s.events[len(s.events)-maxEvents+1:])
		s.events = s.events[:maxEvents-1]
	}
	s.events = append(s.events, e)
	s.mu.Unlock()

	if s.dataFile == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(s.dataFile), 0o750); err != nil {
		log.Printf("create data directory: %v", err)
		return
	}
	f, err := os.OpenFile(s.dataFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o640)
	if err != nil {
		log.Printf("open event log: %v", err)
		return
	}
	defer f.Close()
	_ = json.NewEncoder(f).Encode(e)
}

func (s *Store) Find(testID string, since time.Time) []Event {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Event, 0)
	for _, e := range s.events {
		if testID != "" && e.TestID != testID {
			continue
		}
		if !since.IsZero() && e.Time.Before(since) {
			continue
		}
		out = append(out, e)
	}
	return out
}

type rateEntry struct {
	window time.Time
	count  int
}

type RateLimiter struct {
	mu      sync.Mutex
	entries map[string]rateEntry
	limit   int
	window  time.Duration
}

func NewRateLimiter(limit int, window time.Duration) *RateLimiter {
	return &RateLimiter{entries: make(map[string]rateEntry), limit: limit, window: window}
}

func (r *RateLimiter) Allow(ip string) bool {
	now := time.Now()
	r.mu.Lock()
	defer r.mu.Unlock()
	e := r.entries[ip]
	if e.window.IsZero() || now.Sub(e.window) >= r.window {
		r.entries[ip] = rateEntry{window: now, count: 1}
		return true
	}
	if e.count >= r.limit {
		return false
	}
	e.count++
	r.entries[ip] = e
	return true
}

type ProbeMessage struct {
	TestID string `json:"test_id"`
	Label  string `json:"label,omitempty"`
	Nonce  string `json:"nonce,omitempty"`
}

type Server struct {
	store          *Store
	geoip          *GeoIPResolver
	baseDomain     string
	dashboardToken string
	limiter        *RateLimiter
	authLimiter    *RateLimiter
}

func main() {
	log.SetFlags(log.LstdFlags | log.LUTC | log.Lmicroseconds)

	s := &Server{
		store:          NewStore(env("DATA_FILE", "/data/events.jsonl")),
		geoip:          NewGeoIPResolver(env("GEOIP_DB", "/usr/share/GeoIP/GeoLite2-Country.mmdb"), env("GEOIP_LOOKUP_BIN", "mmdblookup")),
		baseDomain:     strings.TrimSpace(os.Getenv("BASE_DOMAIN")),
		dashboardToken: os.Getenv("DASHBOARD_TOKEN"),
		limiter:        NewRateLimiter(120, time.Minute),
		authLimiter:    NewRateLimiter(5, time.Minute),
	}

	errCh := make(chan error, 4)
	go func() { errCh <- s.serveHTTP(env("HTTP_ADDR", ":8080")) }()
	go func() { errCh <- s.serveTCP(env("TCP_ADDR", ":9001")) }()
	go func() { errCh <- s.serveUDP(env("UDP_ADDR", ":9002")) }()
	go func() { errCh <- s.serveSTUN(env("STUN_ADDR", ":3478")) }()

	if err := <-errCh; err != nil {
		log.Fatal(err)
	}
}

func env(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func (s *Server) serveHTTP(addr string) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/api/config", s.handleConfig)
	mux.HandleFunc("/api/http", s.handleHTTPProbe)
	mux.HandleFunc("/api/events", s.handleEvents)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })

	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Lab-Token")
		w.Header().Set("Cache-Control", "no-store")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		mux.ServeHTTP(w, r)
	})

	srv := &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    32 << 10,
	}
	log.Printf("HTTP listening on %s", addr)
	return srv.ListenAndServe()
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.WriteString(w, indexHTML)
}

func (s *Server) handleConfig(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"base_domain": s.baseDomain,
		"tcp_port":    9001,
		"udp_port":    9002,
		"stun_port":   3478,
		"geoip":       s.geoip.Status(),
	})
}

func (s *Server) handleHTTPProbe(w http.ResponseWriter, r *http.Request) {
	edgeIP := r.Header.Get("X-Edge-Remote-IP")
	edgePort := r.Header.Get("X-Edge-Remote-Port")
	if edgeIP == "" {
		edgeIP, edgePort = splitHostPortLoose(r.RemoteAddr)
	}
	if !s.limiter.Allow(edgeIP) {
		http.Error(w, "rate limit", http.StatusTooManyRequests)
		return
	}

	testID := cleanID(r.URL.Query().Get("test_id"))
	details := selectedHTTPDetails(r)
	e := Event{
		Time:       time.Now().UTC(),
		TestID:     testID,
		Kind:       "http",
		SourceIP:   edgeIP,
		SourcePort: edgePort,
		Listener:   r.Host,
		Transport:  r.Header.Get("X-Edge-Scheme"),
		Host:       r.Host,
		Method:     r.Method,
		Path:       r.URL.RequestURI(),
		Protocol:   firstNonEmpty(r.Header.Get("X-Edge-Protocol"), r.Proto),
		Details:    details,
	}
	e = s.recordEvent(e)
	writeJSON(w, http.StatusOK, e)
}

func selectedHTTPDetails(r *http.Request) map[string]string {
	keys := []string{"User-Agent", "Referer", "Via", "Forwarded", "X-Forwarded-For", "X-Real-IP", "CF-Connecting-IP"}
	out := map[string]string{}
	for _, k := range keys {
		if v := strings.TrimSpace(r.Header.Get(k)); v != "" {
			if len(v) > 512 {
				v = v[:512]
			}
			out[strings.ToLower(k)] = v
		}
	}
	if v := r.Header.Get("X-Edge-TLS-Version"); v != "" {
		out["tls_version"] = v
	}
	if label := r.URL.Query().Get("label"); label != "" {
		out["label"] = label
	}
	return out
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		http.Error(w, "dashboard token required", http.StatusUnauthorized)
		return
	}
	testID := cleanID(r.URL.Query().Get("test_id"))
	var since time.Time
	if v := r.URL.Query().Get("since"); v != "" {
		since, _ = time.Parse(time.RFC3339Nano, v)
	}
	writeJSON(w, http.StatusOK, s.store.Find(testID, since))
}

func (s *Server) authorized(r *http.Request) bool {
	if s.dashboardToken == "" {
		return true
	}
	ip, _ := splitHostPortLoose(r.RemoteAddr)
	if !s.authLimiter.Allow(ip) {
		time.Sleep(800 * time.Millisecond)
		return false
	}
	provided := r.Header.Get("X-Lab-Token")
	ok := subtle.ConstantTimeCompare([]byte(provided), []byte(s.dashboardToken)) == 1
	if !ok {
		time.Sleep(800 * time.Millisecond)
	}
	return ok
}

func (s *Server) serveTCP(addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen TCP %s: %w", addr, err)
	}
	log.Printf("TCP probe listening on %s", addr)
	for {
		conn, err := ln.Accept()
		if err != nil {
			return err
		}
		go s.handleTCPConn(conn, addr)
	}
}

func (s *Server) handleTCPConn(conn net.Conn, listener string) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(8 * time.Second))
	ip, port := splitHostPortLoose(conn.RemoteAddr().String())
	if !s.limiter.Allow(ip) {
		return
	}

	line, err := bufio.NewReader(io.LimitReader(conn, 2048)).ReadBytes('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return
	}
	var msg ProbeMessage
	_ = json.Unmarshal(line, &msg)
	msg.TestID = cleanID(msg.TestID)

	e := Event{
		Time:       time.Now().UTC(),
		TestID:     msg.TestID,
		Kind:       "tcp",
		SourceIP:   ip,
		SourcePort: port,
		Listener:   listener,
		Transport:  "tcp",
		Protocol:   "raw-tcp",
		Details:    compactDetails(msg),
	}
	e = s.recordEvent(e)
	_ = json.NewEncoder(conn).Encode(e)
}

func (s *Server) serveUDP(addr string) error {
	pc, err := net.ListenPacket("udp", addr)
	if err != nil {
		return fmt.Errorf("listen UDP %s: %w", addr, err)
	}
	defer pc.Close()
	log.Printf("UDP probe listening on %s", addr)
	buf := make([]byte, 2048)
	for {
		n, peer, err := pc.ReadFrom(buf)
		if err != nil {
			return err
		}
		if n > 1024 {
			continue
		}
		ip, port := splitHostPortLoose(peer.String())
		if !s.limiter.Allow(ip) {
			continue
		}
		var msg ProbeMessage
		_ = json.Unmarshal(buf[:n], &msg)
		msg.TestID = cleanID(msg.TestID)
		e := Event{
			Time:       time.Now().UTC(),
			TestID:     msg.TestID,
			Kind:       "udp",
			SourceIP:   ip,
			SourcePort: port,
			Listener:   addr,
			Transport:  "udp",
			Protocol:   "raw-udp",
			Details:    compactDetails(msg),
		}
		e = s.recordEvent(e)
		response, _ := json.Marshal(e)
		if len(response) > n && n >= 48 {
			response = response[:n]
		}
		_, _ = pc.WriteTo(response, peer)
	}
}

func (s *Server) serveSTUN(addr string) error {
	pc, err := net.ListenPacket("udp", addr)
	if err != nil {
		return fmt.Errorf("listen STUN %s: %w", addr, err)
	}
	defer pc.Close()
	log.Printf("STUN listening on %s", addr)
	buf := make([]byte, 1500)
	for {
		n, peer, err := pc.ReadFrom(buf)
		if err != nil {
			return err
		}
		ip, port := splitHostPortLoose(peer.String())
		if !s.limiter.Allow(ip) {
			continue
		}
		response, err := buildSTUNBindingResponse(buf[:n], peer)
		if err != nil {
			continue
		}
		s.recordEvent(Event{
			Time:       time.Now().UTC(),
			Kind:       "stun",
			SourceIP:   ip,
			SourcePort: port,
			Listener:   addr,
			Transport:  "udp",
			Protocol:   "stun-binding",
		})
		_, _ = pc.WriteTo(response, peer)
	}
}

func buildSTUNBindingResponse(request []byte, peer net.Addr) ([]byte, error) {
	const magicCookie uint32 = 0x2112A442
	if len(request) < 20 || binary.BigEndian.Uint16(request[0:2]) != 0x0001 || binary.BigEndian.Uint32(request[4:8]) != magicCookie {
		return nil, errors.New("not a STUN binding request")
	}
	udpAddr, ok := peer.(*net.UDPAddr)
	if !ok {
		return nil, errors.New("not UDP")
	}
	transactionID := request[8:20]
	ip4 := udpAddr.IP.To4()
	family := byte(0x01)
	attrValueLen := 8
	if ip4 == nil {
		family = 0x02
		attrValueLen = 20
	}
	attributeLen := 4 + attrValueLen
	response := make([]byte, 20+attributeLen)
	binary.BigEndian.PutUint16(response[0:2], 0x0101)
	binary.BigEndian.PutUint16(response[2:4], uint16(attributeLen))
	binary.BigEndian.PutUint32(response[4:8], magicCookie)
	copy(response[8:20], transactionID)
	binary.BigEndian.PutUint16(response[20:22], 0x0020)
	binary.BigEndian.PutUint16(response[22:24], uint16(attrValueLen))
	response[24] = 0
	response[25] = family
	binary.BigEndian.PutUint16(response[26:28], uint16(udpAddr.Port)^uint16(magicCookie>>16))
	if ip4 != nil {
		v := binary.BigEndian.Uint32(ip4) ^ magicCookie
		binary.BigEndian.PutUint32(response[28:32], v)
	} else {
		ip16 := udpAddr.IP.To16()
		if ip16 == nil {
			return nil, errors.New("invalid IP")
		}
		mask := make([]byte, 16)
		binary.BigEndian.PutUint32(mask[:4], magicCookie)
		copy(mask[4:], transactionID)
		for i := 0; i < 16; i++ {
			response[28+i] = ip16[i] ^ mask[i]
		}
	}
	return response, nil
}

func (s *Server) recordEvent(e Event) Event {
	e.GeoIPInfo = s.geoip.Lookup(e.SourceIP)
	s.store.Add(e)
	return e
}

func compactDetails(msg ProbeMessage) map[string]string {
	out := map[string]string{}
	if msg.Label != "" {
		out["label"] = truncate(msg.Label, 128)
	}
	if msg.Nonce != "" {
		out["nonce"] = truncate(msg.Nonce, 128)
	}
	return out
}

func cleanID(v string) string {
	v = strings.TrimSpace(v)
	if len(v) > 128 {
		v = v[:128]
	}
	return v
}

func truncate(v string, max int) string {
	if len(v) <= max {
		return v
	}
	return v[:max]
}

func splitHostPortLoose(v string) (string, string) {
	host, port, err := net.SplitHostPort(v)
	if err == nil {
		return host, port
	}
	return strings.Trim(v, "[]"), ""
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

//go:embed index.html
var indexHTML string
