package methods

import (
	"context"
	"fmt"
	"strings"
)

type probeDNS struct{}

func (probeDNS) Name() string { return "probe_dns" }
func (probeDNS) Description() string {
	return "Active DNS resolution check: does name resolve to an address?"
}

func (probeDNS) Params() []Param {
	return []Param{
		{Name: "name", Required: true, Description: "hostname to resolve (e.g. kubernetes.default.svc.cluster.local)"},
		{Name: "server", Description: "DNS server IP to query directly; empty = pod's configured resolver"},
		{Name: "port", Description: `DNS server port (default "53"); only used with server`},
		{Name: "timeout", Description: `query timeout as a Go duration (default "5s")`},
	}
}

func (probeDNS) OutputFields() []OutputField {
	return []OutputField{
		{Name: "success", Type: FieldBool, Description: "resolution returned at least one address"},
		{Name: "addresses", Type: FieldString, Description: `comma-separated resolved IPs (A/AAAA), sorted; "" if none`},
		{Name: "recordCount", Type: FieldInt, Description: "number of addresses resolved"},
		{Name: "latencyMs", Type: FieldInt, Description: "query time in ms; -1 on failure"},
		{Name: "error", Type: FieldString, Description: `failure reason (NXDOMAIN/timeout/unreachable); "" on success`},
	}
}

func (probeDNS) Run(ctx context.Context, deps Deps, params map[string]string) (Outputs, error) {
	if params["name"] == "" {
		return nil, fmt.Errorf("param name: required")
	}
	port := 53
	if v := params["port"]; v != "" {
		p, err := parsePort(v)
		if err != nil {
			return nil, err
		}
		port = p
	}
	timeout, err := parseProbeTimeout(params["timeout"])
	if err != nil {
		return nil, err
	}
	res := deps.Prober.ProbeDNS(ctx, DNSProbeRequest{
		Name:    params["name"],
		Server:  params["server"],
		Port:    port,
		Timeout: timeout,
	})
	return Outputs{
		"success":     res.Resolved,
		"addresses":   strings.Join(res.Addresses, ", "),
		"recordCount": int64(len(res.Addresses)),
		"latencyMs":   res.LatencyMS,
		"error":       res.Err,
	}, nil
}

func init() { builtinFns = append(builtinFns, func(r *Registry) { r.Register(probeDNS{}) }) }
