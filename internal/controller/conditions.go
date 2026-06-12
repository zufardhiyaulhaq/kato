package controller

import (
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func joinProblems(p []string) string {
	msg := strings.Join(p, "; ")
	if len(msg) > 1024 { // keep within condition message limits
		return msg[:1021] + "..."
	}
	return msg
}

// setCondition upserts c by Type, returning true if anything changed.
func setCondition(conds *[]metav1.Condition, c metav1.Condition) bool {
	for i, existing := range *conds {
		if existing.Type == c.Type {
			if existing.Status == c.Status && existing.Reason == c.Reason && existing.Message == c.Message {
				return false
			}
			if existing.Status == c.Status {
				c.LastTransitionTime = existing.LastTransitionTime
			}
			(*conds)[i] = c
			return true
		}
	}
	*conds = append(*conds, c)
	return true
}
