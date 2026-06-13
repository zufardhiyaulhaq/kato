package methods

import (
	"context"
	"fmt"
	"sort"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type checkConfigMap struct{}

func (checkConfigMap) Name() string { return "check_configmap" }
func (checkConfigMap) Description() string {
	return "ConfigMap existence, keys, and rendered data (values NOT redacted)"
}

func (checkConfigMap) Params() []Param {
	return []Param{
		{Name: "namespace", Required: true, Description: "ConfigMap namespace"},
		{Name: "name", Required: true, Description: "ConfigMap name"},
		{Name: "keys", Description: "comma-separated key names to render in data (default: all keys); the keys output still lists every key"},
	}
}

func (checkConfigMap) OutputFields() []OutputField {
	return []OutputField{
		{Name: "exists", Type: FieldBool, Description: "ConfigMap exists"},
		{Name: "keyCount", Type: FieldInt, Description: "number of keys (data + binaryData)"},
		{Name: "keys", Type: FieldString, Description: "comma-separated key names, sorted"},
		{Name: "data", Type: FieldString, Description: "rendered key/value blocks (values NOT redacted), truncated if large"},
	}
}

func (checkConfigMap) Run(ctx context.Context, deps Deps, params map[string]string) (Outputs, error) {
	cm, err := deps.Kube.CoreV1().ConfigMaps(params["namespace"]).Get(ctx, params["name"], metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		// Existence is itself a finding — report it rather than failing the step.
		return Outputs{"exists": false, "keyCount": int64(0), "keys": "", "data": ""}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get configmap %s/%s: %w", params["namespace"], params["name"], err)
	}

	keys := make([]string, 0, len(cm.Data)+len(cm.BinaryData))
	for k := range cm.Data {
		keys = append(keys, k)
	}
	for k := range cm.BinaryData {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	// Optional key allow-list: restricts what is rendered into `data` (the token-
	// heavy field). `keys`/`keyCount` outputs still describe every key present.
	var want map[string]bool
	if v := strings.TrimSpace(params["keys"]); v != "" {
		want = map[string]bool{}
		for _, k := range strings.Split(v, ",") {
			if k = strings.TrimSpace(k); k != "" {
				want[k] = true
			}
		}
	}

	var b strings.Builder
	for _, k := range keys {
		if want != nil && !want[k] {
			continue
		}
		if v, ok := cm.Data[k]; ok {
			fmt.Fprintf(&b, "%s:\n%s\n\n", k, v)
		} else {
			fmt.Fprintf(&b, "%s: <%d binary bytes>\n\n", k, len(cm.BinaryData[k]))
		}
	}

	return Outputs{
		"exists":   true,
		"keyCount": int64(len(keys)),
		"keys":     strings.Join(keys, ","),
		"data":     Truncate(strings.TrimRight(b.String(), "\n"), defaultLogBytes),
	}, nil
}

func init() {
	builtinFns = append(builtinFns, func(r *Registry) { r.Register(checkConfigMap{}) })
}
