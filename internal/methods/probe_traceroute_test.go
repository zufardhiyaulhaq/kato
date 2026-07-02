package methods

import (
	"context"
	"testing"
)

func TestProbeTracerouteReachedAndDefaults(t *testing.T) {
	f := &fakeProber{traceroute: TracerouteResult{
		Reached: true, HopCount: 3, DestinationIP: "10.0.0.9", LatencyMS: 7,
		Hops: []HopResult{
			{Hop: 1, Address: "10.0.0.1", RTTMS: 1, Responded: true},
			{Hop: 2, Address: "", RTTMS: -1, Responded: false},
			{Hop: 3, Address: "10.0.0.9", RTTMS: 7, Responded: true, Reached: true},
		},
	}}
	m, ok := Builtin().Get("probe_traceroute")
	if !ok {
		t.Fatal("probe_traceroute not registered")
	}
	out, err := m.Run(context.Background(), Deps{Prober: f}, map[string]string{"target": "svc.cluster.local"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out["success"] != true || out["hopCount"] != int64(3) || out["destinationIp"] != "10.0.0.9" ||
		out["latencyMs"] != int64(7) || out["respondingHops"] != int64(2) || out["error"] != "" {
		t.Errorf("outputs = %#v", out)
	}
	if f.gotTrace.MaxHops != 30 || f.gotTrace.ProbesPerHop != 1 ||
		f.gotTrace.Timeout.String() != "2s" || f.gotTrace.ResolveNames {
		t.Errorf("defaults wrong: %+v", f.gotTrace)
	}
	hops, ok := out["hops"].([]map[string]any)
	if !ok || len(hops) != 3 {
		t.Fatalf("hops = %#v", out["hops"])
	}
	if hops[2]["reached"] != true || hops[2]["address"] != "10.0.0.9" || hops[2]["rttMs"] != int64(7) {
		t.Errorf("hop[2] = %#v", hops[2])
	}
	if hops[1]["responded"] != false || hops[1]["rttMs"] != int64(-1) {
		t.Errorf("silent hop[1] = %#v", hops[1])
	}
}

func TestProbeTracerouteNotReachedIsFinding(t *testing.T) {
	f := &fakeProber{traceroute: TracerouteResult{
		Reached: false, HopCount: -1, DestinationIP: "8.8.8.8", LatencyMS: -1,
		Hops: []HopResult{{Hop: 1, Address: "10.0.0.1", RTTMS: 1, Responded: true}},
	}}
	m, _ := Builtin().Get("probe_traceroute")
	out, err := m.Run(context.Background(), Deps{Prober: f}, map[string]string{"target": "8.8.8.8"})
	if err != nil {
		t.Fatalf("not-reached must not be a Go error: %v", err)
	}
	if out["success"] != false || out["hopCount"] != int64(-1) ||
		out["latencyMs"] != int64(-1) || out["respondingHops"] != int64(1) {
		t.Errorf("outputs = %#v", out)
	}
}

func TestProbeTracerouteParamPassThrough(t *testing.T) {
	f := &fakeProber{traceroute: TracerouteResult{Reached: true, HopCount: 1}}
	m, _ := Builtin().Get("probe_traceroute")
	_, err := m.Run(context.Background(), Deps{Prober: f}, map[string]string{
		"target": "host", "maxHops": "10", "timeout": "500ms", "probesPerHop": "3", "resolveNames": "true",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	g := f.gotTrace
	if g.Target != "host" || g.MaxHops != 10 || g.Timeout.String() != "500ms" ||
		g.ProbesPerHop != 3 || !g.ResolveNames {
		t.Errorf("pass-through wrong: %+v", g)
	}
}

func TestProbeTracerouteParamErrors(t *testing.T) {
	m, _ := Builtin().Get("probe_traceroute")
	cases := map[string]map[string]string{
		"empty target":     {},
		"maxHops zero":      {"target": "x", "maxHops": "0"},
		"maxHops too high":  {"target": "x", "maxHops": "256"},
		"maxHops nonint":    {"target": "x", "maxHops": "lots"},
		"probesPerHop zero": {"target": "x", "probesPerHop": "0"},
		"probesPerHop high": {"target": "x", "probesPerHop": "11"},
		"bad timeout":       {"target": "x", "timeout": "soon"},
		"zero timeout":      {"target": "x", "timeout": "0s"},
		"bad resolveNames":  {"target": "x", "resolveNames": "maybe"},
	}
	for name, params := range cases {
		if _, err := m.Run(context.Background(), Deps{Prober: &fakeProber{}}, params); err == nil {
			t.Errorf("%s: expected param error, got nil", name)
		}
	}
}
