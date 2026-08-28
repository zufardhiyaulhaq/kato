package methods

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// secretData reads a Secret by namespace/name and returns its data map. A
// missing Secret (or any read error) is a misconfiguration, surfaced as an error
// so the step fails with a clear reason.
func secretData(ctx context.Context, deps Deps, namespace, name string) (map[string][]byte, error) {
	sec, err := deps.Kube.CoreV1().Secrets(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("get secret %s/%s: %w", namespace, name, err)
	}
	return sec.Data, nil
}

// secretNS resolves where to read a check's credential Secret: the explicit
// secretNamespace param, or kato's own namespace (deps.Namespace) when omitted.
func secretNS(deps Deps, params map[string]string) string {
	if ns := params["secretNamespace"]; ns != "" {
		return ns
	}
	return deps.Namespace
}

type checkPostgres struct{}

func (checkPostgres) Name() string { return "read_only_check_postgresql" }
func (checkPostgres) Description() string {
	return "Active PostgreSQL connectivity check: connect with credentials from a Secret and run SELECT 1"
}

func (checkPostgres) Params() []Param {
	return []Param{
		{Name: "host", Required: true, Description: "host, IP, or DNS name"},
		{Name: "port", Required: true, Description: "TCP port (1-65535)"},
		{Name: "dbname", Required: true, Description: "database to connect to"},
		{Name: "secret", Required: true, Description: "name of a Secret holding the credentials"},
		{Name: "secretNamespace", Description: "namespace of the Secret (default: kato's own namespace)"},
		{Name: "usernameKey", Description: `key in the Secret holding the username (default "username")`},
		{Name: "passwordKey", Description: `key in the Secret holding the password (default "password"); empty/absent = connect with no password`},
		{Name: "sslmode", Description: `disable/allow/prefer/require/verify-ca/verify-full; empty = disable (no SSL)`},
		{Name: "timeout", Description: `whole-operation timeout as a Go duration (default "5s")`},
	}
}

func (checkPostgres) OutputFields() []OutputField {
	return []OutputField{
		{Name: "success", Type: FieldBool, Description: "connected and SELECT 1 returned 1"},
		{Name: "serverVersion", Type: FieldString, Description: `PostgreSQL server_version; "" on failure`},
		{Name: "latencyMs", Type: FieldInt, Description: "connect + query in ms; -1 on failure"},
		{Name: "error", Type: FieldString, Description: `failure reason (dial/auth/timeout/TLS); "" on success`},
	}
}

// normalizeSSLMode maps an empty sslmode to "disable" (no SSL) and rejects any
// value libpq/pgx would not understand.
func normalizeSSLMode(s string) (string, error) {
	switch s {
	case "":
		return "disable", nil
	case "disable", "allow", "prefer", "require", "verify-ca", "verify-full":
		return s, nil
	}
	return "", fmt.Errorf("param sslmode: must be one of disable/allow/prefer/require/verify-ca/verify-full, got %q", s)
}

func (checkPostgres) Run(ctx context.Context, deps Deps, params map[string]string) (Outputs, error) {
	if params["host"] == "" {
		return nil, fmt.Errorf("param host: required")
	}
	port, err := parsePort(params["port"])
	if err != nil {
		return nil, err
	}
	if params["dbname"] == "" {
		return nil, fmt.Errorf("param dbname: required")
	}
	if params["secret"] == "" {
		return nil, fmt.Errorf("param secret: required")
	}
	sslmode, err := normalizeSSLMode(params["sslmode"])
	if err != nil {
		return nil, err
	}
	timeout, err := parseProbeTimeout(params["timeout"])
	if err != nil {
		return nil, err
	}

	ns := secretNS(deps, params)
	data, err := secretData(ctx, deps, ns, params["secret"])
	if err != nil {
		return nil, err
	}
	usernameKey := params["usernameKey"]
	if usernameKey == "" {
		usernameKey = "username"
	}
	passwordKey := params["passwordKey"]
	if passwordKey == "" {
		passwordKey = "password"
	}
	// Postgres always needs a user, so an unresolvable username is a config error.
	// A password is best-effort: absent/empty means connect without one.
	user := string(data[usernameKey])
	if user == "" {
		return nil, fmt.Errorf("secret %s/%s has no non-empty key %q for the username",
			ns, params["secret"], usernameKey)
	}
	password := string(data[passwordKey])

	res := deps.Prober.ProbePostgres(ctx, PostgresProbeRequest{
		Host:     params["host"],
		Port:     port,
		User:     user,
		Password: password,
		DBName:   params["dbname"],
		SSLMode:  sslmode,
		Timeout:  timeout,
	})
	return Outputs{
		"success":       res.Success,
		"serverVersion": res.ServerVersion,
		"latencyMs":     res.LatencyMS,
		"error":         res.Err,
	}, nil
}

func init() {
	builtinFns = append(builtinFns, func(r *Registry) { r.Register(checkPostgres{}) })
}
