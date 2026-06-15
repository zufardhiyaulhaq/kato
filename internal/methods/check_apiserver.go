package methods

import (
	"context"
	"fmt"
	"strings"
)

// parseEndpoint validates the endpoint param: unset -> "livez"; "livez"/"healthz"
// accepted; anything else (including "readyz", out of scope in v1) is a param error.
func parseEndpoint(s string) (string, error) {
	switch s {
	case "":
		return "livez", nil
	case "livez", "healthz":
		return s, nil
	default:
		return "", fmt.Errorf("param endpoint: must be livez or healthz, got %q", s)
	}
}

// parseFailedChecks extracts the names of failing health checks from a verbose
// apiserver health body. Each failing check is a line beginning with "[-]"; the
// name is the text between "[-]" and the first space (e.g. "[-]etcd failed: x" ->
// "etcd"). Returns a non-nil (possibly empty) slice so it serializes as [] not null.
func parseFailedChecks(body []byte) []map[string]any {
	out := []map[string]any{}
	for _, line := range strings.Split(string(body), "\n") {
		if !strings.HasPrefix(line, "[-]") {
			continue
		}
		rest := line[len("[-]"):]
		name := rest
		if i := strings.IndexByte(rest, ' '); i >= 0 {
			name = rest[:i]
		}
		if name == "" {
			continue
		}
		out = append(out, map[string]any{"name": name})
	}
	return out
}

type checkAPIServer struct{}

func (checkAPIServer) Name() string { return "check_apiserver" }
func (checkAPIServer) Description() string {
	return "Health of the connected API server via /livez or /healthz (verbose), with failing-check names"
}

func (checkAPIServer) Params() []Param {
	return []Param{
		{Name: "endpoint", Description: `which health path: "livez" (default) | "healthz"`},
		{Name: "timeout", Description: `request timeout as a Go duration (default "5s")`},
	}
}

func (checkAPIServer) OutputFields() []OutputField {
	return []OutputField{
		{Name: "healthy", Type: FieldBool, Description: "the endpoint reported healthy (HTTP 200)"},
		{Name: "statusCode", Type: FieldInt, Description: "HTTP status code; 0 if the API server was unreachable"},
		{Name: "failedCount", Type: FieldInt, Description: "number of failing health checks"},
		{Name: "error", Type: FieldString, Description: `unreachable reason (refused/timeout/DNS); "" otherwise`},
	}
}

func (checkAPIServer) ListOutputs() []ListOutputField {
	return []ListOutputField{{
		Name:        "failedChecks",
		Description: "names of the individual failing health checks, as named on that cluster",
		ItemFields: []OutputField{
			{Name: "name", Type: FieldString, Description: "failing health check name (e.g. etcd)"},
		},
	}}
}

func (checkAPIServer) Run(ctx context.Context, deps Deps, params map[string]string) (Outputs, error) {
	endpoint, err := parseEndpoint(params["endpoint"])
	if err != nil {
		return nil, err
	}
	timeout, err := parseProbeTimeout(params["timeout"])
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	result := deps.Kube.Discovery().RESTClient().
		Get().AbsPath("/" + endpoint).Param("verbose", "true").Do(ctx)

	var code int
	result.StatusCode(&code)

	// statusCode 0 means no HTTP response was received: the API server is
	// unreachable (refused/timeout/DNS). That is a finding, not a Go error.
	if code == 0 {
		msg := "api server unreachable"
		if e := result.Error(); e != nil {
			msg = e.Error()
		}
		return Outputs{
			"healthy":      false,
			"statusCode":   int64(0),
			"failedCount":  int64(0),
			"error":        msg,
			"failedChecks": []map[string]any{},
		}, nil
	}

	// An HTTP response (200 healthy / 500 degraded) was received. The verbose body
	// is present on both; the status code is authoritative, so Raw's error is ignored.
	body, _ := result.Raw()
	failed := parseFailedChecks(body)
	return Outputs{
		"healthy":      code == 200,
		"statusCode":   int64(code),
		"failedCount":  int64(len(failed)),
		"error":        "",
		"failedChecks": failed,
	}, nil
}

func init() { builtinFns = append(builtinFns, func(r *Registry) { r.Register(checkAPIServer{}) }) }
