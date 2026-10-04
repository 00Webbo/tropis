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
			rules := r.Rules
			if optIn, ok := optInGrants[filepath.ToSlash(path)]; ok {
				rules = withoutOptIn(t, path, src, r.Kind, rules, optIn)
			}
			for _, v := range violations(path, rules) {
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

// optIn is a write permitted only in one template, only in a namespaced Role,
// and only behind a values flag that defaults to off.
type optIn struct {
	gate     string // the template's opening condition
	apiGroup string
	resource string
	verb     string
}

// optInGrants lists every exception to read-only, by template path. Adding to
// this list is a change to SECURITY.md's promises and must be made there too.
var optInGrants = map[string]optIn{
	// Kubernetes Events on Nodes, for notifications. SECURITY.md, "Optional:
	// Kubernetes Events".
	"helm/tropis/templates/events-rbac.yaml": {
		gate:     "{{- if .Values.notifications.events.enabled }}",
		apiGroup: "events.k8s.io",
		resource: "events",
		verb:     "create",
	},
}

// withoutOptIn checks an opt-in template is gated and namespaced, and removes
// exactly the permitted grant so everything else in it is still checked.
func withoutOptIn(t *testing.T, path string, src []byte, kind string, rules []rbacv1.PolicyRule, o optIn) []rbacv1.PolicyRule {
	t.Helper()
	if !bytes.HasPrefix(bytes.TrimSpace(src), []byte(o.gate)) {
		t.Errorf("%s must open with %q so the grant exists only when opted in", path, o.gate)
	}
	if kind != "Role" {
		t.Errorf("%s: the opt-in grant must be a namespaced Role, not a %s", path, kind)
		return rules
	}
	var out []rbacv1.PolicyRule
	for _, r := range rules {
		exact := len(r.APIGroups) == 1 && r.APIGroups[0] == o.apiGroup &&
			len(r.Resources) == 1 && r.Resources[0] == o.resource &&
			len(r.Verbs) == 1 && r.Verbs[0] == o.verb
		if !exact {
			out = append(out, r)
		}
	}
	return out
}

// The exception must stay exactly as narrow as declared.
func TestOptInGrantIsNarrow(t *testing.T) {
	src := []byte("{{- if .Values.notifications.events.enabled }}\nkind: Role\n")
	o := optInGrants["helm/tropis/templates/events-rbac.yaml"]

	wider := []rbacv1.PolicyRule{{APIGroups: []string{"events.k8s.io"}, Resources: []string{"events"}, Verbs: []string{"create", "delete"}}}
	if len(violations("x", withoutOptIn(t, "x", src, "Role", wider, o))) == 0 {
		t.Error("create+delete on events must not pass as the create-only exception")
	}
	other := []rbacv1.PolicyRule{{APIGroups: []string{""}, Resources: []string{"pods"}, Verbs: []string{"delete"}}}
	if len(violations("x", withoutOptIn(t, "x", src, "Role", other, o))) == 0 {
		t.Error("other writes in the opt-in template must still be caught")
	}
}
