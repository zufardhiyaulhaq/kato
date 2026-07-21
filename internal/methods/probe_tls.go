package methods

import (
	"context"
	"fmt"
	"strconv"
)

type probeTLS struct{}

func (probeTLS) Name() string { return "probe_tls" }
func (probeTLS) Description() string {
	return "Active TLS handshake check with certificate chain verification and expiry facts"
}

func (probeTLS) Params() []Param {
	return []Param{
		{Name: "target", Required: true, Description: "host, IP, or DNS name"},
		{Name: "port", Required: true, Description: "TLS port (1-65535)"},
		{Name: "serverName", Description: "SNI + hostname to verify; empty = derived from target"},
		{Name: "insecureSkipVerify", Description: `"true" to exclude chain/name verification from success (expiry still counts; default "false")`},
		{Name: "timeout", Description: `whole-operation timeout as a Go duration (default "5s")`},
	}
}

func (probeTLS) OutputFields() []OutputField {
	return []OutputField{
		{Name: "success", Type: FieldBool, Description: "handshakeComplete && verified (with insecureSkipVerify: handshakeComplete && !expired)"},
		{Name: "handshakeComplete", Type: FieldBool, Description: "a TLS handshake completed (cert facts are meaningful)"},
		{Name: "verified", Type: FieldBool, Description: "chain + hostname verified against system roots"},
		{Name: "expired", Type: FieldBool, Description: "leaf cert past notAfter"},
		{Name: "daysUntilExpiry", Type: FieldInt, Description: "days until leaf notAfter (floor); negative if expired; gate on handshakeComplete"},
		{Name: "notAfter", Type: FieldString, Description: `leaf expiry, RFC3339; "" if no cert obtained`},
		{Name: "issuer", Type: FieldString, Description: "leaf issuer CN"},
		{Name: "subject", Type: FieldString, Description: "leaf subject CN"},
		{Name: "dnsNames", Type: FieldString, Description: "comma-separated leaf SANs"},
		{Name: "tlsVersion", Type: FieldString, Description: `negotiated version, e.g. "TLS1.3"`},
		{Name: "verifyError", Type: FieldString, Description: `why chain verification failed; "" when verified`},
		{Name: "latencyMs", Type: FieldInt, Description: "dial + handshake in ms; -1 on failure"},
		{Name: "error", Type: FieldString, Description: `transport/handshake failure reason; "" otherwise`},
	}
}

func (probeTLS) Run(ctx context.Context, deps Deps, params map[string]string) (Outputs, error) {
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
	insecure := false
	if v := params["insecureSkipVerify"]; v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return nil, fmt.Errorf("param insecureSkipVerify: %w", err)
		}
		insecure = b
	}
	res := deps.Prober.ProbeTLS(ctx, TLSProbeRequest{
		Target:     params["target"],
		Port:       port,
		ServerName: params["serverName"],
		Timeout:    timeout,
	})
	// Verdict: "TLS is healthy here". insecureSkipVerify excludes chain/name
	// verification (in-cluster internal CAs), but expiry still fails it —
	// expiry monitoring is this probe's primary job.
	success := res.HandshakeComplete && res.Verified
	if insecure {
		success = res.HandshakeComplete && !res.Expired
	}
	return Outputs{
		"success":           success,
		"handshakeComplete": res.HandshakeComplete,
		"verified":          res.Verified,
		"expired":           res.Expired,
		"daysUntilExpiry":   res.DaysUntilExpiry,
		"notAfter":          res.NotAfter,
		"issuer":            res.Issuer,
		"subject":           res.Subject,
		"dnsNames":          res.DNSNames,
		"tlsVersion":        res.TLSVersion,
		"verifyError":       res.VerifyError,
		"latencyMs":         res.LatencyMS,
		"error":             res.Err,
	}, nil
}

func init() { builtinFns = append(builtinFns, func(r *Registry) { r.Register(probeTLS{}) }) }
