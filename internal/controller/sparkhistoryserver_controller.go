/*
Copyright 2023 zncdata-labs.

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

package controller

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	stackv1alpha1 "github.com/zncdata-labs/spark-k8s-operator/api/v1alpha1"
)

// SparkHistoryServerReconciler reconciles a SparkHistoryServer object
type SparkHistoryServerReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

//+kubebuilder:rbac:groups=stack.zncdata.net,resources=sparkhistoryservers,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=stack.zncdata.net,resources=sparkhistoryservers/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=stack.zncdata.net,resources=sparkhistoryservers/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=configmaps,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=persistentvolumeclaims,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=extensions,resources=ingresses,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the SparkHistoryServer object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.14.1/pkg/reconcile
func (r *SparkHistoryServerReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	logger.Info("Reconciling SparkHistory")

	sparkHistory := &stackv1alpha1.SparkHistoryServer{}

	if err := r.Get(ctx, req.NamespacedName, sparkHistory); err != nil {
		if client.IgnoreNotFound(err) != nil {
			logger.Error(err, "unable to fetch SparkHistoryServer")
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	logger.Info("SparkHistoryServer found", "Name", sparkHistory.Name)

	if err := r.reconcilePVC(ctx, sparkHistory); err != nil {
		logger.Error(err, "unable to reconcile PVC")
		return ctrl.Result{}, err
	}

	if err := r.reconcileDeployment(ctx, sparkHistory); err != nil {
		logger.Error(err, "unable to reconcile Deployment")
		return ctrl.Result{}, err
	}

	if err := r.reconcileService(ctx, sparkHistory); err != nil {
		logger.Error(err, "unable to reconcile Service")
		return ctrl.Result{}, err
	}

	if err := r.reconcileIngress(ctx, sparkHistory); err != nil {
		logger.Error(err, "unable to reconcile Ingress")
		return ctrl.Result{}, err
	}

	podList := &corev1.PodList{}
	if err := r.List(ctx, podList, &client.ListOptions{Namespace: sparkHistory.Namespace, LabelSelector: labels.SelectorFromSet(sparkHistory.GetLabels())}); err != nil {
		logger.Error(err, "unable to list pods")
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *SparkHistoryServerReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&stackv1alpha1.SparkHistoryServer{}).
		Complete(r)
}
