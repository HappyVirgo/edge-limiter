package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/HappyVirgo/edge-limiter/limiter"
)

func noEnv(string) string { return "" }

func TestEmptyDocumentUsesDefaults(t *testing.T) {
	c, err := Parse(nil, noEnv)
	if err != nil {
		t.Fatal(err)
	}
	if c.Backend.Type != "memory" || c.Default.Rate != 100 || c.Backend.FailureMode != limiter.FailLocal {
		t.Fatalf("unexpected defaults: %+v", c)
	}
}

func TestResolveFirstMatchWins(t *testing.T) {
	c, err := Parse([]byte(`
rules:
  - {name: acme-login, route: /login, tenant: acme, algorithm: sliding_window_log, rate: 2, period: 1m}
  - {name: login, route: /login, algorithm: sliding_window_log, rate: 5, period: 1m}
  - {name: acme, tenant: acme, algorithm: token_bucket, rate: 50, period: 1s, burst: 10}
`), noEnv)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct{ route, tenant, want string }{
		{"/login/password", "acme", "acme-login"},
		{"/login", "globex", "login"},
		{"/orders", "acme", "acme"},
		{"/orders", "", "default"},
	}
	for _, tc := range cases {
		if got := c.Resolve(tc.route, tc.tenant); got.Name != tc.want {
			t.Errorf("Resolve(%q,%q) = %s, want %s", tc.route, tc.tenant, got.Name, tc.want)
		}
	}
	if r := c.Resolve("/x", "acme"); r.Burst != 10 || r.Period != time.Second {
		t.Fatalf("inline limit not decoded: %+v", r)
	}
}

func TestEnvOverrides(t *testing.T) {
	env := map[string]string{
		"EDGE_LIMITER_BACKEND":        "redis",
		"EDGE_LIMITER_REDIS_ADDR":     "cache.internal:6380",
		"EDGE_LIMITER_REDIS_PASSWORD": "from-env",
		"EDGE_LIMITER_REDIS_TLS":      "true",
		"EDGE_LIMITER_FAILURE_MODE":   "closed",
	}
	c, err := Parse(nil, func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	r := c.Backend.Redis
	if c.Backend.Type != "redis" || r.Addr != "cache.internal:6380" || r.Password != "from-env" || !r.TLS ||
		c.Backend.FailureMode != limiter.FailClosed {
		t.Fatalf("overrides not applied: %+v", c.Backend)
	}
}

func TestValidationErrors(t *testing.T) {
	cases := map[string]string{
		"unknown field":   "listn: :80",
		"bad backend":     "backend: {type: etcd}",
		"bad mode":        "backend: {failure_mode: maybe}",
		"bad algorithm":   "default: {algorithm: leaky, rate: 1, period: 1s}",
		"duplicate rule":  "rules: [{name: a, algorithm: token_bucket, rate: 1, period: 1s}, {name: a, algorithm: token_bucket, rate: 1, period: 1s}]",
		"zero rate":       "rules: [{name: a, algorithm: token_bucket, rate: 0, period: 1s}]",
		"reserved name":   "rules: [{name: default, algorithm: token_bucket, rate: 1, period: 1s}]",
		"password in yml": "backend: {redis: {password: hunter2}}",
	}
	for name, doc := range cases {
		if _, err := Parse([]byte(doc), noEnv); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

// TestShippedConfigsAreValid loads every config shipped in the repo,
// including the one embedded in the Kubernetes ConfigMap.
func TestShippedConfigsAreValid(t *testing.T) {
	files, _ := filepath.Glob("../../configs/*.yaml")
	if len(files) == 0 {
		t.Fatal("no configs found")
	}
	for _, f := range files {
		if _, err := Load(f, noEnv); err != nil {
			t.Errorf("%s: %v", f, err)
		}
	}
	raw, err := os.ReadFile("../../deploy/k8s/configmap.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var cm struct {
		Data map[string]string `yaml:"data"`
	}
	if err := yaml.Unmarshal(raw, &cm); err != nil {
		t.Fatal(err)
	}
	doc, ok := cm.Data["config.yaml"]
	if !ok || !strings.Contains(doc, "backend:") {
		t.Fatal("configmap has no config.yaml")
	}
	if _, err := Parse([]byte(doc), noEnv); err != nil {
		t.Errorf("configmap config.yaml: %v", err)
	}
}

// TestKubernetesManifestsParse is a cheap stand-in for kubectl validation:
// every manifest must be valid YAML with apiVersion, kind and metadata.name.
func TestKubernetesManifestsParse(t *testing.T) {
	files, _ := filepath.Glob("../../deploy/k8s/*.yaml")
	kinds := map[string]bool{}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var m struct {
			APIVersion string `yaml:"apiVersion"`
			Kind       string `yaml:"kind"`
			Metadata   struct {
				Name string `yaml:"name"`
			} `yaml:"metadata"`
		}
		if err := yaml.Unmarshal(raw, &m); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		if m.APIVersion == "" || m.Kind == "" || (m.Kind != "Kustomization" && m.Metadata.Name == "") {
			t.Errorf("%s: missing apiVersion/kind/metadata.name", f)
		}
		kinds[m.Kind] = true
	}
	for _, k := range []string{"Deployment", "Service", "HorizontalPodAutoscaler", "PodDisruptionBudget", "ConfigMap"} {
		if !kinds[k] {
			t.Errorf("missing %s manifest", k)
		}
	}
}
