package controller

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
	"sigs.k8s.io/yaml"
)

// The manager's generated ClusterRole must actually permit everything the
// reconciler does.
//
// envtest runs with administrator credentials and never evaluates RBAC, so no
// other test in this package can catch a missing verb. A shipped release once
// called Delete on Deployments and Services without holding delete, which
// returned Forbidden -- not NotFound -- and aborted reconciliation before the
// probe runner was ever created. This test reads the generated role and pins
// the verbs to the operations the code performs.
func TestGeneratedClusterRoleCoversEveryOperation(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile(filepath.Join("..", "..", "config", "rbac", "role.yaml"))
	if err != nil {
		t.Fatalf("reading generated role: %v", err)
	}

	var role rbacv1.ClusterRole
	if err := yaml.Unmarshal(raw, &role); err != nil {
		t.Fatalf("parsing generated role: %v", err)
	}

	// permitted[group/resource] is the set of verbs granted.
	permitted := map[string][]string{}
	for _, rule := range role.Rules {
		for _, group := range rule.APIGroups {
			for _, resource := range rule.Resources {
				key := group + "/" + resource
				permitted[key] = append(permitted[key], rule.Verbs...)
			}
		}
	}

	required := []struct {
		resource string
		verbs    []string
		why      string
	}{
		{
			resource: "apps/deployments",
			verbs:    []string{"get", "create", "update", "patch", "delete"},
			why:      "ensureIncidentEngine creates it; removeIncidentEngine and the legacy migration delete it",
		},
		{
			resource: "apps/statefulsets",
			verbs:    []string{"get", "create", "update", "patch"},
			why:      "ensureProbeRunner reconciles the probe runner StatefulSet",
		},
		{
			resource: "/services",
			verbs:    []string{"get", "create", "update", "patch", "delete"},
			why:      "ensureNamedService creates them; removeIncidentEngine deletes the engine's",
		},
		{
			resource: "/configmaps",
			verbs:    []string{"get", "create", "update", "patch"},
			why:      "the probe config ConfigMap is written every reconcile",
		},
		{
			resource: "/secrets",
			verbs:    []string{"get", "list", "watch", "create", "update", "patch"},
			why:      "credentials are read from user Secrets and written to the probe auth Secret",
		},
		{
			resource: "/events",
			verbs:    []string{"create", "patch"},
			why:      "the status syncer records incident Events",
		},
		{
			resource: "canary.iambarton.com/anomalypolicies",
			verbs:    []string{"get", "list", "watch"},
			why:      "policies are listed every reconcile",
		},
		{
			resource: "canary.iambarton.com/anomalypolicies/status",
			verbs:    []string{"get", "update", "patch"},
			why:      "inferred dependency proposals are written back to policy status",
		},
		{
			resource: "canary.iambarton.com/grpccanaries",
			verbs:    []string{"get", "list", "watch"},
			why:      "gRPC canaries are listed alongside HTTP ones",
		},
		{
			resource: "canary.iambarton.com/grpccanaries/status",
			verbs:    []string{"get", "update", "patch"},
			why:      "the status syncer writes gRPC canary status",
		},
	}

	for _, requirement := range required {
		granted := permitted[requirement.resource]
		if granted == nil {
			t.Errorf("no rule grants anything on %s (needed: %s)",
				requirement.resource, requirement.why)
			continue
		}

		for _, verb := range requirement.verbs {
			if !slices.Contains(granted, verb) && !slices.Contains(granted, "*") {
				t.Errorf("%s is missing verb %q (needed: %s); granted: %v",
					requirement.resource, verb, requirement.why, granted)
			}
		}
	}
}
