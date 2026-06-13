package methods

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestCheckPVCBound(t *testing.T) {
	sc := "gp3"
	mode := corev1.PersistentVolumeFilesystem
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "data-db-0", Namespace: "data"},
		Spec: corev1.PersistentVolumeClaimSpec{
			StorageClassName: &sc,
			VolumeName:       "pv-123",
			VolumeMode:       &mode,
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("10Gi")},
			},
		},
		Status: corev1.PersistentVolumeClaimStatus{
			Phase:    corev1.ClaimBound,
			Capacity: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("10Gi")},
		},
	}
	client := fake.NewSimpleClientset(pvc)
	m, ok := Builtin().Get("check_pvc")
	if !ok {
		t.Fatal("check_pvc not registered")
	}
	out, err := m.Run(context.Background(), Deps{Kube: client},
		map[string]string{"namespace": "data", "name": "data-db-0"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	checks := map[string]any{
		"exists": true, "phase": "Bound", "storageClass": "gp3",
		"requestedStorage": "10Gi", "capacity": "10Gi", "volumeName": "pv-123",
		"accessModes": "ReadWriteOnce", "volumeMode": "Filesystem",
	}
	for f, want := range checks {
		if out[f] != want {
			t.Errorf("%s = %v, want %v", f, out[f], want)
		}
	}
}

func TestCheckPVCMissing(t *testing.T) {
	client := fake.NewSimpleClientset()
	m, _ := Builtin().Get("check_pvc")
	out, err := m.Run(context.Background(), Deps{Kube: client},
		map[string]string{"namespace": "data", "name": "nope"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out["exists"] != false || out["phase"] != "" || out["volumeName"] != "" {
		t.Errorf("missing PVC should default-empty, got %v", out)
	}
}
