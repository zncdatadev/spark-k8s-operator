package controller

import (
	"context"
	"github.com/go-logr/logr"
	sparkv1alpha1 "github.com/zncdata-labs/spark-k8s-operator/api/v1alpha1"
	"github.com/zncdata-labs/spark-k8s-operator/internal/common"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var roles = make(map[common.Role]common.RoleReconciler)

func RegisterRole(role common.Role, roleReconciler common.RoleReconciler) {
	roles[role] = roleReconciler
}

type ClusterReconciler struct {
	client client.Client
	scheme *runtime.Scheme
	cr     *sparkv1alpha1.SparkHistoryServer
	Log    logr.Logger
}

func NewClusterReconciler(
	client client.Client,
	scheme *runtime.Scheme,
	cr *sparkv1alpha1.SparkHistoryServer) *ClusterReconciler {
	return &ClusterReconciler{
		client: client,
		scheme: scheme,
		cr:     cr,
	}
}

func (c *ClusterReconciler) ReconcileCluster(ctx context.Context) (ctrl.Result, error) {
	role := NewSparkHistoryServer(c.scheme, c.cr, c.client, c.Log)
	res, err := role.ReconcileRole(ctx)
	if err != nil {
		return ctrl.Result{}, err
	}
	return res, nil
}
