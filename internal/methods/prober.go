package methods

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health/grpc_health_v1"
)

// Prober performs active network probes. LocalProber is the in-process default
// (kato running inside the target cluster). A future RemoteProber will run probes
// inside a registered remote cluster (centralized multi-cluster); it implements the
// same interface, so probe_tcp/probe_http and their UseCases stay unchanged.
type Prober interface {
	ProbeTCP(ctx context.Context, target string, port int, timeout time.Duration) TCPResult
	ProbeHTTP(ctx context.Context, req HTTPProbeRequest) HTTPResult
	ProbeDNS(ctx context.Context, req DNSProbeRequest) DNSResult
	ProbeTraceroute(ctx context.Context, req TracerouteRequest) TracerouteResult
	ProbeGRPC(ctx context.Context, req GRPCProbeRequest) GRPCResult
}

// TCPResult is the outcome of a TCP connect probe.
type TCPResult struct {
	Success   bool   // connection established within the timeout
	LatencyMS int64  // connect time in ms; -1 on failure
	Err       string // failure reason; "" on success
}

// HTTPProbeRequest is a fully-resolved HTTP probe (params already parsed/defaulted).
type HTTPProbeRequest struct {
	Scheme             string // "http" | "https"
	Target             string
	Port               int
	Path               string
	ExpectStatus       int // expected status code; probe_http defaults this to 200
	ExpectBodyContains string
	InsecureSkipVerify bool
	Timeout            time.Duration
}

// HTTPResult is the outcome of an HTTP probe. Success is composed by the method
// (StatusMatched && BodyMatched); the prober only reports the raw facts.
type HTTPResult struct {
	StatusCode    int
	StatusMatched bool
	BodyMatched   bool
	LatencyMS     int64 // round-trip in ms; -1 on failure
	Err           string
}

// DNSProbeRequest is a fully-resolved DNS probe (params already parsed/defaulted).
type DNSProbeRequest struct {
	Name    string // hostname to resolve
	Server  string // optional resolver IP; "" = pod's /etc/resolv.conf
	Port    int    // resolver port (default 53; only used when Server is set)
	Timeout time.Duration
}

// DNSResult is the outcome of a DNS resolution probe (A/AAAA, resolves-or-not).
type DNSResult struct {
	Resolved  bool     // got >= 1 address
	Addresses []string // resolved A/AAAA IPs, sorted
	LatencyMS int64    // query time in ms; -1 on failure
	Err       string   // failure reason (NXDOMAIN/timeout/unreachable); "" on success
}

// TracerouteRequest is a fully-resolved traceroute (params already parsed/defaulted).
type TracerouteRequest struct {
	Target       string        // host, IP, or DNS name
	MaxHops      int           // maximum TTL to probe (1-255)
	Timeout      time.Duration // per-hop reply wait
	ProbesPerHop int           // probes sent per TTL (1-10)
	ResolveNames bool          // reverse-DNS each responding hop
}

// TracerouteResult is the outcome of a traceroute. Reached/HopCount are the headline
// signals; Hops is the per-TTL path. Err is set only for setup failures (DNS / socket),
// never for "destination not reached" (that is Reached=false with Err="").
type TracerouteResult struct {
	Reached       bool
	HopCount      int    // hops to destination when reached; -1 otherwise
	DestinationIP string // resolved IPv4; "" on DNS failure
	LatencyMS     int64  // RTT to destination on the final hop; -1 if not reached
	Hops          []HopResult
	Err           string
}

// HopResult is one probed TTL.
type HopResult struct {
	Hop       int    // TTL (1-based)
	Address   string // responding IP; "" for a silent hop
	Name      string // reverse-DNS hostname; "" if unresolved or resolveNames off
	RTTMS     int64  // RTT in ms; -1 if no reply
	Responded bool   // a reply came back at this TTL
	Reached   bool   // this hop is the destination
}

// GRPCProbeRequest is a fully-resolved gRPC health probe (params already parsed/defaulted).
type GRPCProbeRequest struct {
	Target             string        // host, IP, or DNS name
	Port               int           // gRPC port
	Service            string        // health service name; "" = overall server health
	TLS                bool          // true = TLS, false = plaintext h2c
	InsecureSkipVerify bool          // skip cert verification (TLS only)
	ServerName         string        // TLS SNI / cert-name override; "" = derived from target
	Timeout            time.Duration // whole-operation bound (dial + Check RPC)
}

// GRPCResult is the outcome of a gRPC health check.
type GRPCResult struct {
	Serving   bool   // health status == SERVING
	Status    string // "SERVING"/"NOT_SERVING"/"UNKNOWN"; "" if the RPC never completed
	LatencyMS int64  // Check round-trip in ms; -1 on failure
	Err       string // failure reason (dial/timeout/UNIMPLEMENTED/NotFound); "" on success
}

// LocalProber probes from the current process. Network reachability is governed by
// NetworkPolicy, not RBAC.
type LocalProber struct{}

func (LocalProber) ProbeTCP(ctx context.Context, target string, port int, timeout time.Duration) TCPResult {
	d := net.Dialer{Timeout: timeout}
	start := time.Now()
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(target, strconv.Itoa(port)))
	if err != nil {
		return TCPResult{Success: false, LatencyMS: -1, Err: err.Error()}
	}
	latency := time.Since(start).Milliseconds()
	_ = conn.Close()
	return TCPResult{Success: true, LatencyMS: latency}
}

func (LocalProber) ProbeHTTP(ctx context.Context, req HTTPProbeRequest) HTTPResult {
	rawURL := fmt.Sprintf("%s://%s/%s", req.Scheme,
		net.JoinHostPort(req.Target, strconv.Itoa(req.Port)), strings.TrimPrefix(req.Path, "/"))
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: req.InsecureSkipVerify},
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Timeout: req.Timeout, Transport: transport}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return HTTPResult{StatusCode: 0, LatencyMS: -1, Err: err.Error()}
	}
	start := time.Now()
	resp, err := client.Do(httpReq)
	if err != nil {
		return HTTPResult{StatusCode: 0, LatencyMS: -1, Err: err.Error()}
	}
	defer resp.Body.Close()
	bodyMatched := true
	if req.ExpectBodyContains != "" {
		// Bounded read so a large/streaming body cannot exhaust memory; body is
		// never retained past the match check (and is never an output).
		b, _ := io.ReadAll(io.LimitReader(resp.Body, defaultLogBytes))
		bodyMatched = strings.Contains(string(b), req.ExpectBodyContains)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return HTTPResult{
		StatusCode:    resp.StatusCode,
		StatusMatched: resp.StatusCode == req.ExpectStatus,
		BodyMatched:   bodyMatched,
		LatencyMS:     time.Since(start).Milliseconds(),
	}
}

func (LocalProber) ProbeDNS(ctx context.Context, req DNSProbeRequest) DNSResult {
	resolver := net.DefaultResolver
	if req.Server != "" {
		d := net.Dialer{Timeout: req.Timeout}
		server := net.JoinHostPort(req.Server, strconv.Itoa(req.Port))
		// PreferGo so Go's own resolver (not cgo) honors Dial, forcing every query
		// to the requested server over the requested network (udp, then tcp on
		// truncation) and ignoring /etc/resolv.conf.
		resolver = &net.Resolver{
			PreferGo: true,
			Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return d.DialContext(ctx, network, server)
			},
		}
	}
	ctx, cancel := context.WithTimeout(ctx, req.Timeout)
	defer cancel()
	start := time.Now()
	addrs, err := resolver.LookupHost(ctx, req.Name)
	if err != nil {
		return DNSResult{Resolved: false, LatencyMS: -1, Err: err.Error()}
	}
	sort.Strings(addrs)
	return DNSResult{Resolved: true, Addresses: addrs, LatencyMS: time.Since(start).Milliseconds()}
}

func (LocalProber) ProbeTraceroute(ctx context.Context, req TracerouteRequest) TracerouteResult {
	res := TracerouteResult{HopCount: -1, LatencyMS: -1}

	ips, err := net.DefaultResolver.LookupIP(ctx, "ip4", req.Target)
	if err != nil {
		res.Err = fmt.Sprintf("resolve %s: %v", req.Target, err)
		return res
	}
	if len(ips) == 0 {
		res.Err = fmt.Sprintf("resolve %s: no IPv4 address", req.Target)
		return res
	}
	res.DestinationIP = ips[0].String()
	dst := &net.UDPAddr{IP: ips[0]}

	// Unprivileged ICMP datagram socket: needs no CAP_NET_RAW, only that the node
	// sysctl net.ipv4.ping_group_range covers this process's GID.
	conn, err := icmp.ListenPacket("udp4", "0.0.0.0")
	if err != nil {
		res.Err = fmt.Sprintf("open ICMP socket: %v (the node sysctl net.ipv4.ping_group_range must include kato's pod GID)", err)
		return res
	}
	defer conn.Close()
	pc := conn.IPv4PacketConn()

	for ttl := 1; ttl <= req.MaxHops; ttl++ {
		if ctx.Err() != nil {
			break // engine step timeout / cancellation
		}
		if err := pc.SetTTL(ttl); err != nil {
			res.Err = fmt.Sprintf("set TTL: %v", err)
			return res
		}
		hop := HopResult{Hop: ttl, RTTMS: -1}
		for probe := 0; probe < req.ProbesPerHop; probe++ {
			if ctx.Err() != nil {
				break
			}
			sentSeq := ttl*100 + probe
			wm := icmp.Message{
				Type: ipv4.ICMPTypeEcho, Code: 0,
				Body: &icmp.Echo{ID: 0xCAFE, Seq: sentSeq, Data: []byte("kato")},
			}
			wb, err := wm.Marshal(nil)
			if err != nil {
				res.Err = fmt.Sprintf("marshal echo: %v", err)
				return res
			}
			start := time.Now()
			if _, err := conn.WriteTo(wb, dst); err != nil {
				continue
			}
			// One deadline shared across the entire match loop for this probe.
			// Cap by the ctx deadline so cancellation is bounded to at most one read window.
			readDeadline := time.Now().Add(req.Timeout)
			if d, ok := ctx.Deadline(); ok && d.Before(readDeadline) {
				readDeadline = d
			}
			_ = conn.SetReadDeadline(readDeadline)
			rb := make([]byte, 1500)
			matched := false
			for {
				n, peer, err := conn.ReadFrom(rb)
				if err != nil {
					break // timeout/deadline: leave this probe silent
				}
				rm, err := icmp.ParseMessage(1 /* IPv4 ICMP protocol number */, rb[:n])
				if err != nil {
					continue
				}
				// Extract the sequence number from the reply.
				// NOTE: on unprivileged SOCK_DGRAM ICMP sockets (Linux/macOS) the
				// kernel overwrites the outgoing Echo ID with the socket's port, so
				// the reply carries a kernel-assigned ID, not our 0xCAFE.  We therefore
				// match ONLY on Seq, which the kernel always preserves.
				var seq int
				switch body := rm.Body.(type) {
				case *icmp.Echo:
					// EchoReply: Seq is directly available.
					seq = body.Seq
				case *icmp.TimeExceeded:
					s, ok := embeddedEchoSeq(body.Data)
					if !ok {
						continue
					}
					seq = s
				default:
					continue // unexpected type: keep reading within the deadline
				}
				if seq != sentSeq {
					continue // stray packet from a different probe: keep reading
				}
				// Matched: record the result and stop reading for this probe.
				hop.RTTMS = time.Since(start).Milliseconds()
				hop.Responded = true
				hop.Address = peerIP(peer)
				hop.Reached = (rm.Type == ipv4.ICMPTypeEchoReply)
				matched = true
				break
			}
			if matched {
				break // got a reply for this TTL, stop probing further
			}
		}
		res.Hops = append(res.Hops, hop)
		if hop.Reached {
			res.Reached = true
			res.HopCount = ttl
			res.LatencyMS = hop.RTTMS
			break
		}
	}

	if req.ResolveNames {
		for i := range res.Hops {
			if res.Hops[i].Address == "" {
				continue
			}
			if names, err := net.DefaultResolver.LookupAddr(ctx, res.Hops[i].Address); err == nil && len(names) > 0 {
				res.Hops[i].Name = strings.TrimSuffix(names[0], ".")
			}
		}
	}
	return res
}

func (LocalProber) ProbeGRPC(ctx context.Context, req GRPCProbeRequest) GRPCResult {
	ctx, cancel := context.WithTimeout(ctx, req.Timeout)
	defer cancel()

	var creds credentials.TransportCredentials
	if req.TLS {
		creds = credentials.NewTLS(&tls.Config{
			InsecureSkipVerify: req.InsecureSkipVerify,
			ServerName:         req.ServerName, // "" -> derived from the dial target
		})
	} else {
		creds = insecure.NewCredentials()
	}

	addr := net.JoinHostPort(req.Target, strconv.Itoa(req.Port))
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(creds))
	if err != nil {
		return GRPCResult{LatencyMS: -1, Err: err.Error()}
	}
	defer conn.Close()

	start := time.Now()
	// grpc.NewClient is lazy; this Check forces the connect, so dial failures,
	// deadline, UNIMPLEMENTED (no health service) and NotFound (service not
	// registered) all surface here as the RPC error — findings, not method errors.
	resp, err := grpc_health_v1.NewHealthClient(conn).
		Check(ctx, &grpc_health_v1.HealthCheckRequest{Service: req.Service})
	if err != nil {
		return GRPCResult{LatencyMS: -1, Err: err.Error()}
	}
	return GRPCResult{
		Serving:   resp.GetStatus() == grpc_health_v1.HealthCheckResponse_SERVING,
		Status:    resp.GetStatus().String(), // SERVING / NOT_SERVING / UNKNOWN
		LatencyMS: time.Since(start).Milliseconds(),
	}
}

// embeddedEchoSeq extracts the ICMP Echo sequence number from the original
// datagram quoted inside a Time Exceeded message (RFC 792: the body carries the
// original IP header + first 8 bytes of our ICMP Echo). Returns ok=false if the
// data is too short or the IP header length is invalid.
func embeddedEchoSeq(data []byte) (int, bool) {
	if len(data) < 1 {
		return 0, false
	}
	ihl := int(data[0]&0x0f) * 4
	if ihl < 20 || len(data) < ihl+8 {
		return 0, false
	}
	// Seq is bytes 6-7 of the 8-byte embedded ICMP header (network byte order).
	return int(binary.BigEndian.Uint16(data[ihl+6 : ihl+8])), true
}

// peerIP renders the responding peer address (a datagram ICMP socket reports it as *net.UDPAddr).
func peerIP(addr net.Addr) string {
	switch a := addr.(type) {
	case *net.UDPAddr:
		return a.IP.String()
	case *net.IPAddr:
		return a.IP.String()
	default:
		return addr.String()
	}
}
