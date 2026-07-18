package methods

import (
	"context"
	"fmt"
	"strconv"
)

type probeGRPC struct{}

func (probeGRPC) Name() string { return "probe_grpc" }
func (probeGRPC) Description() string {
	return "Active gRPC health check: is target:port SERVING (grpc.health.v1.Health/Check)?"
}

func (probeGRPC) Params() []Param {
	return []Param{
		{Name: "target", Required: true, Description: "host, IP, or DNS name"},
		{Name: "port", Required: true, Description: "gRPC port (1-65535)"},
		{Name: "service", Description: `health service name; empty = overall server health`},
		{Name: "tls", Description: `"true" for TLS, "false" for plaintext h2c (default "false")`},
		{Name: "insecureSkipVerify", Description: `"true" to accept self-signed certs (TLS only; default "false")`},
		{Name: "serverName", Description: "TLS SNI / cert-name override; empty = derived from target"},
		{Name: "timeout", Description: `whole-operation timeout as a Go duration (default "5s")`},
	}
}

func (probeGRPC) OutputFields() []OutputField {
	return []OutputField{
		{Name: "success", Type: FieldBool, Description: "health status is SERVING"},
		{Name: "status", Type: FieldString, Description: `SERVING/NOT_SERVING/UNKNOWN; "" if the RPC never completed`},
		{Name: "latencyMs", Type: FieldInt, Description: "Check round-trip in ms; -1 on failure"},
		{Name: "error", Type: FieldString, Description: `failure reason (dial/timeout/UNIMPLEMENTED/NotFound); "" on success`},
	}
}

func (probeGRPC) Run(ctx context.Context, deps Deps, params map[string]string) (Outputs, error) {
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
	useTLS := false
	if v := params["tls"]; v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return nil, fmt.Errorf("param tls: %w", err)
		}
		useTLS = b
	}
	insecure := false
	if v := params["insecureSkipVerify"]; v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return nil, fmt.Errorf("param insecureSkipVerify: %w", err)
		}
		insecure = b
	}
	res := deps.Prober.ProbeGRPC(ctx, GRPCProbeRequest{
		Target:             params["target"],
		Port:               port,
		Service:            params["service"],
		TLS:                useTLS,
		InsecureSkipVerify: insecure,
		ServerName:         params["serverName"],
		Timeout:            timeout,
	})
	return Outputs{
		"success":   res.Serving,
		"status":    res.Status,
		"latencyMs": res.LatencyMS,
		"error":     res.Err,
	}, nil
}

func init() { builtinFns = append(builtinFns, func(r *Registry) { r.Register(probeGRPC{}) }) }
