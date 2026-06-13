package methods

import (
	"context"
	"fmt"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"
)

type describeNode struct{}

func (describeNode) Name() string        { return "describe_node" }
func (describeNode) Description() string { return "Node capacity, allocatable, taints, manifest" }

func (describeNode) Params() []Param {
	return []Param{{Name: "name", Required: true, Description: "Node name"}}
}

func (describeNode) OutputFields() []OutputField {
	return []OutputField{
		{Name: "taints", Type: FieldString, Description: `rendered "key=value:Effect" list, "" if none`},
		{Name: "allocatableCPU", Type: FieldString, Description: "allocatable CPU quantity"},
		{Name: "allocatableMemory", Type: FieldString, Description: "allocatable memory quantity"},
		{Name: "manifest", Type: FieldString, Description: "sanitized YAML manifest"},
		{Name: "kubeletVersion", Type: FieldString, Description: "status.nodeInfo.kubeletVersion"},
		{Name: "osImage", Type: FieldString, Description: "status.nodeInfo.osImage"},
		{Name: "kernelVersion", Type: FieldString, Description: "status.nodeInfo.kernelVersion"},
		{Name: "containerRuntime", Type: FieldString, Description: "status.nodeInfo.containerRuntimeVersion"},
		{Name: "capacityPods", Type: FieldString, Description: "status.capacity.pods (scheduling ceiling)"},
		{Name: "unschedulable", Type: FieldBool, Description: "spec.unschedulable (cordoned)"},
		{Name: "conditions", Type: FieldString, Description: "node conditions as Type=Status (Reason)"},
	}
}

func (describeNode) Run(ctx context.Context, deps Deps, params map[string]string) (Outputs, error) {
	node, err := deps.Kube.CoreV1().Nodes().Get(ctx, params["name"], metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("get node %s: %w", params["name"], err)
	}
	taints := make([]string, 0, len(node.Spec.Taints))
	for _, t := range node.Spec.Taints {
		taints = append(taints, fmt.Sprintf("%s=%s:%s", t.Key, t.Value, t.Effect))
	}
	n := node.DeepCopy()
	sanitizeObjectMeta(&n.ObjectMeta)
	n.Status.Images = nil // image list is large noise
	y, err := yaml.Marshal(n)
	if err != nil {
		return nil, fmt.Errorf("marshal node: %w", err)
	}
	return Outputs{
		"taints":            strings.Join(taints, ", "),
		"allocatableCPU":    n.Status.Allocatable.Cpu().String(),
		"allocatableMemory": n.Status.Allocatable.Memory().String(),
		"manifest":          Truncate(string(y), defaultLogBytes),
		"kubeletVersion":    node.Status.NodeInfo.KubeletVersion,
		"osImage":           node.Status.NodeInfo.OSImage,
		"kernelVersion":     node.Status.NodeInfo.KernelVersion,
		"containerRuntime":  node.Status.NodeInfo.ContainerRuntimeVersion,
		"capacityPods":      node.Status.Capacity.Pods().String(),
		"unschedulable":     node.Spec.Unschedulable,
		"conditions":        renderNodeConditions(node.Status.Conditions),
	}, nil
}

func init() {
	builtinFns = append(builtinFns, func(r *Registry) {
		r.Register(describeNode{})
	})
}
