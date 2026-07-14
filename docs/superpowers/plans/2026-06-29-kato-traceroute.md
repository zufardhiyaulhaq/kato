# probe_traceroute Method Implementation Plan
{% raw %}
> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a `probe_traceroute` built-in method that performs an unprivileged ICMP traceroute and reports reachability, hop count, and a per-hop list.

**Architecture:** A fourth `probe_*` method behind the existing `Prober` seam. `LocalProber` gains an ICMP-datagram-socket traceroute (Approach A — no `CAP_NET_RAW`, no new RBAC). The method maps the result to flat scalar outputs plus a `forEach`-consumable `hops` list, following the exact patterns of `probe_tcp`/`probe_http` (scalars) and `list_nodes` (list output).

**Tech Stack:** Go 1.24, `golang.org/x/net/icmp` + `golang.org/x/net/ipv4` (already vendored, currently indirect), standard `testing` (`httptest`-style hermetic tests, no external network).

**Design doc:** `docs/superpowers/specs/2026-06-29-kato-traceroute-design.md`

## Global Constraints

- **Module:** `github.com/zufardhiyaulhaq/kato`, `go 1.24.8`. All tests run under `go test ./internal/methods/` with **no cluster and no external network**.
- **Probe technique:** unprivileged ICMP **datagram** socket (`icmp.ListenPacket("udp4", "0.0.0.0")`). **Never** a raw socket; **no** `CAP_NET_RAW`. IPv4 only.
- **Failure-is-a-finding:** network/DNS/socket failures return `success:false` + populated `error` and a **nil Go error**. A non-nil Go error is returned **only** for param mistakes.
- **All outputs always present** with defaults. Scalar values are `bool` / `int64` / `string`; the list output is `[]map[string]any`.
- **`-1` means not-applicable** for `hopCount`, `latencyMs`, and per-hop `rttMs` (matching the other probes' `latencyMs:-1`).
- **Registration:** `func init() { builtinFns = append(builtinFns, func(r *Registry) { r.Register(probeTraceroute{}) }) }`.
- **The `hops` list item schema is fixed/statically declared** (no dynamic keys), so `$(item.<field>)` references validate in a `forEach` step.

---

## File Structure

- `internal/methods/prober.go` — **modify:** add `ProbeTraceroute` to the `Prober` interface, the `TracerouteRequest` / `TracerouteResult` / `HopResult` types, the `LocalProber` implementation, and a `peerIP` helper.
- `internal/methods/probe_tcp_test.go` — **modify:** extend the shared `fakeProber` with traceroute fields + `ProbeTraceroute` method (keeps the package's test build green once the interface grows).
- `internal/methods/prober_test.go` — **modify:** add the skip-guarded `LocalProber.ProbeTraceroute` loopback test.
- `internal/methods/probe_traceroute.go` — **create:** the `probeTraceroute` method (params, outputs, `hops` list, `Run`) + a `parseIntRange` helper.
- `internal/methods/probe_traceroute_test.go` — **create:** method tests using the fake `Prober`.
- `go.mod` — **modify (via `go mod tidy`):** promote `golang.org/x/net` from indirect to direct.
- `docs/METHOD.md` — **modify:** add the `probe_traceroute` section.

---

## Task 1: `Prober` seam — types, interface method, `LocalProber` traceroute

**Files:**
- Modify: `internal/methods/prober.go`
- Modify: `internal/methods/probe_tcp_test.go` (extend shared `fakeProber`)
- Test: `internal/methods/prober_test.go`
- Modify: `go.mod` (via `go mod tidy`)

**Interfaces:**
- Consumes: nothing from other tasks.
- Produces (Task 2 relies on these exact names/types):
  - `Prober.ProbeTraceroute(ctx context.Context, req TracerouteRequest) TracerouteResult`
  - `type TracerouteRequest struct { Target string; MaxHops int; Timeout time.Duration; ProbesPerHop int; ResolveNames bool }`
  - `type TracerouteResult struct { Reached bool; HopCount int; DestinationIP string; LatencyMS int64; Hops []HopResult; Err string }`
  - `type HopResult struct { Hop int; Address string; Name string; RTTMS int64; Responded bool; Reached bool }`
  - Extended `fakeProber` fields: `traceroute TracerouteResult`, `gotTrace TracerouteRequest`.

- [ ] **Step 1: Add the interface method, types, implementation, and helper to `prober.go`**

In `internal/methods/prober.go`, add `"golang.org/x/net/icmp"` and `"golang.org/x/net/ipv4"` to the import block (alongside the existing `context`, `fmt`, `net`, `strings`, `time`, etc.).

Add `ProbeTraceroute` to the `Prober` interface (inside the existing `type Prober interface { ... }`, after `ProbeDNS`):

```go
	ProbeTraceroute(ctx context.Context, req TracerouteRequest) TracerouteResult
```

Add the new types (place them after the `DNSResult` type, before `LocalProber`):

```go
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
```

Add the `LocalProber` implementation and the `peerIP` helper (after the existing `LocalProber.ProbeDNS` method):

```go
func (LocalProber) ProbeTraceroute(ctx context.Context, req TracerouteRequest) TracerouteResult {
	res := TracerouteResult{HopCount: -1, LatencyMS: -1}

	ips, err := net.DefaultResolver.LookupIP(ctx, "ip4", req.Target)
	if err != nil || len(ips) == 0 {
		res.Err = fmt.Sprintf("resolve %s: %v", req.Target, err)
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
			wm := icmp.Message{
				Type: ipv4.ICMPTypeEcho, Code: 0,
				Body: &icmp.Echo{ID: 0xCAFE, Seq: ttl*100 + probe, Data: []byte("kato")},
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
			_ = conn.SetReadDeadline(time.Now().Add(req.Timeout))
			rb := make([]byte, 1500)
			n, peer, err := conn.ReadFrom(rb)
			if err != nil {
				continue // timeout: silent hop, try next probe
			}
			rm, err := icmp.ParseMessage(1 /* IPv4 ICMP protocol number */, rb[:n])
			if err != nil {
				continue
			}
			hop.RTTMS = time.Since(start).Milliseconds()
			hop.Responded = true
			hop.Address = peerIP(peer)
			if rm.Type == ipv4.ICMPTypeEchoReply {
				hop.Reached = true
			}
			break // got a reply for this TTL
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
```

- [ ] **Step 2: Extend the shared `fakeProber` so the package test build stays green**

The `Prober` interface just grew, so the test-only `fakeProber` must implement the new method or `go test` will not compile. In `internal/methods/probe_tcp_test.go`, add two fields to the `fakeProber` struct (after `gotDNS DNSProbeRequest`):

```go
	traceroute TracerouteResult
	gotTrace   TracerouteRequest
```

And add the method (after the existing `ProbeDNS` method on `*fakeProber`):

```go
func (f *fakeProber) ProbeTraceroute(_ context.Context, req TracerouteRequest) TracerouteResult {
	f.gotTrace = req
	return f.traceroute
}
```

- [ ] **Step 3: Promote `golang.org/x/net` to a direct dependency**

Run: `cd /Users/zufardhiyaulhaq/Documents/personal/github/kato && go mod tidy`
Expected: `go.mod` now lists `golang.org/x/net v0.30.0` **without** the `// indirect` comment (it moves into the direct `require` block). No other dependency changes.

Verify: `grep 'golang.org/x/net ' go.mod`
Expected: a line `golang.org/x/net v0.30.0` with **no** `// indirect` suffix.

- [ ] **Step 4: Write the failing `LocalProber` loopback test**

In `internal/methods/prober_test.go`, add `"golang.org/x/net/icmp"` to the import block. Then add this test:

```go
func TestLocalProberTracerouteLoopback(t *testing.T) {
	// Unprivileged ICMP datagram socket; skip where the node sysctl
	// net.ipv4.ping_group_range does not cover this process's GID (common on
	// locked-down CI) so the suite never goes flaky.
	if c, err := icmp.ListenPacket("udp4", "0.0.0.0"); err != nil {
		t.Skipf("ICMP datagram socket unavailable (ping_group_range?): %v", err)
	} else {
		_ = c.Close()
	}

	res := LocalProber{}.ProbeTraceroute(context.Background(), TracerouteRequest{
		Target: "127.0.0.1", MaxHops: 5, Timeout: 2 * time.Second, ProbesPerHop: 1,
	})
	if res.Err != "" {
		t.Fatalf("unexpected setup error: %s", res.Err)
	}
	if !res.Reached || res.HopCount != 1 {
		t.Fatalf("loopback: reached=%v hopCount=%d hops=%+v", res.Reached, res.HopCount, res.Hops)
	}
	if res.DestinationIP != "127.0.0.1" {
		t.Errorf("destinationIp = %q, want 127.0.0.1", res.DestinationIP)
	}
	if len(res.Hops) != 1 || !res.Hops[0].Reached || res.Hops[0].Address != "127.0.0.1" {
		t.Errorf("hops = %+v", res.Hops)
	}
}
```

- [ ] **Step 5: Run the `LocalProber` test**

Run: `cd /Users/zufardhiyaulhaq/Documents/personal/github/kato && go test ./internal/methods/ -run TestLocalProberTraceroute -v`
Expected: PASS on macOS/dev (loopback reaches at TTL 1). On a host with a locked-down `ping_group_range`, expect `--- SKIP` with the socket-unavailable reason — **not** a failure.

- [ ] **Step 6: Commit**

```bash
cd /Users/zufardhiyaulhaq/Documents/personal/github/kato
git add internal/methods/prober.go internal/methods/prober_test.go internal/methods/probe_tcp_test.go go.mod go.sum
git commit -m "feat: add ProbeTraceroute to the Prober seam"
```

---

## Task 2: `probe_traceroute` method

**Files:**
- Create: `internal/methods/probe_traceroute.go`
- Test: `internal/methods/probe_traceroute_test.go`

**Interfaces:**
- Consumes (from Task 1): `Prober.ProbeTraceroute`, `TracerouteRequest`, `TracerouteResult`, `HopResult`, and the extended `fakeProber` (`traceroute`, `gotTrace`).
- Consumes (existing helpers): `parseBoolDefault(params, key, def)` from `list_failing_pods.go`; `Builtin().Get(name)`; `Deps`; `Outputs`; `OutputField`; `ListOutputField`; `Param`.
- Produces: the registered `probe_traceroute` method with scalar outputs `success`/`hopCount`/`respondingHops`/`destinationIp`/`latencyMs`/`error` and the `hops` list output.

- [ ] **Step 1: Write the failing method tests**

Create `internal/methods/probe_traceroute_test.go`:

```go
package methods

import (
	"context"
	"testing"
)

func TestProbeTracerouteReachedAndDefaults(t *testing.T) {
	f := &fakeProber{traceroute: TracerouteResult{
		Reached: true, HopCount: 3, DestinationIP: "10.0.0.9", LatencyMS: 7,
		Hops: []HopResult{
			{Hop: 1, Address: "10.0.0.1", RTTMS: 1, Responded: true},
			{Hop: 2, Address: "", RTTMS: -1, Responded: false},
			{Hop: 3, Address: "10.0.0.9", RTTMS: 7, Responded: true, Reached: true},
		},
	}}
	m, ok := Builtin().Get("probe_traceroute")
	if !ok {
		t.Fatal("probe_traceroute not registered")
	}
	out, err := m.Run(context.Background(), Deps{Prober: f}, map[string]string{"target": "svc.cluster.local"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out["success"] != true || out["hopCount"] != int64(3) || out["destinationIp"] != "10.0.0.9" ||
		out["latencyMs"] != int64(7) || out["respondingHops"] != int64(2) || out["error"] != "" {
		t.Errorf("outputs = %#v", out)
	}
	if f.gotTrace.MaxHops != 30 || f.gotTrace.ProbesPerHop != 1 ||
		f.gotTrace.Timeout.String() != "2s" || f.gotTrace.ResolveNames {
		t.Errorf("defaults wrong: %+v", f.gotTrace)
	}
	hops, ok := out["hops"].([]map[string]any)
	if !ok || len(hops) != 3 {
		t.Fatalf("hops = %#v", out["hops"])
	}
	if hops[2]["reached"] != true || hops[2]["address"] != "10.0.0.9" || hops[2]["rttMs"] != int64(7) {
		t.Errorf("hop[2] = %#v", hops[2])
	}
	if hops[1]["responded"] != false || hops[1]["rttMs"] != int64(-1) {
		t.Errorf("silent hop[1] = %#v", hops[1])
	}
}

func TestProbeTracerouteNotReachedIsFinding(t *testing.T) {
	f := &fakeProber{traceroute: TracerouteResult{
		Reached: false, HopCount: -1, DestinationIP: "8.8.8.8", LatencyMS: -1,
		Hops: []HopResult{{Hop: 1, Address: "10.0.0.1", RTTMS: 1, Responded: true}},
	}}
	m, _ := Builtin().Get("probe_traceroute")
	out, err := m.Run(context.Background(), Deps{Prober: f}, map[string]string{"target": "8.8.8.8"})
	if err != nil {
		t.Fatalf("not-reached must not be a Go error: %v", err)
	}
	if out["success"] != false || out["hopCount"] != int64(-1) ||
		out["latencyMs"] != int64(-1) || out["respondingHops"] != int64(1) {
		t.Errorf("outputs = %#v", out)
	}
}

func TestProbeTracerouteParamPassThrough(t *testing.T) {
	f := &fakeProber{traceroute: TracerouteResult{Reached: true, HopCount: 1}}
	m, _ := Builtin().Get("probe_traceroute")
	_, err := m.Run(context.Background(), Deps{Prober: f}, map[string]string{
		"target": "host", "maxHops": "10", "timeout": "500ms", "probesPerHop": "3", "resolveNames": "true",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	g := f.gotTrace
	if g.Target != "host" || g.MaxHops != 10 || g.Timeout.String() != "500ms" ||
		g.ProbesPerHop != 3 || !g.ResolveNames {
		t.Errorf("pass-through wrong: %+v", g)
	}
}

func TestProbeTracerouteParamErrors(t *testing.T) {
	m, _ := Builtin().Get("probe_traceroute")
	cases := map[string]map[string]string{
		"empty target":     {},
		"maxHops zero":      {"target": "x", "maxHops": "0"},
		"maxHops too high":  {"target": "x", "maxHops": "256"},
		"maxHops nonint":    {"target": "x", "maxHops": "lots"},
		"probesPerHop zero": {"target": "x", "probesPerHop": "0"},
		"probesPerHop high": {"target": "x", "probesPerHop": "11"},
		"bad timeout":       {"target": "x", "timeout": "soon"},
		"zero timeout":      {"target": "x", "timeout": "0s"},
		"bad resolveNames":  {"target": "x", "resolveNames": "maybe"},
	}
	for name, params := range cases {
		if _, err := m.Run(context.Background(), Deps{Prober: &fakeProber{}}, params); err == nil {
			t.Errorf("%s: expected param error, got nil", name)
		}
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd /Users/zufardhiyaulhaq/Documents/personal/github/kato && go test ./internal/methods/ -run TestProbeTraceroute -v`
Expected: build/compile failure or FAIL — `probe_traceroute` is not registered yet (`Builtin().Get` returns `ok=false`, the first test hits `t.Fatal("probe_traceroute not registered")`).

- [ ] **Step 3: Write the method**

Create `internal/methods/probe_traceroute.go`:

```go
package methods

import (
	"context"
	"fmt"
	"strconv"
	"time"
)

type probeTraceroute struct{}

func (probeTraceroute) Name() string { return "probe_traceroute" }
func (probeTraceroute) Description() string {
	return "Active ICMP traceroute: is the destination reachable, how many hops away, and where does the path stop?"
}

func (probeTraceroute) Params() []Param {
	return []Param{
		{Name: "target", Required: true, Description: "host, IP, or DNS name (resolved to its first IPv4)"},
		{Name: "maxHops", Description: `maximum TTL to probe before giving up (1-255; default "30")`},
		{Name: "timeout", Description: `per-hop reply wait as a Go duration (default "2s")`},
		{Name: "probesPerHop", Description: `probes sent per TTL (1-10; default "1")`},
		{Name: "resolveNames", Description: `"true" to reverse-DNS each responding hop (default "false")`},
	}
}

func (probeTraceroute) OutputFields() []OutputField {
	return []OutputField{
		{Name: "success", Type: FieldBool, Description: "destination reached (echo-reply received) within maxHops"},
		{Name: "hopCount", Type: FieldInt, Description: "hops to the destination when reached; -1 if not reached"},
		{Name: "respondingHops", Type: FieldInt, Description: "count of hops (TTLs) that returned any reply"},
		{Name: "destinationIp", Type: FieldString, Description: `resolved IPv4 of target; "" if DNS resolution failed`},
		{Name: "latencyMs", Type: FieldInt, Description: "RTT to the destination on the final hop in ms; -1 if not reached"},
		{Name: "error", Type: FieldString, Description: `setup failure (DNS / ICMP socket); "" when the traceroute ran`},
	}
}

func (probeTraceroute) ListOutputs() []ListOutputField {
	return []ListOutputField{{
		Name:        "hops",
		Description: "per-TTL hop records in hop order; silent hops have responded=false and rttMs=-1",
		ItemFields: []OutputField{
			{Name: "hop", Type: FieldInt, Description: "TTL / hop number (1-based)"},
			{Name: "address", Type: FieldString, Description: `responding router IP; "" for a silent hop`},
			{Name: "name", Type: FieldString, Description: `reverse-DNS hostname when resolveNames is set; else ""`},
			{Name: "rttMs", Type: FieldInt, Description: "RTT for this hop in ms; -1 if no reply"},
			{Name: "responded", Type: FieldBool, Description: "a reply was received at this TTL"},
			{Name: "reached", Type: FieldBool, Description: "this hop is the destination (echo-reply)"},
		},
	}}
}

// parseIntRange reads an integer param within [min,max]; unset -> def. A non-integer
// or out-of-range value is a param error.
func parseIntRange(params map[string]string, key string, def, min, max int) (int, error) {
	v := params[key]
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("param %s: %w", key, err)
	}
	if n < min || n > max {
		return 0, fmt.Errorf("param %s: must be %d-%d, got %d", key, min, max, n)
	}
	return n, nil
}

func (probeTraceroute) Run(ctx context.Context, deps Deps, params map[string]string) (Outputs, error) {
	if params["target"] == "" {
		return nil, fmt.Errorf("param target: required")
	}
	maxHops, err := parseIntRange(params, "maxHops", 30, 1, 255)
	if err != nil {
		return nil, err
	}
	probesPerHop, err := parseIntRange(params, "probesPerHop", 1, 1, 10)
	if err != nil {
		return nil, err
	}
	timeout := 2 * time.Second
	if v := params["timeout"]; v != "" {
		d, perr := time.ParseDuration(v)
		if perr != nil {
			return nil, fmt.Errorf("param timeout: %w", perr)
		}
		if d <= 0 {
			return nil, fmt.Errorf("param timeout: must be > 0, got %s", v)
		}
		timeout = d
	}
	resolveNames, err := parseBoolDefault(params, "resolveNames", false)
	if err != nil {
		return nil, err
	}

	res := deps.Prober.ProbeTraceroute(ctx, TracerouteRequest{
		Target:       params["target"],
		MaxHops:      maxHops,
		Timeout:      timeout,
		ProbesPerHop: probesPerHop,
		ResolveNames: resolveNames,
	})

	hops := make([]map[string]any, 0, len(res.Hops))
	responding := 0
	for _, h := range res.Hops {
		if h.Responded {
			responding++
		}
		hops = append(hops, map[string]any{
			"hop": int64(h.Hop), "address": h.Address, "name": h.Name,
			"rttMs": h.RTTMS, "responded": h.Responded, "reached": h.Reached,
		})
	}
	return Outputs{
		"success":        res.Reached,
		"hopCount":       int64(res.HopCount),
		"respondingHops": int64(responding),
		"destinationIp":  res.DestinationIP,
		"latencyMs":      res.LatencyMS,
		"error":          res.Err,
		"hops":           hops,
	}, nil
}

func init() { builtinFns = append(builtinFns, func(r *Registry) { r.Register(probeTraceroute{}) }) }
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd /Users/zufardhiyaulhaq/Documents/personal/github/kato && go test ./internal/methods/ -run TestProbeTraceroute -v`
Expected: PASS — all four `TestProbeTraceroute*` tests green.

- [ ] **Step 5: Run the full methods package + vet**

Run: `cd /Users/zufardhiyaulhaq/Documents/personal/github/kato && go vet ./internal/methods/ && go test ./internal/methods/`
Expected: `ok  github.com/zufardhiyaulhaq/kato/internal/methods` (the loopback test PASS or SKIP; everything else PASS).

- [ ] **Step 6: Commit**

```bash
cd /Users/zufardhiyaulhaq/Documents/personal/github/kato
git add internal/methods/probe_traceroute.go internal/methods/probe_traceroute_test.go
git commit -m "feat: add probe_traceroute method"
```

---

## Task 3: Documentation (`docs/METHOD.md`)

**Files:**
- Modify: `docs/METHOD.md`

**Interfaces:**
- Consumes: the final param/output names from Tasks 1–2. No code depends on this task.

- [ ] **Step 1: Add the `probe_traceroute` section**

In `docs/METHOD.md`, add a new section adjacent to the other `probe_*` methods (e.g. right after the `probe_dns` section's closing `---`). Use the exact content below:

````markdown
### `probe_traceroute`

Active ICMP traceroute from kato's pod: sends echo probes with an increasing IP TTL and
reads the returning `time-exceeded` (intermediate routers) and `echo-reply` (destination)
messages, reporting whether the destination was **reached**, how many **hops** away it is,
and the per-hop path. Scoped to reachability + hop count — when `success` is `false`, the
`hops` list shows *where* replies stopped. A not-reached destination, a DNS failure, or a
blocked ICMP socket is a finding (`success: false`), not an error, so a flow can gate later
steps on `$(steps.<step>.success)`. IPv4 only.

Uses an **unprivileged ICMP datagram socket** — no `CAP_NET_RAW` and no Kubernetes RBAC. Its
one requirement is that the node sysctl `net.ipv4.ping_group_range` includes kato's pod GID
(many distros default it open: `0 2147483647`). Where it is locked down, the probe surfaces
`success: false` with an `error` naming the sysctl — it never crashes and never needs raw
sockets. Reachability is otherwise governed by NetworkPolicy.

Worst-case wall time ≈ `maxHops × probesPerHop × timeout` when the target is unreachable, so
keep the engine step timeout in mind, or lower `maxHops`.

**Inputs**

| Name | Required | Description |
|---|---|---|
| `target` | yes | host, IP, or DNS name (resolved to its first IPv4) |
| `maxHops` | no | maximum TTL to probe before giving up (1–255; default `30`) |
| `timeout` | no | per-hop reply wait as a Go duration (default `2s`) |
| `probesPerHop` | no | probes sent per TTL (1–10; default `1`); `>1` improves accuracy against transient loss |
| `resolveNames` | no | `"true"` to reverse-DNS each responding hop (adds latency; default `"false"`) |

**Scalar outputs**

| Name | Type | Description |
|---|---|---|
| `success` | bool | destination reached (an echo-reply was received) within `maxHops` |
| `hopCount` | int | hops to the destination when reached; `-1` if not reached |
| `respondingHops` | int | count of hops (TTLs) that returned any reply |
| `destinationIp` | string | resolved IPv4 of `target`; `""` if DNS resolution failed |
| `latencyMs` | int | RTT to the destination on the final hop in ms; `-1` if not reached |
| `error` | string | setup failure (DNS / ICMP socket); `""` when the traceroute ran |

**List output `hops`** (one record per probed TTL, in hop order)

| Item field | Type | Description |
|---|---|---|
| `hop` | int | TTL / hop number (1-based) |
| `address` | string | responding router IP; `""` for a silent (`*`) hop |
| `name` | string | reverse-DNS hostname when `resolveNames` is set; else `""` |
| `rttMs` | int | RTT for this hop in ms; `-1` if no reply |
| `responded` | bool | a reply was received at this TTL |
| `reached` | bool | this hop is the destination (echo-reply) |

When `success` is `false`, reference the list from a `forEach` step to surface the path:
`forEach: $(steps.<step>.hops)`, then bind `$(item.address)` / `$(item.hop)` in the step's
`with`. Pair with `probe_tcp`: if a TCP connect fails, a `probe_traceroute` (gated by `when`)
shows whether the path even gets close — distinguishing "service down" from "network path broken".

---
````

- [ ] **Step 2: Verify the doc renders and references are consistent**

Run: `cd /Users/zufardhiyaulhaq/Documents/personal/github/kato && grep -n "probe_traceroute" docs/METHOD.md`
Expected: the new `### \`probe_traceroute\`` heading appears. Eyeball that every param/output name in the tables matches `internal/methods/probe_traceroute.go` exactly (`maxHops`, `probesPerHop`, `resolveNames`, `respondingHops`, `destinationIp`, `hops` item fields).

- [ ] **Step 3: Commit**

```bash
cd /Users/zufardhiyaulhaq/Documents/personal/github/kato
git add docs/METHOD.md
git commit -m "docs: document probe_traceroute method"
```

---

## Final Verification

- [ ] **Run the whole suite**

Run: `cd /Users/zufardhiyaulhaq/Documents/personal/github/kato && go build ./... && go vet ./... && go test ./...`
Expected: build clean, vet clean, all packages `ok` (the `TestLocalProberTracerouteLoopback` test PASS on a permissive host or SKIP on a locked-down one — never FAIL).

---

## Self-Review (completed during plan authoring)

**Spec coverage** — every spec section maps to a task:
- Method identity / params / scalar outputs / `hops` list → Task 2 (method) + Task 1 (types).
- Unprivileged-ICMP-datagram (Approach A) implementation + `ping_group_range` degradation → Task 1 (`LocalProber.ProbeTraceroute`).
- `Prober` seam (interface method + request/result types) → Task 1.
- Error-handling (param errors = Go error; DNS/socket/not-reached = finding) → Task 2 tests (`TestProbeTracerouteParamErrors`, `TestProbeTracerouteNotReachedIsFinding`) + Task 1 implementation.
- Testing strategy (skip-guarded loopback; fake-Prober method tests) → Task 1 Step 4 + Task 2 Step 1.
- `go.mod` x/net promotion → Task 1 Step 3.
- `docs/METHOD.md` + `ping_group_range` note → Task 3.
- Example use cases → captured in the Task 3 doc prose (egress reachability, ingress upstream path, `probe_tcp` triage).

**Placeholder scan** — no TBD/TODO; every code step shows complete code; every command shows expected output.

**Type consistency** — `TracerouteRequest`/`TracerouteResult`/`HopResult` field names (`MaxHops`, `ProbesPerHop`, `ResolveNames`, `RTTMS`, `Responded`, `Reached`, `HopCount`, `LatencyMS`, `DestinationIP`, `Err`) are identical across the Task 1 definition, the Task 1 `LocalProber` body, the `fakeProber` stub, and the Task 2 method/tests. Output keys (`success`, `hopCount`, `respondingHops`, `destinationIp`, `latencyMs`, `error`, `hops`) and `hops` item keys (`hop`, `address`, `name`, `rttMs`, `responded`, `reached`) match between the method, its tests, and the docs.

**Non-goals confirmed out of scope:** `CAP_NET_RAW`/raw sockets, IPv6, AS/geo/MTU, Paris-traceroute, `RemoteProber` — none appear in any task.
{% endraw %}
