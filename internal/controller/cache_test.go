package controller

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/zufardhiyaulhaq/kato/api/v1alpha1"
)

func TestUseCaseCache(t *testing.T) {
	c := NewUseCaseCache()
	uc := &v1alpha1.UseCase{ObjectMeta: metav1.ObjectMeta{Name: "pod-crashloop"}}

	if _, ok := c.GetUseCase("pod-crashloop"); ok {
		t.Fatal("empty cache returned a use case")
	}
	c.Set(uc, true)
	got, ok := c.GetUseCase("pod-crashloop")
	if !ok || got.Name != "pod-crashloop" {
		t.Fatalf("Get = %v, %v", got, ok)
	}
	if !c.IsReady("pod-crashloop") {
		t.Error("expected ready")
	}
	if len(c.ListUseCases()) != 1 {
		t.Error("list should have 1")
	}
	c.Set(uc, false)
	if c.IsReady("pod-crashloop") {
		t.Error("expected not ready after update")
	}
	c.Delete("pod-crashloop")
	if _, ok := c.GetUseCase("pod-crashloop"); ok {
		t.Error("expected deleted")
	}
}
