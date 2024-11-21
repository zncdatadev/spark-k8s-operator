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

	"github.com/zncdatadev/operator-go/pkg/client"
	"github.com/zncdatadev/operator-go/pkg/reconciler"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"

	sparkv1alpha1 "github.com/zncdatadev/spark-k8s-operator/api/v1alpha1"
)

var (
	logger = ctrl.Log.WithName("controller")
)

// SparkHistoryServerReconciler reconciles a SparkHistoryServer object
type SparkHistoryServerReconciler struct {
	ctrlclient.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=spark.kubedoop.dev,resources=sparkhistoryservers,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=spark.kubedoop.dev,resources=sparkhistoryservers/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=spark.kubedoop.dev,resources=sparkhistoryservers/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=configmaps,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=secrets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups=authentication.kubedoop.dev,resources=authenticationclasses,verbs=get;list;watch
// +kubebuilder:rbac:groups=s3.kubedoop.dev,resources=s3connections,verbs=get;list;watch
// +kubebuilder:rbac:groups=s3.kubedoop.dev,resources=s3buckets,verbs=get;list;watch
// +kubebuilder:rbac:groups=policy,resources=poddisruptionbudgets,verbs=get;list;watch;create;update;patch;delete

func (r *SparkHistoryServerReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {

	logger.Info("Reconciling SparkHistory")

	instance := &sparkv1alpha1.SparkHistoryServer{}
	err := r.Get(ctx, req.NamespacedName, instance)
	if err != nil {
		if ctrlclient.IgnoreNotFound(err) == nil {
			logger.V(1).Info("SparkHistoryServer resource not found. Ignoring since object must be deleted.")
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	resourceClient := &client.Client{
		Client:         r.Client,
		OwnerReference: instance,
	}

	clusterInfo := reconciler.ClusterInfo{
		GVK: &metav1.GroupVersionKind{
			Group:   sparkv1alpha1.GroupVersion.Group,
			Version: sparkv1alpha1.GroupVersion.Version,
			Kind:    "SparkHistoryServer",
		},
		ClusterName: instance.Name,
	}

	reconciler := NewClusterReconciler(resourceClient, clusterInfo, &instance.Spec)

	if err := reconciler.RegisterResource(ctx); err != nil {
		return ctrl.Result{}, err
	}

	return reconciler.Run(ctx)
}

// SetupWithManager sets up the controller with the Manager.
func (r *SparkHistoryServerReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&sparkv1alpha1.SparkHistoryServer{}).
		Complete(r)
}
