// Package deploy holds Tropis's installation manifests. Its only Go code is
// this test, which enforces the read-only guarantee on the shipped RBAC.
package deploy

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
	"sigs.k8s.io/yaml"
)

// readVerbs are the only verbs Tropis may hold on anything but its own output.
var readVerbs = map[string]bool{"get": true, "list": true, "watch": true}

// ownResources are the only resources Tropis may write.
var ownResources = map[string]bool{
	"nodehealthreports":        true,
	"nodehealthreports/status": true,
}

var (
	// A line holding only a template directive, such as {{- if ... }}.
	directiveLine = regexp.MustCompile(`(?m)^\s*\{\{-?[^}]*-?\}\}\s*$`)
	// Any remaining inline directive.
	inlineDirective = regexp.MustCompile(`\{\{-?[^}]*-?\}\}`)
)

// untemplate turns a Helm template into parseable YAML. RBAC rules in the
// chart are deliberately literal, so replacing directives with a placeholder
// loses nothing this test inspects.
func untemplate(src []byte) []byte {
	out := directiveLine.ReplaceAll(src, nil)
	return inlineDirective.ReplaceAll(out, []byte("placeholder"))
}

type rbacDoc struct {
	Kind  string              `json:"kind"`
	Rules []rbacv1.PolicyRule `json:"rules"`
}

// The T5 acceptance criterion: shipped RBAC contains no write verbs, other
// than on Tropis's own NodeHealthReport resources.
func TestRBACIsReadOnly(t *testing.T) {
	var roles int

	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if ext := filepath.Ext(path); ext != ".yaml" && ext != ".yml" {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for i, doc := range bytes.Split(untemplate(src), []byte("\n---")) {
			var r rbacDoc
			if err := yaml.Unmarshal(doc, &r); err != nil {
				t.Errorf("%s document %d: %v", path, i, err)
				continue
			}
			if r.Kind != "Role" && r.Kind != "ClusterRole" {
				continue
			}
			roles++
			for _, v := range violations(path, r.Rules) {
				t.Error(v)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if roles == 0 {
		t.Fatal("found no Role or ClusterRole to check; the test is not looking where the manifests are")
	}
}

// violations returns every way rules exceed what Tropis may hold.
func violations(path string, rules []rbacv1.PolicyRule) []string {
	var out []string
	for _, rule := range rules {
		for _, verb := range rule.Verbs {
			if verb == "*" {
				out = append(out, fmt.Sprintf("%s: wildcard verb on %v", path, rule.Resources))
				continue
			}
			if readVerbs[verb] {
				continue
			}
			for _, res := range rule.Resources {
				if !ownResources[res] || !contains(rule.APIGroups, "tropis.io") {
					out = append(out, fmt.Sprintf("%s: write verb %q on %v/%s; Tropis is read-only outside its own reports",
						path, verb, rule.APIGroups, res))
				}
			}
		}
		for _, res := range rule.Resources {
			if res == "*" {
				out = append(out, fmt.Sprintf("%s: wildcard resource in %v", path, rule.APIGroups))
			}
			if res == "secrets" || res == "configmaps" {
				out = append(out, fmt.Sprintf("%s: access to %s is not needed for diagnosis", path, res))
			}
		}
	}
	return out
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// The check must actually catch a violation.
func TestRBACCheckDetectsWrites(t *testing.T) {
	for _, bad := range []rbacv1.PolicyRule{
		{APIGroups: []string{""}, Resources: []string{"pods"}, Verbs: []string{"delete"}},
		{APIGroups: []string{""}, Resources: []string{"nodes"}, Verbs: []string{"patch"}},
		{APIGroups: []string{"policy"}, Resources: []string{"pods/eviction"}, Verbs: []string{"create"}},
		{APIGroups: []string{""}, Resources: []string{"pods"}, Verbs: []string{"*"}},
		{APIGroups: []string{"other.io"}, Resources: []string{"nodehealthreports"}, Verbs: []string{"create"}},
	} {
		if len(violations("synthetic", []rbacv1.PolicyRule{bad})) == 0 {
			t.Errorf("rule %+v should have been rejected", bad)
		}
	}
}

func TestUntemplate(t *testing.T) {
	src := "{{- if .Values.x }}\nname: {{ include \"a\" . }}-b\n{{- end }}\n"
	got := strings.TrimSpace(string(untemplate([]byte(src))))
	if got != "name: placeholder-b" {
		t.Errorf("untemplate = %q", got)
	}
}
