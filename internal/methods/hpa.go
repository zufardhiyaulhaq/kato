package methods

import (
	"context"
	"fmt"
	"strings"

	autoscalingv2 "k8s.io/api/autoscaling/v2"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type checkHPA struct{}

func (checkHPA) Name() string { return "check_hpa" }
func (checkHPA) Description() string {
	return "HorizontalPodAutoscaler replica bounds, current scale, metrics, and scaling conditions"
}

func (checkHPA) Params() []Param {
	return []Param{
		{Name: "namespace", Required: true, Description: "HPA namespace"},
		{Name: "name", Required: true, Description: "HPA name"},
	}
}

func (checkHPA) OutputFields() []OutputField {
	return []OutputField{
		{Name: "exists", Type: FieldBool, Description: "HPA exists"},
		{Name: "scaleTarget", Type: FieldString, Description: `scale target, e.g. "Deployment/coredns"`},
		{Name: "minReplicas", Type: FieldInt, Description: "spec.minReplicas (1 if unset)"},
		{Name: "maxReplicas", Type: FieldInt, Description: "spec.maxReplicas"},
		{Name: "currentReplicas", Type: FieldInt, Description: "status.currentReplicas"},
		{Name: "desiredReplicas", Type: FieldInt, Description: "status.desiredReplicas"},
		{Name: "atMax", Type: FieldBool, Description: "currentReplicas >= maxReplicas (saturated, cannot scale out further)"},
		{Name: "ableToScale", Type: FieldBool, Description: "AbleToScale condition is True"},
		{Name: "scalingLimited", Type: FieldBool, Description: "ScalingLimited condition is True (held at a min/max bound)"},
		{Name: "metrics", Type: FieldString, Description: `per-metric current vs target, one line each: "<name>: cur=<v> target=<v>"`},
		{Name: "conditionReason", Type: FieldString, Description: `reason/message when scaling is limited or unable to scale, "" otherwise`},
	}
}

func (checkHPA) Run(ctx context.Context, deps Deps, params map[string]string) (Outputs, error) {
	h, err := deps.Kube.AutoscalingV2().HorizontalPodAutoscalers(params["namespace"]).
		Get(ctx, params["name"], metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		// Existence is itself a finding — a missing HPA means no autoscaling.
		return Outputs{
			"exists": false, "scaleTarget": "", "minReplicas": int64(0), "maxReplicas": int64(0),
			"currentReplicas": int64(0), "desiredReplicas": int64(0), "atMax": false,
			"ableToScale": false, "scalingLimited": false, "metrics": "", "conditionReason": "",
		}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get hpa %s/%s: %w", params["namespace"], params["name"], err)
	}

	minR := int64(1)
	if h.Spec.MinReplicas != nil {
		minR = int64(*h.Spec.MinReplicas)
	}
	maxR := int64(h.Spec.MaxReplicas)
	current := int64(h.Status.CurrentReplicas)

	out := Outputs{
		"exists":          true,
		"scaleTarget":     h.Spec.ScaleTargetRef.Kind + "/" + h.Spec.ScaleTargetRef.Name,
		"minReplicas":     minR,
		"maxReplicas":     maxR,
		"currentReplicas": current,
		"desiredReplicas": int64(h.Status.DesiredReplicas),
		"atMax":           maxR > 0 && current >= maxR,
		"ableToScale":     true, // absence of the condition is treated as not-blocked
		"scalingLimited":  false,
		"metrics":         hpaMetrics(h),
		"conditionReason": "",
	}

	var reason string
	for _, c := range h.Status.Conditions {
		isTrue := c.Status == "True"
		switch c.Type {
		case autoscalingv2.AbleToScale:
			out["ableToScale"] = isTrue
			if !isTrue && reason == "" {
				reason = condText(c)
			}
		case autoscalingv2.ScalingLimited:
			out["scalingLimited"] = isTrue
			if isTrue && reason == "" {
				reason = condText(c)
			}
		}
	}
	out["conditionReason"] = reason
	return out, nil
}

func condText(c autoscalingv2.HorizontalPodAutoscalerCondition) string {
	if c.Message != "" {
		return c.Reason + ": " + c.Message
	}
	return c.Reason
}

// hpaMetrics renders each spec metric's target alongside its observed current
// value (matched by metric identity), one line per metric.
func hpaMetrics(h *autoscalingv2.HorizontalPodAutoscaler) string {
	current := map[string]string{}
	for _, s := range h.Status.CurrentMetrics {
		k, v := metricStatusKV(s)
		if k != "" {
			current[k] = v
		}
	}
	var b strings.Builder
	for _, m := range h.Spec.Metrics {
		key, label, target := metricSpecKV(m)
		cur, ok := current[key]
		if !ok {
			cur = "-"
		}
		fmt.Fprintf(&b, "%s: cur=%s target=%s\n", label, cur, target)
	}
	return strings.TrimRight(b.String(), "\n")
}

func metricSpecKV(m autoscalingv2.MetricSpec) (key, label, target string) {
	switch m.Type {
	case autoscalingv2.ResourceMetricSourceType:
		name := string(m.Resource.Name)
		return "resource/" + name, name, targetText(m.Resource.Target)
	case autoscalingv2.ContainerResourceMetricSourceType:
		name := string(m.ContainerResource.Name)
		return "containerResource/" + m.ContainerResource.Container + "/" + name,
			m.ContainerResource.Container + "/" + name, targetText(m.ContainerResource.Target)
	case autoscalingv2.PodsMetricSourceType:
		return "pods/" + m.Pods.Metric.Name, m.Pods.Metric.Name, targetText(m.Pods.Target)
	case autoscalingv2.ObjectMetricSourceType:
		return "object/" + m.Object.Metric.Name, m.Object.Metric.Name, targetText(m.Object.Target)
	case autoscalingv2.ExternalMetricSourceType:
		return "external/" + m.External.Metric.Name, m.External.Metric.Name, targetText(m.External.Target)
	}
	return "", string(m.Type), "-"
}

func metricStatusKV(s autoscalingv2.MetricStatus) (key, value string) {
	switch s.Type {
	case autoscalingv2.ResourceMetricSourceType:
		return "resource/" + string(s.Resource.Name), currentText(s.Resource.Current)
	case autoscalingv2.ContainerResourceMetricSourceType:
		return "containerResource/" + s.ContainerResource.Container + "/" + string(s.ContainerResource.Name),
			currentText(s.ContainerResource.Current)
	case autoscalingv2.PodsMetricSourceType:
		return "pods/" + s.Pods.Metric.Name, currentText(s.Pods.Current)
	case autoscalingv2.ObjectMetricSourceType:
		return "object/" + s.Object.Metric.Name, currentText(s.Object.Current)
	case autoscalingv2.ExternalMetricSourceType:
		return "external/" + s.External.Metric.Name, currentText(s.External.Current)
	}
	return "", ""
}

func targetText(t autoscalingv2.MetricTarget) string {
	switch {
	case t.AverageUtilization != nil:
		return fmt.Sprintf("%d%%", *t.AverageUtilization)
	case t.AverageValue != nil:
		return t.AverageValue.String()
	case t.Value != nil:
		return t.Value.String()
	}
	return "-"
}

func currentText(c autoscalingv2.MetricValueStatus) string {
	switch {
	case c.AverageUtilization != nil:
		return fmt.Sprintf("%d%%", *c.AverageUtilization)
	case c.AverageValue != nil:
		return c.AverageValue.String()
	case c.Value != nil:
		return c.Value.String()
	}
	return "-"
}

func init() {
	builtinFns = append(builtinFns, func(r *Registry) { r.Register(checkHPA{}) })
}
