package methods

import (
	"context"
	"testing"
	"time"

	"k8s.io/client-go/kubernetes/fake"
)

func TestCheckRedisRegistered(t *testing.T) {
	if _, ok := Builtin().Get("ping_check_redis"); !ok {
		t.Fatal("ping_check_redis not registered")
	}
}

func TestCheckRedisNoSecretJustPings(t *testing.T) {
	f := &fakeProber{redis: RedisResult{Success: true, LatencyMS: 3}}
	m, _ := Builtin().Get("ping_check_redis")

	out, err := m.Run(context.Background(), Deps{Prober: f},
		map[string]string{"host": "10.0.0.9", "port": "6379"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out["success"] != true || out["latencyMs"] != int64(3) || out["error"] != "" {
		t.Errorf("outputs = %+v", out)
	}
	// No secret → no AUTH sent.
	if f.gotRedis.Username != "" || f.gotRedis.Password != "" {
		t.Errorf("expected no auth, got user=%q pass=%q", f.gotRedis.Username, f.gotRedis.Password)
	}
	if f.gotRedis.Host != "10.0.0.9" || f.gotRedis.Port != 6379 || f.gotRedis.Timeout != 5*time.Second {
		t.Errorf("connection fields = %+v", f.gotRedis)
	}
}

func TestCheckRedisProberFailureIsFinding(t *testing.T) {
	// A prober failure (refused/timeout/NOAUTH) is a finding, not a Go error.
	f := &fakeProber{redis: RedisResult{Success: false, Err: "connection refused", LatencyMS: -1}}
	m, _ := Builtin().Get("ping_check_redis")

	out, err := m.Run(context.Background(), Deps{Prober: f},
		map[string]string{"host": "h", "port": "6379"})
	if err != nil {
		t.Fatalf("prober failure must be a finding, not an error: %v", err)
	}
	if out["success"] != false || out["error"] != "connection refused" || out["latencyMs"] != int64(-1) {
		t.Errorf("outputs = %+v", out)
	}
}

func TestCheckRedisPasswordOnlyFromSecret(t *testing.T) {
	client := fake.NewSimpleClientset(pgSecret(map[string]string{"username": "ignored", "password": "pw"}))
	f := &fakeProber{redis: RedisResult{Success: true}}
	m, _ := Builtin().Get("ping_check_redis")

	_, err := m.Run(context.Background(), Deps{Kube: client, Namespace: "kato", Prober: f},
		map[string]string{"host": "h", "port": "6379", "secret": "db-creds"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// Redis default is password-only AUTH: usernameKey is NOT looked up unless the
	// caller sets it, so the secret's "username" key is deliberately ignored here.
	if f.gotRedis.Password != "pw" {
		t.Errorf("password = %q, want pw", f.gotRedis.Password)
	}
	if f.gotRedis.Username != "" {
		t.Errorf("username = %q, want empty (no usernameKey given)", f.gotRedis.Username)
	}
}

func TestCheckRedisACLUsernameWhenKeyGiven(t *testing.T) {
	client := fake.NewSimpleClientset(pgSecret(map[string]string{"user": "acl", "password": "pw"}))
	f := &fakeProber{redis: RedisResult{Success: true}}
	m, _ := Builtin().Get("ping_check_redis")

	_, err := m.Run(context.Background(), Deps{Kube: client, Namespace: "kato", Prober: f},
		map[string]string{"host": "h", "port": "6379", "secret": "db-creds", "usernameKey": "user"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if f.gotRedis.Username != "acl" || f.gotRedis.Password != "pw" {
		t.Errorf("want user=acl pass=pw, got user=%q pass=%q", f.gotRedis.Username, f.gotRedis.Password)
	}
}

func TestCheckRedisCustomPasswordKey(t *testing.T) {
	client := fake.NewSimpleClientset(pgSecret(map[string]string{"redis-pw": "sekret"}))
	f := &fakeProber{redis: RedisResult{Success: true}}
	m, _ := Builtin().Get("ping_check_redis")

	_, err := m.Run(context.Background(), Deps{Kube: client, Namespace: "kato", Prober: f},
		map[string]string{"host": "h", "port": "6379", "secret": "db-creds", "passwordKey": "redis-pw"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if f.gotRedis.Password != "sekret" {
		t.Errorf("password = %q, want sekret", f.gotRedis.Password)
	}
}

func TestCheckRedisSecretNamespace(t *testing.T) {
	// Secret in another namespace, reached via secretNamespace.
	client := fake.NewSimpleClientset(pgSecretIn("data", map[string]string{"password": "pw"}))
	f := &fakeProber{redis: RedisResult{Success: true}}
	m, _ := Builtin().Get("ping_check_redis")

	_, err := m.Run(context.Background(), Deps{Kube: client, Namespace: "kato", Prober: f},
		map[string]string{"host": "h", "port": "6379", "secret": "db-creds", "secretNamespace": "data"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if f.gotRedis.Password != "pw" {
		t.Errorf("cross-namespace password not resolved: %q", f.gotRedis.Password)
	}
}

func TestCheckRedisSecretNamedButMissing(t *testing.T) {
	client := fake.NewSimpleClientset() // no secret
	f := &fakeProber{}
	m, _ := Builtin().Get("ping_check_redis")

	// An explicitly named secret that does not exist is a misconfiguration.
	if _, err := m.Run(context.Background(), Deps{Kube: client, Namespace: "kato", Prober: f},
		map[string]string{"host": "h", "port": "6379", "secret": "nope"}); err == nil {
		t.Fatal("expected error when a named secret is missing")
	}
}

func TestCheckRedisRequiredParams(t *testing.T) {
	f := &fakeProber{redis: RedisResult{Success: true}}
	m, _ := Builtin().Get("ping_check_redis")

	cases := map[string]map[string]string{
		"missing host": {"host": "", "port": "6379"},
		"missing port": {"host": "h", "port": ""},
		"bad port":     {"host": "h", "port": "0"},
	}
	for name, params := range cases {
		if _, err := m.Run(context.Background(), Deps{Prober: f}, params); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}
