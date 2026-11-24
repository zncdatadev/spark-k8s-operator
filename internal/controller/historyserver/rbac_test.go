/*
Copyright 2026 ZNCDataDev.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package historyserver

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	rbacv1 "k8s.io/api/rbac/v1"
	"sigs.k8s.io/yaml"
)

// grantSet expands a ClusterRole into exact group/resource:verb triples. Wildcards
// remain literal, so an over-broad wildcard grant cannot satisfy an enumerated set.
func grantSet(role *rbacv1.ClusterRole) []string {
	var grants []string
	for _, rule := range role.Rules {
		for _, group := range rule.APIGroups {
			for _, resource := range rule.Resources {
				for _, verb := range rule.Verbs {
					grants = append(grants, fmt.Sprintf("%s/%s:%s", group, resource, verb))
				}
			}
		}
	}
	sort.Strings(grants)
	return slices.Compact(grants)
}

func readClusterRole(path string) *rbacv1.ClusterRole {
	content, err := os.ReadFile(path)
	Expect(err).NotTo(HaveOccurred())

	role := &rbacv1.ClusterRole{}
	Expect(yaml.Unmarshal(content, role)).To(Succeed())
	return role
}

// readHelmClusterRole removes the small amount of Helm-only metadata templating.
// The RBAC rules themselves deliberately contain no template expressions, allowing
// this unit test to catch drift without requiring the helm binary.
func readHelmClusterRole(path string) *rbacv1.ClusterRole {
	content, err := os.ReadFile(path)
	Expect(err).NotTo(HaveOccurred())

	var rendered []string
	for _, line := range strings.Split(string(content), "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "{{"):
			continue
		case strings.HasPrefix(trimmed, "name: {{"):
			rendered = append(rendered, "  name: helm-manager-role")
		case trimmed == "labels:":
			continue
		default:
			rendered = append(rendered, line)
		}
	}

	role := &rbacv1.ClusterRole{}
	Expect(yaml.Unmarshal([]byte(strings.Join(rendered, "\n")), role)).To(Succeed())
	return role
}

var _ = Describe("Manager ClusterRole", func() {
	It("grants exactly the operator-go baseline and Spark dependencies", func() {
		expected := []string{
			"spark.kubedoop.dev/sparkhistoryservers:get",
			"spark.kubedoop.dev/sparkhistoryservers:list",
			"spark.kubedoop.dev/sparkhistoryservers:watch",
			"spark.kubedoop.dev/sparkhistoryservers/status:get",
			"spark.kubedoop.dev/sparkhistoryservers/status:update",
			"spark.kubedoop.dev/sparkhistoryservers/status:patch",
			"spark.kubedoop.dev/sparkhistoryservers/finalizers:update",

			"apps/statefulsets:get", "apps/statefulsets:list", "apps/statefulsets:watch",
			"apps/statefulsets:create", "apps/statefulsets:update", "apps/statefulsets:patch",
			"apps/statefulsets:delete",
			"/configmaps:get", "/configmaps:list", "/configmaps:watch",
			"/configmaps:create", "/configmaps:update", "/configmaps:patch", "/configmaps:delete",
			"/services:get", "/services:list", "/services:watch",
			"/services:create", "/services:update", "/services:patch", "/services:delete",
			"policy/poddisruptionbudgets:get", "policy/poddisruptionbudgets:list",
			"policy/poddisruptionbudgets:watch", "policy/poddisruptionbudgets:create",
			"policy/poddisruptionbudgets:update", "policy/poddisruptionbudgets:patch",
			"policy/poddisruptionbudgets:delete",

			"/serviceaccounts:get", "/serviceaccounts:list", "/serviceaccounts:watch",
			"/serviceaccounts:create", "/serviceaccounts:update", "/serviceaccounts:patch",
			"/persistentvolumeclaims:get", "/persistentvolumeclaims:list",
			"/persistentvolumeclaims:watch", "/persistentvolumeclaims:delete",
			"/pods:get", "/pods:list", "/pods:watch",
			"/events:create", "/events:patch",

			"/secrets:get", "/secrets:list", "/secrets:watch",
			"/secrets:create", "/secrets:update", "/secrets:patch",
			"authentication.kubedoop.dev/authenticationclasses:get",
			"authentication.kubedoop.dev/authenticationclasses:list",
			"authentication.kubedoop.dev/authenticationclasses:watch",
			"s3.kubedoop.dev/s3buckets:get", "s3.kubedoop.dev/s3buckets:list",
			"s3.kubedoop.dev/s3buckets:watch",
			"s3.kubedoop.dev/s3connections:get", "s3.kubedoop.dev/s3connections:list",
			"s3.kubedoop.dev/s3connections:watch",
		}
		sort.Strings(expected)

		root := filepath.Join("..", "..", "..")
		generatedRole := readClusterRole(filepath.Join(root, "config", "rbac", "role.yaml"))
		Expect(generatedRole.Name).To(Equal("manager-role"))
		Expect(grantSet(generatedRole)).To(Equal(expected),
			"config/rbac/role.yaml drifted; run `make manifests` after changing RBAC markers")

		helmRole := readHelmClusterRole(filepath.Join(root, "deploy", "helm", "spark-k8s-operator", "templates", "clusterrole.yaml"))
		Expect(grantSet(helmRole)).To(Equal(expected),
			"the Helm ClusterRole must stay in step with the generated manager role")
	})
})
