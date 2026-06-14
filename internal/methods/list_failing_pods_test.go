package methods

import (
	"context"
	"fmt"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
)

func crashingPod(name, ns string, owner metav1.OwnerReference, restarts int32, waiting string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, OwnerReferences: []metav1.OwnerReference{owner}},
		Status: corev1.PodStatus{
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionFalse}},
			ContainerStatuses: []corev1.ContainerStatus{{
				Name: "c", RestartCount: restarts,
				State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: waiting}},
			}},
		},
	}
}

func dsOwner(name string) metav1.OwnerReference {
	return metav1.OwnerReference{Kind: "DaemonSet", Name: name}
}

// TestListFailingPodsIgnoresRecoveredPod reproduces the terway-eniip-ffnvx case:
// a pod that crashed long ago (high restartCount, a non-zero lastTermination) but
// is now Running and Ready (2/2). Its historical lastState must NOT mark it
// failing — even with includeNotReady=true, since the pod is Ready.
func TestListFailingPodsIgnoresRecoveredPod(t *testing.T) {
	recovered := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "terway-eniip-ffnvx", Namespace: "kube-system",
			OwnerReferences: []metav1.OwnerReference{dsOwner("terway-eniip")}},
		Status: corev1.PodStatus{
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}},
			ContainerStatuses: []corev1.ContainerStatus{
				{Name: "terway", Ready: true, RestartCount: 467,
					State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
					LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
						Reason: "StartError", ExitCode: 128}}},
				{Name: "policy", Ready: true,
					State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}},
			},
		},
	}
	client := fake.NewSimpleClientset(recovered)
	m, _ := Builtin().Get("list_failing_pods")
	out, err := m.Run(context.Background(), Deps{Kube: client}, map[string]string{
		"namespace": "kube-system", "kind": "DaemonSet", "name": "terway-eniip",
		"includeNotReady": "true",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out["count"] != int64(0) || out["anyFailing"] != false {
		t.Errorf("recovered Running+Ready pod flagged as failing: count=%v anyFailing=%v",
			out["count"], out["anyFailing"])
	}
}

// TestListFailingPodsFlagsCurrentlyFailingFromLastState confirms the lastState
// signal still fires when the container is NOT currently ready (e.g. OOMing now).
func TestListFailingPodsFlagsCurrentlyFailingFromLastState(t *testing.T) {
	ooming := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "oom-0", Namespace: "kube-system",
			OwnerReferences: []metav1.OwnerReference{dsOwner("app")}},
		Status: corev1.PodStatus{
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionFalse}},
			ContainerStatuses: []corev1.ContainerStatus{{
				Name: "c", Ready: false, RestartCount: 4,
				State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
				LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
					Reason: "OOMKilled", ExitCode: 137}},
			}},
		},
	}
	client := fake.NewSimpleClientset(ooming)
	m, _ := Builtin().Get("list_failing_pods")
	// includeCrashLoop=false so the only thing that can flag it is the lastState branch.
	out, err := m.Run(context.Background(), Deps{Kube: client}, map[string]string{
		"namespace": "kube-system", "kind": "DaemonSet", "name": "app",
		"includeCrashLoop": "false",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out["count"] != int64(1) {
		t.Errorf("currently not-ready pod with OOM lastState should be failing: count=%v", out["count"])
	}
	pods, _ := out["pods"].([]map[string]any)
	if len(pods) != 1 || pods[0]["reason"] != "OOMKilled" {
		t.Errorf("expected OOMKilled reason, got %v", pods)
	}
}

func TestListFailingPodsDaemonSet(t *testing.T) {
	healthy := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "ok", Namespace: "kube-system", OwnerReferences: []metav1.OwnerReference{dsOwner("nld")}},
		Status: corev1.PodStatus{
			Conditions:        []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}},
			ContainerStatuses: []corev1.ContainerStatus{{Name: "c", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}},
		},
	}
	client := fake.NewSimpleClientset(
		healthy,
		crashingPod("nld-a", "kube-system", dsOwner("nld"), 9, "CrashLoopBackOff"),
		crashingPod("nld-b", "kube-system", dsOwner("nld"), 12, "CrashLoopBackOff"),
		crashingPod("other", "kube-system", dsOwner("different-ds"), 5, "CrashLoopBackOff"), // wrong owner
	)
	m, ok := Builtin().Get("list_failing_pods")
	if !ok {
		t.Fatal("list_failing_pods not registered")
	}
	out, err := m.Run(context.Background(), Deps{Kube: client},
		map[string]string{"namespace": "kube-system", "kind": "DaemonSet", "name": "nld"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out["count"] != int64(2) || out["anyFailing"] != true {
		t.Errorf("count=%v anyFailing=%v", out["count"], out["anyFailing"])
	}
	pods, _ := out["pods"].([]map[string]any)
	if len(pods) != 2 {
		t.Fatalf("pods len = %d, want 2", len(pods))
	}
	// Worst-first: nld-b (12 restarts) before nld-a (9).
	if pods[0]["name"] != "nld-b" || pods[0]["restartCount"] != int64(12) {
		t.Errorf("worst-first wrong: %v", pods)
	}
	if pods[0]["namespace"] != "kube-system" || pods[0]["reason"] != "CrashLoopBackOff" {
		t.Errorf("item fields wrong: %v", pods[0])
	}
}

func TestListFailingPodsDeployment(t *testing.T) {
	// Deployment -> ReplicaSet -> Pod (two hops).
	rs := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{
		Name: "api-7d9", Namespace: "payments",
		OwnerReferences: []metav1.OwnerReference{{Kind: "Deployment", Name: "api"}},
	}}
	rsOwner := metav1.OwnerReference{Kind: "ReplicaSet", Name: "api-7d9"}
	client := fake.NewSimpleClientset(
		rs,
		crashingPod("api-7d9-x", "payments", rsOwner, 3, "CrashLoopBackOff"),
	)
	m, _ := Builtin().Get("list_failing_pods")
	out, err := m.Run(context.Background(), Deps{Kube: client},
		map[string]string{"namespace": "payments", "kind": "Deployment", "name": "api"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out["count"] != int64(1) {
		t.Fatalf("count = %v, want 1", out["count"])
	}
	pods := out["pods"].([]map[string]any)
	if pods[0]["name"] != "api-7d9-x" {
		t.Errorf("pod = %v", pods[0])
	}
}

func TestListFailingPodsCriteriaAndMinRestarts(t *testing.T) {
	imagePull := crashingPod("img", "default", dsOwner("ds"), 0, "ImagePullBackOff")
	crash := crashingPod("crash", "default", dsOwner("ds"), 7, "CrashLoopBackOff")
	client := fake.NewSimpleClientset(imagePull, crash)
	m, _ := Builtin().Get("list_failing_pods")

	// Default criteria: both image-pull and crashloop match.
	out, _ := m.Run(context.Background(), Deps{Kube: client},
		map[string]string{"namespace": "default", "kind": "DaemonSet", "name": "ds"})
	if out["count"] != int64(2) {
		t.Errorf("default count = %v, want 2", out["count"])
	}

	// Disable image-pull: only the crashloop pod remains.
	out, _ = m.Run(context.Background(), Deps{Kube: client},
		map[string]string{"namespace": "default", "kind": "DaemonSet", "name": "ds", "includeImagePull": "false"})
	if out["count"] != int64(1) {
		t.Errorf("no-imagepull count = %v, want 1", out["count"])
	}

	// minRestarts=5 drops the image-pull pod (0 restarts), keeps crash (7).
	out, _ = m.Run(context.Background(), Deps{Kube: client},
		map[string]string{"namespace": "default", "kind": "DaemonSet", "name": "ds", "minRestarts": "5"})
	if out["count"] != int64(1) {
		t.Errorf("minRestarts count = %v, want 1", out["count"])
	}
}

func TestListFailingPodsNoneFailing(t *testing.T) {
	client := fake.NewSimpleClientset()
	m, _ := Builtin().Get("list_failing_pods")
	out, err := m.Run(context.Background(), Deps{Kube: client},
		map[string]string{"namespace": "default", "kind": "DaemonSet", "name": "ds"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out["count"] != int64(0) || out["anyFailing"] != false {
		t.Errorf("expected zero/false, got count=%v any=%v", out["count"], out["anyFailing"])
	}
	if pods := out["pods"].([]map[string]any); len(pods) != 0 {
		t.Errorf("pods should be empty, got %v", pods)
	}
	if out["listTruncated"] != false {
		t.Errorf("expected listTruncated false on empty, got %v", out["listTruncated"])
	}
}

func TestListFailingPodsCapsList(t *testing.T) {
	owner := dsOwner("big")
	objs := make([]runtime.Object, 0, 60)
	for i := 0; i < 60; i++ {
		// Distinct restartCounts so ordering is deterministic and worst-first.
		objs = append(objs, crashingPod(fmt.Sprintf("p-%02d", i), "default", owner, int32(i), "CrashLoopBackOff"))
	}
	client := fake.NewSimpleClientset(objs...)
	m, _ := Builtin().Get("list_failing_pods")

	out, err := m.Run(context.Background(), Deps{Kube: client},
		map[string]string{"namespace": "default", "kind": "DaemonSet", "name": "big"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// count reflects the TRUE matched total, not the capped list.
	if out["count"] != int64(60) {
		t.Errorf("count = %v, want 60 (true total)", out["count"])
	}
	if out["listTruncated"] != true {
		t.Errorf("listTruncated = %v, want true", out["listTruncated"])
	}
	pods := out["pods"].([]map[string]any)
	if len(pods) != 50 {
		t.Fatalf("pods len = %d, want 50 (capped)", len(pods))
	}
	// Worst-first survives the cap: highest restartCount (59) is first.
	if pods[0]["restartCount"] != int64(59) {
		t.Errorf("first kept pod restartCount = %v, want 59", pods[0]["restartCount"])
	}
}

func TestListFailingPodsUnderCapNotTruncated(t *testing.T) {
	client := fake.NewSimpleClientset(
		crashingPod("a", "default", dsOwner("ds"), 2, "CrashLoopBackOff"),
		crashingPod("b", "default", dsOwner("ds"), 1, "CrashLoopBackOff"),
	)
	m, _ := Builtin().Get("list_failing_pods")
	out, _ := m.Run(context.Background(), Deps{Kube: client},
		map[string]string{"namespace": "default", "kind": "DaemonSet", "name": "ds"})
	if out["listTruncated"] != false {
		t.Errorf("listTruncated = %v, want false", out["listTruncated"])
	}
	if len(out["pods"].([]map[string]any)) != 2 {
		t.Errorf("pods len = %d, want 2", len(out["pods"].([]map[string]any)))
	}
}

func TestListFailingPodsDeclaresListTruncated(t *testing.T) {
	m, _ := Builtin().Get("list_failing_pods")
	found := false
	for _, f := range m.OutputFields() {
		if f.Name == "listTruncated" {
			if f.Type != FieldBool {
				t.Errorf("listTruncated type = %v, want bool", f.Type)
			}
			found = true
		}
	}
	if !found {
		t.Error("listTruncated not declared in OutputFields")
	}
}

func TestListFailingPodsMaxListItemsParam(t *testing.T) {
	owner := dsOwner("big")
	objs := make([]runtime.Object, 0, 60)
	for i := 0; i < 60; i++ {
		objs = append(objs, crashingPod(fmt.Sprintf("p-%02d", i), "default", owner, int32(i), "CrashLoopBackOff"))
	}
	client := fake.NewSimpleClientset(objs...)
	m, _ := Builtin().Get("list_failing_pods")

	out, err := m.Run(context.Background(), Deps{Kube: client}, map[string]string{
		"namespace": "default", "kind": "DaemonSet", "name": "big", "maxListItems": "5"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out["count"] != int64(60) || out["listTruncated"] != true {
		t.Errorf("count=%v listTruncated=%v, want 60,true", out["count"], out["listTruncated"])
	}
	if len(out["pods"].([]map[string]any)) != 5 {
		t.Errorf("pods len = %d, want 5", len(out["pods"].([]map[string]any)))
	}

	out, err = m.Run(context.Background(), Deps{Kube: client}, map[string]string{
		"namespace": "default", "kind": "DaemonSet", "name": "big", "maxListItems": "0"})
	if err != nil {
		t.Fatalf("Run (unlimited): %v", err)
	}
	if out["listTruncated"] != false || len(out["pods"].([]map[string]any)) != 60 {
		t.Errorf("unlimited: listTruncated=%v len=%d, want false,60", out["listTruncated"], len(out["pods"].([]map[string]any)))
	}

	if _, err := m.Run(context.Background(), Deps{Kube: client}, map[string]string{
		"namespace": "default", "kind": "DaemonSet", "name": "big", "maxListItems": "abc"}); err == nil {
		t.Error("expected error for non-integer maxListItems")
	}
}

func TestListFailingPodsDeclaresListOutput(t *testing.T) {
	m, _ := Builtin().Get("list_failing_pods")
	lists := ListOutputsOf(m)
	if len(lists) != 1 || lists[0].Name != "pods" {
		t.Fatalf("list outputs = %v", lists)
	}
	fields := map[string]FieldType{}
	for _, f := range lists[0].ItemFields {
		fields[f.Name] = f.Type
	}
	if fields["namespace"] != FieldString || fields["name"] != FieldString ||
		fields["reason"] != FieldString || fields["restartCount"] != FieldInt {
		t.Errorf("item fields wrong: %v", fields)
	}
}
