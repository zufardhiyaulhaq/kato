package v1alpha1

import (
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
)

func TestSchemeRegistersAllKinds(t *testing.T) {
	s := runtime.NewScheme()
	if err := AddToScheme(s); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	for _, obj := range []runtime.Object{
		&UseCase{}, &UseCaseList{},
		&ModelConfig{}, &ModelConfigList{},
		&Run{}, &RunList{},
	} {
		if _, _, err := s.ObjectKinds(obj); err != nil {
			t.Errorf("kind not registered: %T: %v", obj, err)
		}
	}
}
