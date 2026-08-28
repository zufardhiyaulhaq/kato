package methods

import (
	"context"
	"fmt"
)

type checkRedis struct{}

func (checkRedis) Name() string { return "ping_check_redis" }
func (checkRedis) Description() string {
	return "Active Redis connectivity check: connect and PING (optional AUTH from a Secret)"
}

func (checkRedis) Params() []Param {
	return []Param{
		{Name: "host", Required: true, Description: "host, IP, or DNS name"},
		{Name: "port", Required: true, Description: "TCP port (1-65535)"},
		{Name: "secret", Description: "name of a Secret holding the AUTH credentials; omit for no-auth Redis"},
		{Name: "secretNamespace", Description: "namespace of the Secret (default: kato's own namespace); used only when secret is set"},
		{Name: "passwordKey", Description: `key in the Secret holding the password (default "password"); used only when secret is set`},
		{Name: "usernameKey", Description: "key in the Secret holding the ACL username; used only when set (Redis AUTH is password-only by default)"},
		{Name: "timeout", Description: `whole-operation timeout as a Go duration (default "5s")`},
	}
}

func (checkRedis) OutputFields() []OutputField {
	return []OutputField{
		{Name: "success", Type: FieldBool, Description: "PONG received"},
		{Name: "latencyMs", Type: FieldInt, Description: "dial + PING in ms; -1 on failure"},
		{Name: "error", Type: FieldString, Description: `failure reason (dial/timeout/NOAUTH/WRONGPASS); "" on success`},
	}
}

func (checkRedis) Run(ctx context.Context, deps Deps, params map[string]string) (Outputs, error) {
	if params["host"] == "" {
		return nil, fmt.Errorf("param host: required")
	}
	port, err := parsePort(params["port"])
	if err != nil {
		return nil, err
	}
	timeout, err := parseProbeTimeout(params["timeout"])
	if err != nil {
		return nil, err
	}

	// Credentials are optional ("just try"). Only when a secret is named do we
	// resolve AUTH from it; a named-but-missing secret is a misconfiguration.
	var username, password string
	if name := params["secret"]; name != "" {
		data, err := secretData(ctx, deps, secretNS(deps, params), name)
		if err != nil {
			return nil, err
		}
		passwordKey := params["passwordKey"]
		if passwordKey == "" {
			passwordKey = "password"
		}
		password = string(data[passwordKey])
		// Redis AUTH is password-only by default; an ACL username is sent only when
		// the caller explicitly points at a key (no "username" default lookup).
		if usernameKey := params["usernameKey"]; usernameKey != "" {
			username = string(data[usernameKey])
		}
	}

	res := deps.Prober.ProbeRedis(ctx, RedisProbeRequest{
		Host:     params["host"],
		Port:     port,
		Username: username,
		Password: password,
		Timeout:  timeout,
	})
	return Outputs{
		"success":   res.Success,
		"latencyMs": res.LatencyMS,
		"error":     res.Err,
	}, nil
}

func init() {
	builtinFns = append(builtinFns, func(r *Registry) { r.Register(checkRedis{}) })
}
