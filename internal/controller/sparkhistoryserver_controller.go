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

	sparkv1alpha1 "github.com/zncdata-labs/spark-k8s-operator/api/v1alpha1"
	"github.com/zncdata-labs/spark-k8s-operator/internal/common"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var (
	logger = ctrl.Log.WithName("controller")
)

// SparkHistoryServerReconciler reconciles a SparkHistoryServer object
type SparkHistoryServerReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=spark.zncdata.dev,resources=sparkhistoryservers,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=spark.zncdata.dev,resources=sparkhistoryservers/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=spark.zncdata.dev,resources=sparkhistoryservers/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=configmaps,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=secrets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=persistentvolumeclaims,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=networking.k8s.io,resources=ingresses,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch

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

	logger.Info("Reconciling SparkHistory")

	sparkHistory := &sparkv1alpha1.SparkHistoryServer{}

	if err := r.Get(ctx, req.NamespacedName, sparkHistory); err != nil {
		if client.IgnoreNotFound(err) != nil {
			logger.Error(err, "unable to fetch SparkHistoryServer")
			return ctrl.Result{}, err
		}
		logger.Info("SparkHistoryServer resource not found. Ignoring since object must be deleted")
		return ctrl.Result{}, nil
	}

	logger.Info("SparkHistoryServer found", "Name", sparkHistory.Name, "Namespace", sparkHistory.Namespace)

	if r.ReconciliationPaused(ctx, sparkHistory) {
		logger.Info("Reconciliation is paused for SparkHistoryServer")
		return ctrl.Result{}, nil
	}

	result, err := NewClusterReconciler(r.Client, r.Scheme, sparkHistory).ReconcileCluster(ctx)
	if err != nil {
		return ctrl.Result{}, err
	} else if result.Requeue || result.RequeueAfter > 0 {
		return result, nil
	}

	logger.Info("Successfully reconciled SparkHistoryServer")
	return result, nil
}

func (r *SparkHistoryServerReconciler) ReconciliationPaused(
	ctx context.Context,
	instance *sparkv1alpha1.SparkHistoryServer,
) bool {
	clusterOperation := common.NewClusterOperation(
		&SparkHistoryInstance{Instance: instance},
		common.ResourceClient{
			Ctx:       ctx,
			Client:    r.Client,
			Namespace: instance.Namespace,
		},
	)
	return clusterOperation.ReconciliationPaused()
}

// SetupWithManager sets up the controller with the Manager.
func (r *SparkHistoryServerReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&sparkv1alpha1.SparkHistoryServer{}).
		Complete(r)
}

type SparkHistoryInstance struct {
	Instance *sparkv1alpha1.SparkHistoryServer
}

func (i *SparkHistoryInstance) GetClusterConfig() *sparkv1alpha1.ClusterConfigSpec {
	return i.Instance.Spec.ClusterConfig
}

func (i *SparkHistoryInstance) GetClusterOperation() *sparkv1alpha1.ClusterOperationSpec {
	return i.Instance.Spec.ClusterOperation
}
