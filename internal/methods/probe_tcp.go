package methods

import (
	"context"
	"fmt"
	"strconv"
	"time"
)

const defaultProbeTimeout = 5 * time.Second

// parsePort validates a 1-65535 port string. Shared by probe_tcp and probe_http.
func parsePort(s string) (int, error) {
	if s == "" {
		return 0, fmt.Errorf("param port: required")
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("param port: %w", err)
	}
	if n < 1 || n > 65535 {
		return 0, fmt.Errorf("param port: must be 1-65535, got %d", n)
	}
	return n, nil
}

// parseProbeTimeout reads the timeout param: unset -> 5s, else a Go duration > 0.
func parseProbeTimeout(s string) (time.Duration, error) {
	if s == "" {
		return defaultProbeTimeout, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("param timeout: %w", err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("param timeout: must be > 0, got %s", s)
	}
	return d, nil
}

type probeTCP struct{}

func (probeTCP) Name() string { return "probe_tcp" }
func (probeTCP) Description() string {
	return "Active TCP connect check: does target:port accept a connection?"
}

func (probeTCP) Params() []Param {
	return []Param{
		{Name: "target", Required: true, Description: "host, IP, or DNS name"},
		{Name: "port", Required: true, Description: "TCP port (1-65535)"},
		{Name: "timeout", Description: `connect timeout as a Go duration (default "5s")`},
	}
}

func (probeTCP) OutputFields() []OutputField {
	return []OutputField{
		{Name: "success", Type: FieldBool, Description: "TCP connection established within the timeout"},
		{Name: "latencyMs", Type: FieldInt, Description: "connect time in ms; -1 on failure"},
		{Name: "error", Type: FieldString, Description: `failure reason (refused/timeout/DNS); "" on success`},
	}
}

func (probeTCP) Run(ctx context.Context, deps Deps, params map[string]string) (Outputs, error) {
	if params["target"] == "" {
		return nil, fmt.Errorf("param target: required")
	}
	port, err := parsePort(params["port"])
	if err != nil {
		return nil, err
	}
	timeout, err := parseProbeTimeout(params["timeout"])
	if err != nil {
		return nil, err
	}
	res := deps.Prober.ProbeTCP(ctx, params["target"], port, timeout)
	return Outputs{
		"success":   res.Success,
		"latencyMs": res.LatencyMS,
		"error":     res.Err,
	}, nil
}

func init() { builtinFns = append(builtinFns, func(r *Registry) { r.Register(probeTCP{}) }) }
