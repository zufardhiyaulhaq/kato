package methods

import (
	"context"
	"fmt"

	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type checkPDB struct{}

func (checkPDB) Name() string { return "check_pdb" }
func (checkPDB) Description() string {
	return "PodDisruptionBudget state — does it currently permit voluntary disruption (evictions/drains)?"
}

func (checkPDB) Params() []Param {
	return []Param{
		{Name: "namespace", Required: true, Description: "PDB namespace"},
		{Name: "name", Required: true, Description: "PDB name"},
	}
}

func (checkPDB) OutputFields() []OutputField {
	return []OutputField{
		{Name: "exists", Type: FieldBool, Description: "PDB exists"},
		{Name: "minAvailable", Type: FieldString, Description: `spec.minAvailable (int-or-percent, e.g. "2", "50%"); "" if unset`},
		{Name: "maxUnavailable", Type: FieldString, Description: `spec.maxUnavailable (int-or-percent); "" if unset`},
		{Name: "selector", Type: FieldString, Description: `spec.selector matchLabels as "k=v, k=v"; "" if none`},
		{Name: "expectedPods", Type: FieldInt, Description: "status.expectedPods"},
		{Name: "currentHealthy", Type: FieldInt, Description: "status.currentHealthy"},
		{Name: "desiredHealthy", Type: FieldInt, Description: "status.desiredHealthy"},
		{Name: "disruptionsAllowed", Type: FieldInt, Description: "status.disruptionsAllowed"},
		{Name: "blocked", Type: FieldBool, Description: "disruptionsAllowed == 0 — no voluntary disruption (eviction/drain) possible right now; a PDB gates evictions, not rolling updates"},
		{Name: "conditionReason", Type: FieldString, Description: `reason of the DisruptionAllowed condition when False (e.g. "InsufficientPods"), "" otherwise`},
	}
}

func (checkPDB) Run(ctx context.Context, deps Deps, params map[string]string) (Outputs, error) {
	pdb, err := deps.Kube.PolicyV1().PodDisruptionBudgets(params["namespace"]).
		Get(ctx, params["name"], metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		// Existence is itself a finding — no PDB means drains evict freely.
		return Outputs{
			"exists": false, "minAvailable": "", "maxUnavailable": "", "selector": "",
			"expectedPods": int64(0), "currentHealthy": int64(0), "desiredHealthy": int64(0),
			"disruptionsAllowed": int64(0), "blocked": false, "conditionReason": "",
		}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get pdb %s/%s: %w", params["namespace"], params["name"], err)
	}

	minA, maxU := "", ""
	if pdb.Spec.MinAvailable != nil {
		minA = pdb.Spec.MinAvailable.String()
	}
	if pdb.Spec.MaxUnavailable != nil {
		maxU = pdb.Spec.MaxUnavailable.String()
	}
	selector := ""
	if pdb.Spec.Selector != nil {
		selector = renderKVMap(pdb.Spec.Selector.MatchLabels)
	}
	reason := ""
	for _, c := range pdb.Status.Conditions {
		if c.Type == policyv1.DisruptionAllowedCondition && c.Status == metav1.ConditionFalse {
			reason = c.Reason
		}
	}
	return Outputs{
		"exists":             true,
		"minAvailable":       minA,
		"maxUnavailable":     maxU,
		"selector":           selector,
		"expectedPods":       int64(pdb.Status.ExpectedPods),
		"currentHealthy":     int64(pdb.Status.CurrentHealthy),
		"desiredHealthy":     int64(pdb.Status.DesiredHealthy),
		"disruptionsAllowed": int64(pdb.Status.DisruptionsAllowed),
		"blocked":            pdb.Status.DisruptionsAllowed == 0,
		"conditionReason":    reason,
	}, nil
}

func init() {
	builtinFns = append(builtinFns, func(r *Registry) { r.Register(checkPDB{}) })
}
