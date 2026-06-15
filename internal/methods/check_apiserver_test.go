package methods

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

func TestParseEndpoint(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"", "livez", false},     // unset defaults to livez
		{"livez", "livez", false},
		{"healthz", "healthz", false},
		{"readyz", "", true},     // rejected in v1
		{"bogus", "", true},
	}
	for _, c := range cases {
		got, err := parseEndpoint(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("parseEndpoint(%q): expected error, got nil", c.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseEndpoint(%q): unexpected error %v", c.in, err)
		}
		if got != c.want {
			t.Errorf("parseEndpoint(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestParseFailedChecks(t *testing.T) {
	body := "[+]ping ok\n" +
		"[+]log ok\n" +
		"[-]etcd failed: reason withheld\n" +
		"[+]poststarthook/start-service-ca ok\n" +
		"[-]poststarthook/bootstrap-controller failed\n" +
		"livez check failed\n"
	got := parseFailedChecks([]byte(body))
	want := []map[string]any{
		{"name": "etcd"},
		{"name": "poststarthook/bootstrap-controller"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseFailedChecks = %#v, want %#v", got, want)
	}
}

func TestParseFailedChecksAllHealthy(t *testing.T) {
	body := "[+]ping ok\n[+]etcd ok\nlivez check passed\n"
	got := parseFailedChecks([]byte(body))
	if len(got) != 0 {
		t.Errorf("parseFailedChecks on healthy body = %#v, want empty", got)
	}
	// Must be non-nil so it serializes as [] not null.
	if got == nil {
		t.Error("parseFailedChecks returned nil, want non-nil empty slice")
	}
}

// healthHandler serves /livez and /healthz with a chosen status code and body,
// and records the last requested path so tests can assert routing.
type healthHandler struct {
	code     int
	body     string
	gotPath  string
	gotQuery string
}

func (h *healthHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.gotPath = r.URL.Path
	h.gotQuery = r.URL.RawQuery
	w.WriteHeader(h.code)
	_, _ = w.Write([]byte(h.body))
}

// newAPIServerClient builds a real clientset pointed at srv.
func newAPIServerClient(t *testing.T, srv *httptest.Server) kubernetes.Interface {
	t.Helper()
	client, err := kubernetes.NewForConfig(&rest.Config{Host: srv.URL})
	if err != nil {
		t.Fatalf("NewForConfig: %v", err)
	}
	return client
}

func TestCheckAPIServerHealthy(t *testing.T) {
	h := &healthHandler{code: 200, body: "[+]ping ok\n[+]etcd ok\nlivez check passed\n"}
	srv := httptest.NewServer(h)
	defer srv.Close()

	m, ok := Builtin().Get("check_apiserver")
	if !ok {
		t.Fatal("check_apiserver not registered")
	}
	out, err := m.Run(context.Background(), Deps{Kube: newAPIServerClient(t, srv)}, map[string]string{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out["healthy"] != true || out["statusCode"] != int64(200) ||
		out["failedCount"] != int64(0) || out["error"] != "" {
		t.Errorf("outputs = %#v", out)
	}
	fc, _ := out["failedChecks"].([]map[string]any)
	if len(fc) != 0 {
		t.Errorf("failedChecks = %#v, want empty", fc)
	}
	// default endpoint is livez, verbose=true
	if h.gotPath != "/livez" || h.gotQuery != "verbose=true" {
		t.Errorf("requested %q?%q, want /livez?verbose=true", h.gotPath, h.gotQuery)
	}
}

func TestCheckAPIServerDegradedIsFindingNotError(t *testing.T) {
	body := "[+]ping ok\n[-]etcd failed: timeout\n[-]poststarthook/x failed\nlivez check failed\n"
	srv := httptest.NewServer(&healthHandler{code: 500, body: body})
	defer srv.Close()

	m, _ := Builtin().Get("check_apiserver")
	out, err := m.Run(context.Background(), Deps{Kube: newAPIServerClient(t, srv)}, map[string]string{})
	if err != nil {
		t.Fatalf("degraded apiserver must not be a Go error: %v", err)
	}
	if out["healthy"] != false || out["statusCode"] != int64(500) || out["failedCount"] != int64(2) {
		t.Errorf("outputs = %#v", out)
	}
	fc, _ := out["failedChecks"].([]map[string]any)
	if len(fc) != 2 || fc[0]["name"] != "etcd" || fc[1]["name"] != "poststarthook/x" {
		t.Errorf("failedChecks = %#v", fc)
	}
}

func TestCheckAPIServerHealthzEndpoint(t *testing.T) {
	h := &healthHandler{code: 200, body: "[+]ping ok\nhealthz check passed\n"}
	srv := httptest.NewServer(h)
	defer srv.Close()

	m, _ := Builtin().Get("check_apiserver")
	_, err := m.Run(context.Background(), Deps{Kube: newAPIServerClient(t, srv)},
		map[string]string{"endpoint": "healthz"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if h.gotPath != "/healthz" {
		t.Errorf("requested %q, want /healthz", h.gotPath)
	}
}

func TestCheckAPIServerUnreachableIsFindingNotError(t *testing.T) {
	srv := httptest.NewServer(&healthHandler{code: 200, body: "ok"})
	client := newAPIServerClient(t, srv)
	srv.Close() // now connections are refused

	m, _ := Builtin().Get("check_apiserver")
	out, err := m.Run(context.Background(), Deps{Kube: client},
		map[string]string{"timeout": "2s"})
	if err != nil {
		t.Fatalf("unreachable apiserver must not be a Go error: %v", err)
	}
	if out["healthy"] != false || out["statusCode"] != int64(0) || out["error"] == "" {
		t.Errorf("outputs = %#v", out)
	}
	fc, _ := out["failedChecks"].([]map[string]any)
	if fc == nil || len(fc) != 0 {
		t.Errorf("failedChecks = %#v, want non-nil empty", fc)
	}
}

func TestCheckAPIServerParamErrors(t *testing.T) {
	srv := httptest.NewServer(&healthHandler{code: 200, body: "ok"})
	defer srv.Close()
	client := newAPIServerClient(t, srv)

	m, _ := Builtin().Get("check_apiserver")
	cases := map[string]map[string]string{
		"bad endpoint": {"endpoint": "readyz"},
		"zero timeout": {"timeout": "0s"},
		"bad timeout":  {"timeout": "soon"},
	}
	for name, params := range cases {
		if _, err := m.Run(context.Background(), Deps{Kube: client}, params); err == nil {
			t.Errorf("%s: expected param error, got nil", name)
		}
	}
}
