package controller

import (
	stackv1alpha1 "github.com/zncdata-labs/spark-k8s-operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
)

// GetRoleGroupEx todo - use inject
/*func GetRoleGroupEx(clusterCfg any, roleCfg any, groupCfg any, fields ...string) client.Object {
	var mergedRoleGroup client.Object

	selectFields := make(map[string]bool)
	for _, field := range fields {
		selectFields[field] = true
	}

	// can edit use reflect.ValueOf(&mergedRoleGroup)
	roleGroupValue := reflect.ValueOf(groupCfg).Elem()
	roleConfigValue := reflect.ValueOf(roleCfg).Elem()
	clusterConfigValue := reflect.ValueOf(clusterCfg).Elem()
	mergedRoleGroupValue := reflect.ValueOf(mergedRoleGroup).Elem()

	for i := 0; i < roleGroupValue.NumField(); i++ {
		field := roleGroupValue.Field(i)
		if !selectFields[field.Type().Name()] {
			continue
		}

		if field.IsNil() {
			roleConfigField := roleConfigValue.Field(i)
			if !roleConfigField.IsNil() {
				mergedRoleGroupValue.Field(i).Set(roleConfigField)
			} else {
				clusterConfigField := clusterConfigValue.Field(i)
				if !clusterConfigField.IsNil() {
					mergedRoleGroupValue.Field(i).Set(clusterConfigField)
				}
			}
		} else {
			mergedRoleGroupValue.Field(i).Set(field)
		}
	}
	return mergedRoleGroup
}*/

func (r *SparkHistoryServerReconciler) getRoleGroupLabels(config *stackv1alpha1.RoleConfigSpec) map[string]string {
	additionalLabels := make(map[string]string)
	if configLabels := config.MatchLabels; configLabels != nil {
		for k, v := range config.MatchLabels {
			additionalLabels[k] = v
		}
	}
	return additionalLabels
}

// merge labels
func (r *SparkHistoryServerReconciler) mergeLabels(instanceLabels map[string]string,
	roleGroup *stackv1alpha1.RoleConfigSpec) Map {
	var mergeLabels *Map
	mergeLabels.MapMerge(instanceLabels, true)
	mergeLabels.MapMerge(r.getRoleGroupLabels(roleGroup), true)
	return *mergeLabels
}

// get service info
func (r *SparkHistoryServerReconciler) getServiceInfo(instanceSvc *stackv1alpha1.ServiceSpec,
	roleGroup *stackv1alpha1.RoleConfigSpec) (int32, corev1.ServiceType, map[string]string) {
	var targetSvc = instanceSvc
	if roleGroup != nil && roleGroup.Service != nil {
		targetSvc = roleGroup.Service
	}
	return targetSvc.Port, targetSvc.Type, targetSvc.Annotations
}

// get pvc info
func (r *SparkHistoryServerReconciler) getPvcInfo(instance *stackv1alpha1.SparkHistoryServer,
	roleGroup *stackv1alpha1.RoleConfigSpec) (*string, string, corev1.PersistentVolumeMode, *stackv1alpha1.S3Spec) {
	var (
		storageClassName = instance.Spec.RoleConfig.StorageClass
		storageSize      = instance.Spec.RoleConfig.StorageSize
		volumeMode       = corev1.PersistentVolumeFilesystem
		s3               = instance.Spec.RoleConfig.Config.S3
	)
	if roleGroup != nil {
		if rgStorageClass := roleGroup.StorageClass; rgStorageClass != nil {
			storageClassName = rgStorageClass
		}
		if rgStorageSize := roleGroup.StorageSize; rgStorageSize != "" {
			storageSize = rgStorageSize
		}
		if rgs3 := roleGroup.Config.S3; rgs3 != nil {
			s3 = rgs3
		}
	}
	return storageClassName, storageSize, volumeMode, s3
}

// get deployment info
func (r *SparkHistoryServerReconciler) getDeploymentInfo(instance *stackv1alpha1.SparkHistoryServer,
	roleGroup *stackv1alpha1.RoleConfigSpec) (*stackv1alpha1.ImageSpec, *corev1.PodSecurityContext, int32,
	*corev1.ResourceRequirements, string) {
	var (
		image           = instance.Spec.RoleConfig.Image
		securityContext = instance.Spec.RoleConfig.SecurityContext
		replicas        = instance.Spec.RoleConfig.Replicas
		resources       = instance.Spec.RoleConfig.Config.Resources
		mountPath       = "/tmp/spark-events"
	)
	if roleGroup != nil {
		if rgImage := roleGroup.Image; rgImage != nil {
			image = rgImage
		}
		if rgSecurityContext := roleGroup.SecurityContext; rgSecurityContext != nil {
			securityContext = rgSecurityContext
		}
		if rgReplicas := roleGroup.Replicas; rgReplicas != 0 {
			replicas = rgReplicas
		}
		if rgResources := roleGroup.Config.Resources; rgResources != nil {
			resources = rgResources
		}
		if rgEvtLog := roleGroup.EventLog; rgEvtLog != nil {
			if rgMountPath := rgEvtLog.Dir; rgMountPath != "" {
				mountPath = rgMountPath
			}
		}
	}
	return image, securityContext, replicas, resources, mountPath
}

// get ingress info
func (r *SparkHistoryServerReconciler) getIngressInfo(instance *stackv1alpha1.SparkHistoryServer,
	roleGroup *stackv1alpha1.RoleConfigSpec) (string, int32) {
	var (
		host = instance.Spec.RoleConfig.Ingress.Host
		port = instance.Spec.RoleConfig.Service.Port
	)
	if roleGroup != nil {
		if rgHost := roleGroup.Ingress.Host; rgHost != "" {
			host = rgHost
		}
		if rgPort := roleGroup.Service.Port; rgPort != 0 {
			port = rgPort
		}
	}
	return host, port
}

// get configMap info
func (r *SparkHistoryServerReconciler) getConfigMapInfo(instance *stackv1alpha1.SparkHistoryServer,
	roleGroup *stackv1alpha1.RoleConfigSpec) (*stackv1alpha1.EventLogSpec, *stackv1alpha1.S3Spec,
	*stackv1alpha1.HistorySpec) {
	var (
		evtLog  = instance.Spec.RoleConfig.EventLog
		s3Cfg   = instance.Spec.RoleConfig.Config.S3
		history = instance.Spec.RoleConfig.History
	)
	if roleGroup != nil {
		if rgEvtLog := roleGroup.EventLog; rgEvtLog != nil {
			evtLog = rgEvtLog
		}
		if rgS3Cfg := roleGroup.Config.S3; rgS3Cfg != nil {
			s3Cfg = rgS3Cfg
		}
		if rgHistory := roleGroup.History; rgHistory != nil {
			history = rgHistory
		}
	}
	return evtLog, s3Cfg, history
}

// get s3 info
func (r *SparkHistoryServerReconciler) getS3Info(instance *stackv1alpha1.SparkHistoryServer,
	roleGroup *stackv1alpha1.RoleConfigSpec) *stackv1alpha1.S3Spec {
	var s3Cfg = instance.Spec.RoleConfig.Config.S3
	if roleGroup != nil {
		if rgS3Cfg := roleGroup.Config.S3; rgS3Cfg != nil {
			s3Cfg = rgS3Cfg
		}
	}
	return s3Cfg
}
