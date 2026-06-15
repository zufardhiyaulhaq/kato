package methods

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Prober performs active network probes. LocalProber is the in-process default
// (kato running inside the target cluster). A future RemoteProber will run probes
// inside a registered remote cluster (centralized multi-cluster); it implements the
// same interface, so probe_tcp/probe_http and their UseCases stay unchanged.
type Prober interface {
	ProbeTCP(ctx context.Context, target string, port int, timeout time.Duration) TCPResult
	ProbeHTTP(ctx context.Context, req HTTPProbeRequest) HTTPResult
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
