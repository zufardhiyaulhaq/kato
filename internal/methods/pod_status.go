package methods

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type checkPodStatus struct{}

func (checkPodStatus) Name() string        { return "check_pod_status" }
func (checkPodStatus) Description() string { return "Pod phase, readiness, restarts, last termination" }

func (checkPodStatus) Params() []Param {
	return []Param{
		{Name: "namespace", Required: true, Description: "Pod namespace"},
		{Name: "name", Required: true, Description: "Pod name"},
	}
}

func (checkPodStatus) OutputFields() []OutputField {
	return []OutputField{
		{Name: "phase", Type: FieldString, Description: "Pending|Running|Succeeded|Failed|Unknown"},
		{Name: "ready", Type: FieldBool, Description: "Ready condition is True"},
		{Name: "restartCount", Type: FieldInt, Description: "max restartCount across containers, 0 if none"},
		{Name: "nodeName", Type: FieldString, Description: `scheduled node, "" if unscheduled`},
		{Name: "waitingReason", Type: FieldString, Description: `e.g. CrashLoopBackOff, "" if none`},
		{Name: "waitingMessage", Type: FieldString, Description: `waiting message, "" if none`},
		{Name: "lastTerminationReason", Type: FieldString, Description: `e.g. OOMKilled, "" if none`},
		{Name: "lastTerminationExitCode", Type: FieldInt, Description: "-1 if no prior termination"},
		{Name: "qosClass", Type: FieldString, Description: "Guaranteed|Burstable|BestEffort"},
	}
}

func (checkPodStatus) Run(ctx context.Context, deps Deps, params map[string]string) (Outputs, error) {
	pod, err := deps.Kube.CoreV1().Pods(params["namespace"]).Get(ctx, params["name"], metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("get pod %s/%s: %w", params["namespace"], params["name"], err)
	}

	out := Outputs{
		"phase": string(pod.Status.Phase), "ready": false,
		"restartCount": int64(0), "nodeName": pod.Spec.NodeName,
		"waitingReason": "", "waitingMessage": "",
		"lastTerminationReason": "", "lastTerminationExitCode": int64(-1),
		"qosClass": string(pod.Status.QOSClass),
	}
	for _, c := range pod.Status.Conditions {
		if c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue {
			out["ready"] = true
		}
	}
	for _, cs := range pod.Status.ContainerStatuses {
		if int64(cs.RestartCount) > out["restartCount"].(int64) {
			out["restartCount"] = int64(cs.RestartCount)
		}
		if w := cs.State.Waiting; w != nil && out["waitingReason"] == "" {
			out["waitingReason"], out["waitingMessage"] = w.Reason, w.Message
		}
		if t := cs.LastTerminationState.Terminated; t != nil && out["lastTerminationReason"] == "" {
			out["lastTerminationReason"] = t.Reason
			out["lastTerminationExitCode"] = int64(t.ExitCode)
		}
	}
	return out, nil
}

func init() { builtinFns = append(builtinFns, func(r *Registry) { r.Register(checkPodStatus{}) }) }
