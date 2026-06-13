package methods

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type checkPVC struct{}

func (checkPVC) Name() string        { return "check_pvc" }
func (checkPVC) Description() string { return "PersistentVolumeClaim binding status" }

func (checkPVC) Params() []Param {
	return []Param{
		{Name: "namespace", Required: true, Description: "PVC namespace"},
		{Name: "name", Required: true, Description: "PVC name"},
	}
}

func (checkPVC) OutputFields() []OutputField {
	return []OutputField{
		{Name: "exists", Type: FieldBool, Description: "PVC exists"},
		{Name: "phase", Type: FieldString, Description: `Pending|Bound|Lost, "" if not exists`},
		{Name: "storageClass", Type: FieldString, Description: `spec.storageClassName, "" if nil/default`},
		{Name: "requestedStorage", Type: FieldString, Description: "spec.resources.requests.storage"},
		{Name: "capacity", Type: FieldString, Description: `status.capacity.storage (actual), "" if unbound`},
		{Name: "volumeName", Type: FieldString, Description: `bound PV name, "" if unbound`},
		{Name: "accessModes", Type: FieldString, Description: "comma-separated access modes"},
		{Name: "volumeMode", Type: FieldString, Description: "Filesystem|Block"},
	}
}

func (checkPVC) Run(ctx context.Context, deps Deps, params map[string]string) (Outputs, error) {
	out := Outputs{
		"exists": false, "phase": "", "storageClass": "", "requestedStorage": "",
		"capacity": "", "volumeName": "", "accessModes": "", "volumeMode": "",
	}
	pvc, err := deps.Kube.CoreV1().PersistentVolumeClaims(params["namespace"]).Get(ctx, params["name"], metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return out, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get pvc %s/%s: %w", params["namespace"], params["name"], err)
	}
	out["exists"] = true
	out["phase"] = string(pvc.Status.Phase)
	if pvc.Spec.StorageClassName != nil {
		out["storageClass"] = *pvc.Spec.StorageClassName
	}
	if q, ok := pvc.Spec.Resources.Requests[corev1.ResourceStorage]; ok {
		out["requestedStorage"] = q.String()
	}
	if q, ok := pvc.Status.Capacity[corev1.ResourceStorage]; ok {
		out["capacity"] = q.String()
	}
	out["volumeName"] = pvc.Spec.VolumeName
	out["accessModes"] = renderAccessModes(pvc.Spec.AccessModes)
	if pvc.Spec.VolumeMode != nil {
		out["volumeMode"] = string(*pvc.Spec.VolumeMode)
	}
	return out, nil
}

// renderAccessModes renders PVC access modes comma-joined; "" if none.
func renderAccessModes(modes []corev1.PersistentVolumeAccessMode) string {
	if len(modes) == 0 {
		return ""
	}
	parts := make([]string, len(modes))
	for i, m := range modes {
		parts[i] = string(m)
	}
	return strings.Join(parts, ",")
}

func init() { builtinFns = append(builtinFns, func(r *Registry) { r.Register(checkPVC{}) }) }
