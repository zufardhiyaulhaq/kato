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
			"rttMs": int64(h.RTTMS), "responded": h.Responded, "reached": h.Reached,
		})
	}
	return Outputs{
		"success":        res.Reached,
		"hopCount":       int64(res.HopCount),
		"respondingHops": int64(responding),
		"destinationIp":  res.DestinationIP,
		"latencyMs":      int64(res.LatencyMS),
		"error":          res.Err,
		"hops":           hops,
	}, nil
}

func init() { builtinFns = append(builtinFns, func(r *Registry) { r.Register(probeTraceroute{}) }) }
