package methods

import (
	"context"
	"fmt"
	"strconv"

	corev1 "k8s.io/api/core/v1"
)

const (
	defaultLogBytes      = 64 * 1024
	defaultTailLines     = 10
	defaultMaxLineLength = 1000
)

type checkPodLogs struct{}

func (checkPodLogs) Name() string        { return "check_pod_logs" }
func (checkPodLogs) Description() string { return "Container logs (optionally previous instance)" }

func (checkPodLogs) Params() []Param {
	return []Param{
		{Name: "namespace", Required: true, Description: "Pod namespace"},
		{Name: "name", Required: true, Description: "Pod name"},
		{Name: "container", Description: "container name; empty = first container"},
		{Name: "previous", Description: `"true" to fetch the previous instance's logs`},
		{Name: "tailLines", Description: "max lines from the end (integer); defaults to 10"},
		{Name: "maxLineLength", Description: `max characters per line; longer lines are trimmed with a "…[+N chars]" marker (default "1000"; "0" = unlimited)`},
	}
}

func (checkPodLogs) OutputFields() []OutputField {
	return []OutputField{
		{Name: "logs", Type: FieldString, Description: "log text; per line trimmed to maxLineLength, whole blob truncated head+tail if large"},
	}
}

// buildPodLogOptions parses the log params. tailLines defaults to
// defaultTailLines (10) when not supplied, so a flow that just asks for logs
// gets a bounded, summary-friendly amount rather than the whole stream.
func buildPodLogOptions(params map[string]string) (*corev1.PodLogOptions, error) {
	opts := &corev1.PodLogOptions{Container: params["container"]}
	if v := params["previous"]; v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return nil, fmt.Errorf("param previous: %w", err)
		}
		opts.Previous = b
	}
	tail := int64(defaultTailLines)
	if v := params["tailLines"]; v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("param tailLines: %w", err)
		}
		tail = n
	}
	opts.TailLines = &tail
	return opts, nil
}

// parseMaxLineLength reads the maxLineLength param: unset -> defaultMaxLineLength,
// "0" -> 0 (unlimited), a valid non-negative int -> that value; negative or
// non-integer -> error.
func parseMaxLineLength(params map[string]string) (int, error) {
	v := params["maxLineLength"]
	if v == "" {
		return defaultMaxLineLength, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("param maxLineLength: %w", err)
	}
	if n < 0 {
		return 0, fmt.Errorf("param maxLineLength: must be >= 0, got %d", n)
	}
	return n, nil
}

func (checkPodLogs) Run(ctx context.Context, deps Deps, params map[string]string) (Outputs, error) {
	opts, err := buildPodLogOptions(params)
	if err != nil {
		return nil, err
	}
	maxLine, err := parseMaxLineLength(params)
	if err != nil {
		return nil, err
	}
	raw, err := deps.Kube.CoreV1().Pods(params["namespace"]).
		GetLogs(params["name"], opts).DoRaw(ctx)
	if err != nil {
		return nil, fmt.Errorf("get logs %s/%s: %w", params["namespace"], params["name"], err)
	}
	return Outputs{"logs": Truncate(ClampLineLength(string(raw), maxLine), defaultLogBytes)}, nil
}

func init() { builtinFns = append(builtinFns, func(r *Registry) { r.Register(checkPodLogs{}) }) }
