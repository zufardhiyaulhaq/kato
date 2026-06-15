package methods

import (
	"context"
	"fmt"
	"strconv"
)

type probeHTTP struct{}

func (probeHTTP) Name() string { return "probe_http" }
func (probeHTTP) Description() string {
	return "Active HTTP(S) GET with status and optional body assertions"
}

func (probeHTTP) Params() []Param {
	return []Param{
		{Name: "target", Required: true, Description: "host, IP, or DNS name"},
		{Name: "port", Required: true, Description: "port (1-65535)"},
		{Name: "scheme", Description: `"http" or "https" (default "http")`},
		{Name: "path", Description: `request path (default "/")`},
		{Name: "expectStatus", Description: `expected HTTP status code (default "200")`},
		{Name: "expectBodyContains", Description: "substring the response body must contain; empty = no body check"},
		{Name: "insecureSkipVerify", Description: `"true" to accept self-signed certs (https only; default "false")`},
		{Name: "timeout", Description: `request timeout as a Go duration (default "5s")`},
	}
}

func (probeHTTP) OutputFields() []OutputField {
	return []OutputField{
		{Name: "success", Type: FieldBool, Description: "statusMatched and bodyMatched"},
		{Name: "statusCode", Type: FieldInt, Description: "HTTP status; 0 if no response (transport failure)"},
		{Name: "statusMatched", Type: FieldBool, Description: "statusCode == expectStatus"},
		{Name: "bodyMatched", Type: FieldBool, Description: "body contains expectBodyContains (true if unset)"},
		{Name: "latencyMs", Type: FieldInt, Description: "round-trip in ms; -1 on failure"},
		{Name: "error", Type: FieldString, Description: `failure reason; "" on success`},
	}
}

func (probeHTTP) Run(ctx context.Context, deps Deps, params map[string]string) (Outputs, error) {
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
	scheme := params["scheme"]
	if scheme == "" {
		scheme = "http"
	}
	if scheme != "http" && scheme != "https" {
		return nil, fmt.Errorf(`param scheme: must be "http" or "https", got %q`, scheme)
	}
	path := params["path"]
	if path == "" {
		path = "/"
	}
	expectStatus := 200
	if v := params["expectStatus"]; v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return nil, fmt.Errorf("param expectStatus: %w", err)
		}
		if n < 100 || n > 599 {
			return nil, fmt.Errorf("param expectStatus: must be 100-599, got %d", n)
		}
		expectStatus = n
	}
	insecure := false
	if v := params["insecureSkipVerify"]; v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return nil, fmt.Errorf("param insecureSkipVerify: %w", err)
		}
		insecure = b
	}
	res := deps.Prober.ProbeHTTP(ctx, HTTPProbeRequest{
		Scheme:             scheme,
		Target:             params["target"],
		Port:               port,
		Path:               path,
		ExpectStatus:       expectStatus,
		ExpectBodyContains: params["expectBodyContains"],
		InsecureSkipVerify: insecure,
		Timeout:            timeout,
	})
	return Outputs{
		"success":       res.StatusMatched && res.BodyMatched,
		"statusCode":    int64(res.StatusCode),
		"statusMatched": res.StatusMatched,
		"bodyMatched":   res.BodyMatched,
		"latencyMs":     res.LatencyMS,
		"error":         res.Err,
	}, nil
}

func init() { builtinFns = append(builtinFns, func(r *Registry) { r.Register(probeHTTP{}) }) }
