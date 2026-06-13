package methods

import (
	"context"
	"testing"

	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func i32(v int32) *int32 { return &v }

func TestCheckHPASaturated(t *testing.T) {
	hpa := &autoscalingv2.HorizontalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{Name: "coredns", Namespace: "kube-system"},
		Spec: autoscalingv2.HorizontalPodAutoscalerSpec{
			ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{Kind: "Deployment", Name: "coredns"},
			MinReplicas:    i32(2),
			MaxReplicas:    5,
			Metrics: []autoscalingv2.MetricSpec{{
				Type: autoscalingv2.ResourceMetricSourceType,
				Resource: &autoscalingv2.ResourceMetricSource{
					Name: corev1.ResourceCPU,
					Target: autoscalingv2.MetricTarget{
						Type:               autoscalingv2.UtilizationMetricType,
						AverageUtilization: i32(50),
					},
				},
			}},
		},
		Status: autoscalingv2.HorizontalPodAutoscalerStatus{
			CurrentReplicas: 5,
			DesiredReplicas: 5,
			CurrentMetrics: []autoscalingv2.MetricStatus{{
				Type: autoscalingv2.ResourceMetricSourceType,
				Resource: &autoscalingv2.ResourceMetricStatus{
					Name:    corev1.ResourceCPU,
					Current: autoscalingv2.MetricValueStatus{AverageUtilization: i32(83)},
				},
			}},
			Conditions: []autoscalingv2.HorizontalPodAutoscalerCondition{{
				Type:    autoscalingv2.ScalingLimited,
				Status:  "True",
				Reason:  "TooManyReplicas",
				Message: "the desired replica count is more than the maximum replica count",
			}},
		},
	}
	client := fake.NewSimpleClientset(hpa)
	m, ok := Builtin().Get("check_hpa")
	if !ok {
		t.Fatal("check_hpa not registered")
	}
	out, err := m.Run(context.Background(), Deps{Kube: client},
		map[string]string{"namespace": "kube-system", "name": "coredns"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out["exists"] != true {
		t.Errorf("exists = %v, want true", out["exists"])
	}
	if out["scaleTarget"] != "Deployment/coredns" {
		t.Errorf("scaleTarget = %v", out["scaleTarget"])
	}
	if out["maxReplicas"] != int64(5) || out["currentReplicas"] != int64(5) {
		t.Errorf("replica counts = %v/%v", out["currentReplicas"], out["maxReplicas"])
	}
	if out["atMax"] != true {
		t.Errorf("atMax = %v, want true", out["atMax"])
	}
	if out["scalingLimited"] != true {
		t.Errorf("scalingLimited = %v, want true", out["scalingLimited"])
	}
	if out["ableToScale"] != true { // no AbleToScale=False condition present
		t.Errorf("ableToScale = %v, want true", out["ableToScale"])
	}
	if got := out["metrics"]; got != "cpu: cur=83% target=50%" {
		t.Errorf("metrics = %q", got)
	}
	if got, _ := out["conditionReason"].(string); got == "" {
		t.Errorf("conditionReason should be set, got empty")
	}
}

func TestCheckHPAMissing(t *testing.T) {
	client := fake.NewSimpleClientset()
	m, _ := Builtin().Get("check_hpa")
	out, err := m.Run(context.Background(), Deps{Kube: client},
		map[string]string{"namespace": "kube-system", "name": "coredns"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out["exists"] != false {
		t.Errorf("exists = %v, want false", out["exists"])
	}
	if out["metrics"] != "" || out["scaleTarget"] != "" {
		t.Errorf("expected empty metrics/scaleTarget, got %v / %v", out["metrics"], out["scaleTarget"])
	}
}

func TestHPAResourceAverageValue(t *testing.T) {
	hpa := &autoscalingv2.HorizontalPodAutoscaler{
		Spec: autoscalingv2.HorizontalPodAutoscalerSpec{
			MaxReplicas: 10,
			Metrics: []autoscalingv2.MetricSpec{{
				Type: autoscalingv2.ResourceMetricSourceType,
				Resource: &autoscalingv2.ResourceMetricSource{
					Name: corev1.ResourceMemory,
					Target: func() autoscalingv2.MetricTarget {
						q := resource.MustParse("500Mi")
						return autoscalingv2.MetricTarget{Type: autoscalingv2.AverageValueMetricType, AverageValue: &q}
					}(),
				},
			}},
		},
		Status: autoscalingv2.HorizontalPodAutoscalerStatus{
			CurrentMetrics: []autoscalingv2.MetricStatus{{
				Type: autoscalingv2.ResourceMetricSourceType,
				Resource: &autoscalingv2.ResourceMetricStatus{
					Name: corev1.ResourceMemory,
					Current: func() autoscalingv2.MetricValueStatus {
						q := resource.MustParse("420Mi")
						return autoscalingv2.MetricValueStatus{AverageValue: &q}
					}(),
				},
			}},
		},
	}
	if got := hpaMetrics(hpa); got != "memory: cur=420Mi target=500Mi" {
		t.Errorf("hpaMetrics = %q", got)
	}
}
