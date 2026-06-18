package methods

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
		{Name: "container", Description: "container name; empty = all containers"},
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
	ns, name := params["namespace"], params["name"]
	pods := deps.Kube.CoreV1().Pods(ns)

	fetch := func(container string) (string, error) {
		opts.Container = container
		raw, err := pods.GetLogs(name, opts).DoRaw(ctx)
		if err != nil {
			return "", err
		}
		return ClampLineLength(string(raw), maxLine), nil
	}

	// Explicit container: single fetch, hard error on failure (unchanged behavior).
	if opts.Container != "" {
		text, err := fetch(opts.Container)
		if err != nil {
			return nil, fmt.Errorf("get logs %s/%s: %w", ns, name, err)
		}
		return Outputs{"logs": Truncate(text, defaultLogBytes)}, nil
	}

	// No container named: enumerate the pod's containers. The logs subresource
	// rejects an empty container on a multi-container pod, so we name each one.
	pod, err := pods.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("get pod %s/%s: %w", ns, name, err)
	}
	names := podContainerNames(pod)

	// 0 or 1 container: one fetch, no per-container header (preserves single-
	// container output). 0 is degenerate (no real pod has zero containers); an
	// empty container name lets the API pick the sole container.
	if len(names) <= 1 {
		c := ""
		if len(names) == 1 {
			c = names[0]
		}
		text, err := fetch(c)
		if err != nil {
			return nil, fmt.Errorf("get logs %s/%s: %w", ns, name, err)
		}
		return Outputs{"logs": Truncate(text, defaultLogBytes)}, nil
	}

	// Multiple containers: fetch each; per-container failures become inline notes.
	logs := make([]containerLog, 0, len(names))
	for _, c := range names {
		text, err := fetch(c)
		if err != nil {
			logs = append(logs, containerLog{name: c, err: err})
			continue
		}
		logs = append(logs, containerLog{name: c, text: text})
	}
	return Outputs{"logs": Truncate(aggregateContainerLogs(logs), defaultLogBytes)}, nil
}

// podContainerNames returns the pod's regular containers followed by its init
// containers (init containers crash loop too). Ephemeral containers are out of scope.
func podContainerNames(pod *corev1.Pod) []string {
	names := make([]string, 0, len(pod.Spec.Containers)+len(pod.Spec.InitContainers))
	for _, c := range pod.Spec.Containers {
		names = append(names, c.Name)
	}
	for _, c := range pod.Spec.InitContainers {
		names = append(names, c.Name)
	}
	return names
}

// containerLog is one container's log fetch result: text on success, err on failure.
type containerLog struct {
	name string
	text string
	err  error
}

// aggregateContainerLogs renders one labeled block per container. A failed
// fetch becomes an inline "(no logs: …)" note so one container (e.g. a healthy
// sidecar with no previous instance) never fails the whole step.
func aggregateContainerLogs(logs []containerLog) string {
	var b strings.Builder
	for i, cl := range logs {
		if i > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString("=== container: ")
		b.WriteString(cl.name)
		b.WriteString(" ===\n")
		if cl.err != nil {
			b.WriteString("(no logs: ")
			b.WriteString(cl.err.Error())
			b.WriteString(")")
		} else {
			b.WriteString(cl.text)
		}
	}
	return b.String()
}

func init() { builtinFns = append(builtinFns, func(r *Registry) { r.Register(checkPodLogs{}) }) }
