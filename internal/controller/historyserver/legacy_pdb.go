/*
Copyright 2023 zncdatadev.

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
	"context"
	"fmt"
	"sort"
	"strings"

	commonsv1alpha1 "github.com/zncdatadev/operator-go/pkg/apis/commons/v1alpha1"
	"github.com/zncdatadev/operator-go/pkg/common"
	"github.com/zncdatadev/operator-go/pkg/constant"
	"github.com/zncdatadev/operator-go/pkg/reconciler"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	shsv1alpha1 "github.com/zncdatadev/spark-k8s-operator/api/v1alpha1"
)

// LegacyCompatibilityExtension owns the two compatibility actions that need cluster lifecycle hooks:
// reclaiming an unlabelled v0.12 PDB before normal reconciliation and supplementing health after
// the framework checks only its v0.13 workload label.
type LegacyCompatibilityExtension struct {
	common.BaseExtension
}

var _ common.ClusterExtension[*shsv1alpha1.SparkHistoryServer] = &LegacyCompatibilityExtension{}

// NewLegacyCompatibilityExtension creates the migration compatibility extension.
func NewLegacyCompatibilityExtension() *LegacyCompatibilityExtension {
	return &LegacyCompatibilityExtension{
		BaseExtension: common.NewBaseExtension("spark-history-server-legacy-compatibility"),
	}
}

// PreReconcile deletes only a disabled legacy PDB. An enabled PDB is left for v0.13 to adopt and
// stamp with its role-slot label; framework-created and user-created same-name PDBs are untouched.
func (e *LegacyCompatibilityExtension) PreReconcile(
	ctx context.Context,
	k8sClient ctrlclient.Client,
	cr *shsv1alpha1.SparkHistoryServer,
) error {
	if err := prepareRemovedLegacyRoleGroupsForCleanup(ctx, k8sClient, cr); err != nil {
		return err
	}

	if legacyRolePDBEnabled(cr) {
		return nil
	}

	name := reconciler.RoleResourceName(cr.Name, shsv1alpha1.RoleNode)
	pdb := &policyv1.PodDisruptionBudget{}
	if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: cr.Namespace, Name: name}, pdb); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}
	if pdb.Labels[reconciler.LabelRolePodDisruptionBudget] != "" ||
		!metav1.IsControlledBy(pdb, cr) ||
		pdb.Labels[constant.LabelKubernetesName] != AppName ||
		pdb.Labels[constant.LabelKubernetesInstance] != cr.Name ||
		pdb.Labels[constant.LabelKubernetesComponent] != shsv1alpha1.RoleNode ||
		pdb.Labels[constant.LabelKubernetesManagedBy] != shsv1alpha1.GroupVersion.Group {
		return nil
	}
	return k8sClient.Delete(ctx, pdb)
}

// prepareRemovedLegacyRoleGroupsForCleanup rehydrates the cleanup ledger for v0.12 role groups
// that disappeared from the spec before their first v0.13 reconciliation. The framework's live
// discovery ignores their legacy managed-by label and the old status has no ledger. StatefulSet,
// ConfigMap, client Service and metrics Service are all inspected so at least one durable anchor
// remains after every intermediate cleanup step; a controller restart can therefore reconstruct
// the ledger until the final Service is gone. No live resource is mutated here.
func prepareRemovedLegacyRoleGroupsForCleanup(
	ctx context.Context,
	k8sClient ctrlclient.Client,
	cr *shsv1alpha1.SparkHistoryServer,
) error {
	if cr == nil || cr.UID == "" {
		return nil
	}

	listOptions := []ctrlclient.ListOption{
		ctrlclient.InNamespace(cr.Namespace),
		ctrlclient.MatchingLabels{
			constant.LabelKubernetesName:      AppName,
			constant.LabelKubernetesInstance:  cr.Name,
			constant.LabelKubernetesComponent: shsv1alpha1.RoleNode,
			constant.LabelKubernetesManagedBy: shsv1alpha1.GroupVersion.Group,
		},
	}
	statefulSets := &appsv1.StatefulSetList{}
	if err := k8sClient.List(ctx, statefulSets, listOptions...); err != nil {
		return fmt.Errorf("listing legacy StatefulSets for orphan recovery: %w", err)
	}
	configMaps := &corev1.ConfigMapList{}
	if err := k8sClient.List(ctx, configMaps, listOptions...); err != nil {
		return fmt.Errorf("listing legacy ConfigMaps for orphan recovery: %w", err)
	}
	services := &corev1.ServiceList{}
	if err := k8sClient.List(ctx, services, listOptions...); err != nil {
		return fmt.Errorf("listing legacy Services for orphan recovery: %w", err)
	}

	candidates := make([]ctrlclient.Object, 0,
		len(statefulSets.Items)+len(configMaps.Items)+len(services.Items))
	for i := range statefulSets.Items {
		candidates = append(candidates, &statefulSets.Items[i])
	}
	for i := range configMaps.Items {
		candidates = append(candidates, &configMaps.Items[i])
	}
	for i := range services.Items {
		candidates = append(candidates, &services.Items[i])
	}

	orphanGroups := make(map[string]struct{})
	for _, candidate := range candidates {
		groupName, orphan := removedLegacyRoleGroupOf(cr, candidate)
		if !orphan {
			continue
		}
		legacyName := fmt.Sprintf("%s-%s-%s", cr.Name, shsv1alpha1.RoleNode, groupName)
		frameworkName := reconciler.RoleGroupResourceName(cr.Name, shsv1alpha1.RoleNode, groupName)
		if legacyName != frameworkName {
			return fmt.Errorf(
				"removed legacy role group resource %q cannot be adopted for cleanup because operator-go v0.13 names its slot %q; "+
					"restore role group %q and follow the documented long-name migration before upgrade",
				legacyName, frameworkName, groupName,
			)
		}
		orphanGroups[groupName] = struct{}{}
	}

	// Cleanup is an ordered state machine that can span several reconciles. Once its
	// StatefulSet and ConfigMap anchors are gone, only this ledger keeps the remaining
	// Services discoverable until the framework confirms the whole slot is reclaimed.
	for groupName := range orphanGroups {
		cr.GetStatus().SetRoleGroup(shsv1alpha1.RoleNode, groupName)
	}
	return nil
}

func removedLegacyRoleGroupOf(
	cr *shsv1alpha1.SparkHistoryServer,
	object ctrlclient.Object,
) (string, bool) {
	if !metav1.IsControlledBy(object, cr) {
		return "", false
	}
	groupName := object.GetLabels()[constant.LabelKubernetesRoleGroup]
	if groupName == "" {
		return "", false
	}
	legacyName := fmt.Sprintf("%s-%s-%s", cr.Name, shsv1alpha1.RoleNode, groupName)
	validName := object.GetName() == legacyName
	if _, service := object.(*corev1.Service); service {
		validName = validName || object.GetName() == legacyName+"-metrics"
	}
	if !validName {
		return "", false
	}
	if cr.Spec.Node != nil {
		if _, declared := cr.Spec.Node.RoleGroups[groupName]; declared {
			return "", false
		}
	}
	return groupName, true
}

// PostReconcile supplements the framework's pod failure check for an adopted StatefulSet. Its
// immutable selector forces legacy managed-by labels onto those Pods, while the v0.13 health
// manager lists only managed-by=operator-go. Fresh workloads are intentionally invisible here.
func (e *LegacyCompatibilityExtension) PostReconcile(
	ctx context.Context,
	k8sClient ctrlclient.Client,
	cr *shsv1alpha1.SparkHistoryServer,
) error {
	if cr == nil || (cr.Spec.ClusterOperation != nil && cr.Spec.ClusterOperation.Stopped) {
		return nil
	}
	current := cr.GetStatus().GetCondition(commonsv1alpha1.ConditionDegraded)
	if current != nil && current.Status == metav1.ConditionTrue {
		return nil
	}

	pods := &corev1.PodList{}
	if err := k8sClient.List(
		ctx,
		pods,
		ctrlclient.InNamespace(cr.Namespace),
		ctrlclient.MatchingLabels{
			constant.LabelKubernetesName:      AppName,
			constant.LabelKubernetesInstance:  cr.Name,
			constant.LabelKubernetesManagedBy: shsv1alpha1.GroupVersion.Group,
		},
	); err != nil {
		// Match the framework's health policy: an observation failure is an operator error, not
		// evidence that the Spark workload itself is degraded.
		log.FromContext(ctx).Error(err, "Failed to list legacy-labelled pods while evaluating health")
		return nil
	}

	failures := make([]legacyPodFailure, 0)
	for i := range pods.Items {
		pod := &pods.Items[i]
		if !pod.DeletionTimestamp.IsZero() {
			continue
		}
		if reason := legacyPodFailureReason(pod); reason != "" {
			failures = append(failures, legacyPodFailure{pod: pod.Name, reason: reason})
		}
	}
	if len(failures) == 0 {
		return nil
	}
	sort.Slice(failures, func(i, j int) bool { return failures[i].pod < failures[j].pod })
	cr.GetStatus().SetDegraded(
		true,
		commonsv1alpha1.ReasonPodFailure,
		summarizeLegacyPodFailures(failures),
	)
	return nil
}

// OnReconcileError has no rollback work; deletion is idempotent and already complete.
func (e *LegacyCompatibilityExtension) OnReconcileError(
	context.Context,
	ctrlclient.Client,
	*shsv1alpha1.SparkHistoryServer,
	error,
) error {
	return nil
}

func legacyRolePDBEnabled(cr *shsv1alpha1.SparkHistoryServer) bool {
	return cr != nil && cr.Spec.Node != nil && cr.Spec.Node.RoleConfig != nil &&
		cr.Spec.Node.RoleConfig.PodDisruptionBudget != nil &&
		cr.Spec.Node.RoleConfig.PodDisruptionBudget.IsEnabled()
}

var legacyStuckContainerReasons = map[string]struct{}{
	"CrashLoopBackOff":           {},
	reasonImagePullBackOff:       {},
	"ErrImagePull":               {},
	reasonErrImageNeverPull:      {},
	"InvalidImageName":           {},
	"CreateContainerConfigError": {},
	"CreateContainerError":       {},
	"RunContainerError":          {},
}

const (
	reasonImagePullBackOff  = "ImagePullBackOff"
	reasonErrImageNeverPull = "ErrImageNeverPull"
)

type legacyPodFailure struct {
	pod    string
	reason string
}

func legacyPodFailureReason(pod *corev1.Pod) string {
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodScheduled && condition.Status == corev1.ConditionFalse &&
			condition.Reason == corev1.PodReasonUnschedulable {
			return corev1.PodReasonUnschedulable
		}
	}
	for _, statuses := range [][]corev1.ContainerStatus{
		pod.Status.InitContainerStatuses,
		pod.Status.ContainerStatuses,
	} {
		for i := range statuses {
			waiting := statuses[i].State.Waiting
			if waiting == nil {
				continue
			}
			if _, stuck := legacyStuckContainerReasons[waiting.Reason]; stuck {
				return waiting.Reason
			}
		}
	}
	return ""
}

func summarizeLegacyPodFailures(failures []legacyPodFailure) string {
	const maxReportedFailures = 3

	shown := failures
	if len(shown) > maxReportedFailures {
		shown = shown[:maxReportedFailures]
	}
	parts := make([]string, 0, len(shown))
	for _, failure := range shown {
		parts = append(parts, fmt.Sprintf("%s (%s)", failure.pod, failure.reason))
	}
	message := "Pods requiring attention: " + strings.Join(parts, ", ")
	if remaining := len(failures) - len(shown); remaining > 0 {
		message += fmt.Sprintf(", and %d more", remaining)
	}
	return message
}
