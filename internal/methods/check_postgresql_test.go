package methods

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// pgSecretIn builds a Secret named "db-creds" in the given namespace holding the
// given string keys.
func pgSecretIn(ns string, data map[string]string) *corev1.Secret {
	d := map[string][]byte{}
	for k, v := range data {
		d[k] = []byte(v)
	}
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "db-creds", Namespace: ns},
		Data:       d,
	}
}

// pgSecret builds the "db-creds" Secret in kato's own namespace ("kato").
func pgSecret(data map[string]string) *corev1.Secret { return pgSecretIn("kato", data) }

func pgParams(extra map[string]string) map[string]string {
	p := map[string]string{
		"host":   "10.0.0.5",
		"port":   "5432",
		"dbname": "app",
		"secret": "db-creds",
	}
	for k, v := range extra {
		p[k] = v
	}
	return p
}

func TestCheckPostgresRegistered(t *testing.T) {
	if _, ok := Builtin().Get("read_only_check_postgresql"); !ok {
		t.Fatal("read_only_check_postgresql not registered")
	}
}

func TestCheckPostgresSuccessResolvesCredsFromSecret(t *testing.T) {
	client := fake.NewSimpleClientset(pgSecret(map[string]string{"username": "app", "password": "s3cret"}))
	f := &fakeProber{postgres: PostgresResult{Success: true, ServerVersion: "16.2", LatencyMS: 5}}
	m, _ := Builtin().Get("read_only_check_postgresql")

	out, err := m.Run(context.Background(),
		Deps{Kube: client, Namespace: "kato", Prober: f}, pgParams(nil))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out["success"] != true || out["serverVersion"] != "16.2" || out["latencyMs"] != int64(5) || out["error"] != "" {
		t.Errorf("outputs = %+v", out)
	}
	// Credentials come from the Secret, never from params, and are passed through.
	if f.gotPG.User != "app" || f.gotPG.Password != "s3cret" {
		t.Errorf("creds not resolved from secret: user=%q pass=%q", f.gotPG.User, f.gotPG.Password)
	}
	if f.gotPG.Host != "10.0.0.5" || f.gotPG.Port != 5432 || f.gotPG.DBName != "app" {
		t.Errorf("connection fields = %+v", f.gotPG)
	}
	if f.gotPG.SSLMode != "disable" || f.gotPG.Timeout != 5*time.Second {
		t.Errorf("defaults: sslmode=%q timeout=%v", f.gotPG.SSLMode, f.gotPG.Timeout)
	}
}

func TestCheckPostgresProberFailureIsFinding(t *testing.T) {
	// A prober failure (refused/timeout/auth) is a finding, not a Go error.
	client := fake.NewSimpleClientset(pgSecret(map[string]string{"username": "app", "password": "pw"}))
	f := &fakeProber{postgres: PostgresResult{Success: false, Err: "connection refused", LatencyMS: -1}}
	m, _ := Builtin().Get("read_only_check_postgresql")

	out, err := m.Run(context.Background(), Deps{Kube: client, Namespace: "kato", Prober: f}, pgParams(nil))
	if err != nil {
		t.Fatalf("prober failure must be a finding, not an error: %v", err)
	}
	if out["success"] != false || out["error"] != "connection refused" || out["latencyMs"] != int64(-1) {
		t.Errorf("outputs = %+v", out)
	}
}

func TestCheckPostgresCustomKeys(t *testing.T) {
	client := fake.NewSimpleClientset(pgSecret(map[string]string{"u": "admin", "p": "pw"}))
	f := &fakeProber{postgres: PostgresResult{Success: true}}
	m, _ := Builtin().Get("read_only_check_postgresql")

	_, err := m.Run(context.Background(), Deps{Kube: client, Namespace: "kato", Prober: f},
		pgParams(map[string]string{"usernameKey": "u", "passwordKey": "p"}))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if f.gotPG.User != "admin" || f.gotPG.Password != "pw" {
		t.Errorf("custom keys not honored: user=%q pass=%q", f.gotPG.User, f.gotPG.Password)
	}
}

func TestCheckPostgresSecretNamespace(t *testing.T) {
	// The Secret lives in another namespace; secretNamespace points kato at it.
	// deps.Namespace ("kato") holds no such Secret, so a default lookup would fail.
	client := fake.NewSimpleClientset(pgSecretIn("payments", map[string]string{"username": "app", "password": "pw"}))
	f := &fakeProber{postgres: PostgresResult{Success: true}}
	m, _ := Builtin().Get("read_only_check_postgresql")

	_, err := m.Run(context.Background(), Deps{Kube: client, Namespace: "kato", Prober: f},
		pgParams(map[string]string{"secretNamespace": "payments"}))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if f.gotPG.User != "app" || f.gotPG.Password != "pw" {
		t.Errorf("cross-namespace creds not resolved: %+v", f.gotPG)
	}
}

func TestCheckPostgresMissingPasswordJustTries(t *testing.T) {
	// Option A: a resolvable username but no password → connect with no password.
	client := fake.NewSimpleClientset(pgSecret(map[string]string{"username": "app"}))
	f := &fakeProber{postgres: PostgresResult{Success: true}}
	m, _ := Builtin().Get("read_only_check_postgresql")

	out, err := m.Run(context.Background(), Deps{Kube: client, Namespace: "kato", Prober: f}, pgParams(nil))
	if err != nil {
		t.Fatalf("missing password must not fail the step: %v", err)
	}
	if f.gotPG.User != "app" || f.gotPG.Password != "" {
		t.Errorf("want user=app pass empty, got user=%q pass=%q", f.gotPG.User, f.gotPG.Password)
	}
	if out["success"] != true {
		t.Errorf("outputs = %+v", out)
	}
}

func TestCheckPostgresMissingUsernameFails(t *testing.T) {
	// Option A: username is structurally required by Postgres → a step failure.
	client := fake.NewSimpleClientset(pgSecret(map[string]string{"password": "pw"}))
	f := &fakeProber{postgres: PostgresResult{Success: true}}
	m, _ := Builtin().Get("read_only_check_postgresql")

	if _, err := m.Run(context.Background(), Deps{Kube: client, Namespace: "kato", Prober: f}, pgParams(nil)); err == nil {
		t.Fatal("expected error when the username key is absent from the secret")
	}
}

func TestCheckPostgresSecretNotFound(t *testing.T) {
	client := fake.NewSimpleClientset() // no secret
	f := &fakeProber{}
	m, _ := Builtin().Get("read_only_check_postgresql")

	if _, err := m.Run(context.Background(), Deps{Kube: client, Namespace: "kato", Prober: f}, pgParams(nil)); err == nil {
		t.Fatal("expected error when the referenced secret does not exist")
	}
}

func TestCheckPostgresRequiredParams(t *testing.T) {
	client := fake.NewSimpleClientset(pgSecret(map[string]string{"username": "app", "password": "pw"}))
	f := &fakeProber{postgres: PostgresResult{Success: true}}
	m, _ := Builtin().Get("read_only_check_postgresql")

	cases := map[string]map[string]string{
		"missing host":   {"host": "", "port": "5432", "dbname": "app", "secret": "db-creds"},
		"missing port":   {"host": "h", "port": "", "dbname": "app", "secret": "db-creds"},
		"bad port":       {"host": "h", "port": "70000", "dbname": "app", "secret": "db-creds"},
		"missing dbname": {"host": "h", "port": "5432", "dbname": "", "secret": "db-creds"},
		"missing secret": {"host": "h", "port": "5432", "dbname": "app", "secret": ""},
	}
	for name, params := range cases {
		if _, err := m.Run(context.Background(), Deps{Kube: client, Namespace: "kato", Prober: f}, params); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func TestCheckPostgresSSLMode(t *testing.T) {
	client := fake.NewSimpleClientset(pgSecret(map[string]string{"username": "app", "password": "pw"}))
	m, _ := Builtin().Get("read_only_check_postgresql")

	// require is passed straight through.
	f := &fakeProber{postgres: PostgresResult{Success: true}}
	if _, err := m.Run(context.Background(), Deps{Kube: client, Namespace: "kato", Prober: f},
		pgParams(map[string]string{"sslmode": "require"})); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if f.gotPG.SSLMode != "require" {
		t.Errorf("sslmode = %q, want require", f.gotPG.SSLMode)
	}

	// An unknown sslmode is a misconfiguration → step failure.
	if _, err := m.Run(context.Background(), Deps{Kube: client, Namespace: "kato", Prober: f},
		pgParams(map[string]string{"sslmode": "bogus"})); err == nil {
		t.Error("expected error for unknown sslmode")
	}
}

func TestCheckPostgresTimeoutInvalid(t *testing.T) {
	client := fake.NewSimpleClientset(pgSecret(map[string]string{"username": "app", "password": "pw"}))
	f := &fakeProber{postgres: PostgresResult{Success: true}}
	m, _ := Builtin().Get("read_only_check_postgresql")

	if _, err := m.Run(context.Background(), Deps{Kube: client, Namespace: "kato", Prober: f},
		pgParams(map[string]string{"timeout": "nope"})); err == nil {
		t.Error("expected error for invalid timeout")
	}
}
