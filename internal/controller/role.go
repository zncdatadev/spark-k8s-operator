package controller

import (
	"context"
	"strings"

	"github.com/go-logr/logr"
	sparkv1alpha1 "github.com/zncdata-labs/spark-k8s-operator/api/v1alpha1"
	"github.com/zncdata-labs/spark-k8s-operator/internal/common"
	"github.com/zncdata-labs/spark-k8s-operator/internal/util"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// roleMaster reconciler

type SparkHistoryServer struct {
	common.BaseRoleReconciler[*sparkv1alpha1.SparkHistoryServer, *sparkv1alpha1.RoleSpec]
}

// NewSparkHistoryServer  new roleMaster
func NewSparkHistoryServer(
	scheme *runtime.Scheme,
	instance *sparkv1alpha1.SparkHistoryServer,
	client client.Client,
	log logr.Logger) *SparkHistoryServer {
	r := &SparkHistoryServer{
		BaseRoleReconciler: common.BaseRoleReconciler[*sparkv1alpha1.SparkHistoryServer, *sparkv1alpha1.RoleSpec]{
			Scheme:   scheme,
			Instance: instance,
			Client:   client,
			Log:      log,
			Role:     instance.Spec.SparkHistory,
		},
	}
	r.Labels = r.MergeLabels()
	return r
}

func (r *SparkHistoryServer) RoleName() common.Role {
	return common.HistoryServer
}

func (r *SparkHistoryServer) MergeLabels() map[string]string {
	return r.GetLabels(r.RoleName())
}

func (r *SparkHistoryServer) ReconcileRole(ctx context.Context) (ctrl.Result, error) {
	if r.Role.Config != nil && r.Role.Config.PodDisruptionBudget != nil {
		pdb := common.NewReconcilePDB[*sparkv1alpha1.SparkHistoryServer](
			r.Client,
			r.Scheme,
			r.Instance,
			r.Labels,
			string(r.RoleName()),
			r.Role.Config.PodDisruptionBudget)
		res, err := pdb.ReconcileResource(ctx, "", pdb)
		if err != nil {
			return ctrl.Result{}, err
		}
		if res.RequeueAfter > 0 {
			return res, nil
		}
	}

	for name := range r.Role.RoleGroups {
		groupReconciler := NewRoleMasterGroup(r.Scheme, r.Instance, r.Client, name, r.Labels, r.Log)
		res, err := groupReconciler.ReconcileGroup(ctx)
		if err != nil {
			return ctrl.Result{}, err
		}
		if res.RequeueAfter > 0 {
			return res, nil
		}
	}
	return ctrl.Result{}, nil
}

// RoleMasterGroup master role group reconcile
type RoleMasterGroup struct {
	Scheme     *runtime.Scheme
	Instance   *sparkv1alpha1.SparkHistoryServer
	Client     client.Client
	GroupName  string
	RoleLabels map[string]string
	Log        logr.Logger
}

func NewRoleMasterGroup(
	scheme *runtime.Scheme,
	instance *sparkv1alpha1.SparkHistoryServer,
	client client.Client,
	groupName string,
	roleLabels map[string]string,
	log logr.Logger) *RoleMasterGroup {
	r := &RoleMasterGroup{
		Scheme:     scheme,
		Instance:   instance,
		Client:     client,
		GroupName:  groupName,
		RoleLabels: roleLabels,
		Log:        log,
	}
	return r
}

// ReconcileGroup ReconcileRole implements the Role interface
func (r *RoleMasterGroup) ReconcileGroup(ctx context.Context) (ctrl.Result, error) {
	//reconcile all resources below

	//convert any to *sparkv1alpha1.MasterRoleGroupSpec
	mergedCfgObj := r.MergeGroupConfigSpec()
	mergedGroupCfg := mergedCfgObj.(*sparkv1alpha1.RoleGroupSpec)

	mergedLabels := r.MergeLabels(mergedGroupCfg)
	//pdb
	if mergedGroupCfg.Config != nil && mergedGroupCfg.Config.PodDisruptionBudget != nil {
		pdb := common.NewReconcilePDB[*sparkv1alpha1.SparkHistoryServer](
			r.Client,
			r.Scheme,
			r.Instance,
			mergedLabels,
			r.GroupName,
			nil)
		if resource, err := pdb.ReconcileResource(ctx, r.GroupName, pdb); err != nil {
			r.Log.Error(err, "Reconcile pdb  failed", "groupName", r.GroupName)
			return ctrl.Result{}, err
		} else if resource.RequeueAfter > 0 {
			return resource, nil
		}
	}
	// configmap
	configmap := NewConfigMap(
		r.Scheme, r.Instance, r.Client, r.GroupName, mergedLabels, mergedGroupCfg)
	if res, err := configmap.ReconcileResource(ctx, r.GroupName, configmap); err != nil {
		r.Log.Error(err, "Reconcile configmap failed", "groupName", r.GroupName)
		return ctrl.Result{}, err
	} else if res.RequeueAfter > 0 {
		return res, nil
	}
	// secret
	secret := NewSecret(
		r.Scheme, r.Instance, r.Client, r.GroupName, mergedLabels, mergedGroupCfg)
	if res, err := secret.ReconcileResource(ctx, r.GroupName, secret); err != nil {
		r.Log.Error(err, "Reconcile secret failed", "groupName", r.GroupName)
		return ctrl.Result{}, err
	} else if res.RequeueAfter > 0 {
		return res, nil
	}
	//pvc
	pvc := NewPvc(
		r.Scheme, r.Instance, r.Client, r.GroupName, mergedLabels, mergedGroupCfg)
	if res, err := pvc.ReconcileResource(ctx, r.GroupName, pvc); err != nil {
		r.Log.Error(err, "Reconcile pvc failed", "groupName", r.GroupName)
		return ctrl.Result{}, err
	} else if res.RequeueAfter > 0 {
		return res, nil
	}
	//deployment
	deployment := NewDeployment(
		r.Scheme, r.Instance, r.Client, r.GroupName, mergedLabels, mergedGroupCfg, mergedGroupCfg.Replicas)
	if res, err := deployment.ReconcileResource(ctx, r.GroupName, deployment); err != nil {
		r.Log.Error(err, "Reconcile deployment failed", "groupName", r.GroupName)
		return ctrl.Result{}, err
	} else if res.RequeueAfter > 0 {
		return res, nil
	}
	// service
	svc := NewService(
		r.Scheme, r.Instance, r.Client, r.GroupName, mergedLabels, mergedGroupCfg)
	if res, err := svc.ReconcileResource(ctx, r.GroupName, svc); err != nil {
		r.Log.Error(err, "Reconcile service failed", "groupName", r.GroupName)
		return ctrl.Result{}, err
	} else if res.RequeueAfter > 0 {
		return res, nil
	}
	//ingress
	ingress := NewIngress(
		r.Scheme, r.Instance, r.Client, r.GroupName, mergedLabels, mergedGroupCfg)
	if res, err := ingress.ReconcileResource(ctx, r.GroupName, ingress); err != nil {
		r.Log.Error(err, "Reconcile ingress failed", "groupName", r.GroupName)
		return ctrl.Result{}, err
	} else if res.RequeueAfter > 0 {
		return res, nil
	}
	return ctrl.Result{}, nil
}

func (r *RoleMasterGroup) MergeGroupConfigSpec() any {
	originMasterCfg := r.Instance.Spec.SparkHistory.RoleGroups[r.GroupName]
	instance := r.Instance
	// Merge the role into the role group.
	// if the role group has a config, and role group not has a config, will
	// merge the role's config into the role group's config.
	return mergeConfig(instance.Spec.SparkHistory, originMasterCfg)
}

func (r *RoleMasterGroup) MergeLabels(mergedCfg any) map[string]string {
	mergedMasterCfg := mergedCfg.(*sparkv1alpha1.RoleGroupSpec)
	roleLabels := r.RoleLabels
	mergeLabels := make(util.Map)
	mergeLabels.MapMerge(roleLabels, true)
	mergeLabels.MapMerge(mergedMasterCfg.Config.NodeSelector, true)
	mergeLabels["app.kubernetes.io/instance"] = strings.ToLower(r.GroupName)
	return mergeLabels
}

// mergeConfig merge the role's config into the role group's config
func mergeConfig(
	masterRole *sparkv1alpha1.RoleSpec,
	group *sparkv1alpha1.RoleGroupSpec) *sparkv1alpha1.RoleGroupSpec {
	copiedRoleGroup := group.DeepCopy()
	// Merge the role into the role group.
	// if the role group has a config, and role group not has a config, will
	// merge the role's config into the role group's config.
	common.MergeObjects(copiedRoleGroup, masterRole, []string{"RoleGroups"})

	// merge the role's config into the role group's config
	if masterRole.Config != nil && copiedRoleGroup.Config != nil {
		common.MergeObjects(copiedRoleGroup.Config, masterRole.Config, []string{})
	}
	return copiedRoleGroup
}

//type LogDataBuilder struct {
//	cfg *sparkv1alpha1.RoleGroupSpec
//}
