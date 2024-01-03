package controller

import (
	"context"
	"fmt"
	"github.com/zncdata-labs/operator-go/pkg/errors"
	"github.com/zncdata-labs/operator-go/pkg/status"
	"github.com/zncdata-labs/operator-go/pkg/utils"

	stackv1alpha1 "github.com/zncdata-labs/spark-k8s-operator/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"strconv"
)

func (r *SparkHistoryServerReconciler) makePVCs(instance *stackv1alpha1.SparkHistoryServer) ([]*corev1.PersistentVolumeClaim, error) {
	var pvcs []*corev1.PersistentVolumeClaim

	if instance.Spec.RoleGroups != nil {
		for roleGroupName, roleGroup := range instance.Spec.RoleGroups {
			pvc, err := r.makePVCForRoleGroup(instance, roleGroupName, roleGroup, r.Scheme)
			if err != nil {
				return nil, err
			}
			if pvc != nil {
				pvcs = append(pvcs, pvc)
			}
		}
	}

	return pvcs, nil
}

func (r *SparkHistoryServerReconciler) makePVCForRoleGroup(instance *stackv1alpha1.SparkHistoryServer, roleGroupName string, roleGroup *stackv1alpha1.RoleGroupSpec, schema *runtime.Scheme) (*corev1.PersistentVolumeClaim, error) {
	labels := instance.GetLabels()

	additionalLabels := make(map[string]string)

	if roleGroup.Config != nil && roleGroup.Config.MatchLabels != nil {
		for k, v := range roleGroup.Config.MatchLabels {
			additionalLabels[k] = v
		}
	}

	mergedLabels := make(map[string]string)
	for key, value := range labels {
		mergedLabels[key] = value
	}
	for key, value := range additionalLabels {
		mergedLabels[key] = value
	}

	var storageClassName *string
	var accessMode corev1.PersistentVolumeAccessMode
	var storageSize string
	var volumeMode *corev1.PersistentVolumeMode

	if roleGroup != nil && roleGroup.Config != nil && roleGroup.Config.Persistence != nil {
		if !roleGroup.Config.Persistence.Enable {
			return nil, nil
		}
		storageClassName = roleGroup.Config.Persistence.StorageClassName
		accessMode = corev1.PersistentVolumeAccessMode(roleGroup.Config.Persistence.AccessMode)
		storageSize = roleGroup.Config.Persistence.StorageSize
		volumeMode = roleGroup.Config.Persistence.VolumeMode
	} else {
		if !instance.Spec.Persistence.Enable {
			return nil, nil
		}
		storageClassName = instance.Spec.Persistence.StorageClassName
		accessMode = corev1.PersistentVolumeAccessMode(instance.Spec.Persistence.AccessMode)
		storageSize = instance.Spec.Persistence.StorageSize
		volumeMode = instance.Spec.Persistence.VolumeMode
	}

	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      instance.GetNameWithSuffix(roleGroupName),
			Namespace: instance.Namespace,
			Labels:    mergedLabels,
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			StorageClassName: storageClassName,
			AccessModes:      []corev1.PersistentVolumeAccessMode{accessMode},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: resource.MustParse(storageSize),
				},
			},
			VolumeMode: volumeMode,
		},
	}

	err := ctrl.SetControllerReference(instance, pvc, schema)
	if err != nil {
		r.Log.Error(err, "Failed to set controller reference for pvc")
		return nil, errors.Wrap(err, "Failed to set controller reference for pvc")
	}
	return pvc, nil
}

func (r *SparkHistoryServerReconciler) reconcilePVC(ctx context.Context, instance *stackv1alpha1.SparkHistoryServer) error {
	obj, err := r.makePVCs(instance)
	if err != nil {
		return err
	}

	for _, pvc := range obj {
		if err := CreateOrUpdate(ctx, r.Client, pvc); err != nil {
			r.Log.Error(err, "Failed to create or update pvc")
			return err
		}
	}
	return nil
}

func (r *SparkHistoryServerReconciler) makeIngress(instance *stackv1alpha1.SparkHistoryServer) ([]*v1.Ingress, error) {
	var ing []*v1.Ingress

	if instance.Spec.RoleGroups != nil {
		for roleGroupName, roleGroup := range instance.Spec.RoleGroups {
			i, err := r.makeIngressForRoleGroup(instance, roleGroupName, roleGroup, r.Scheme)
			if err != nil {
				return nil, err
			}
			ing = append(ing, i)
		}
	}
	return ing, nil
}

func (r *SparkHistoryServerReconciler) makeIngressForRoleGroup(instance *stackv1alpha1.SparkHistoryServer, roleGroupName string, roleGroup *stackv1alpha1.RoleGroupSpec, schema *runtime.Scheme) (*v1.Ingress, error) {
	labels := instance.GetLabels()

	additionalLabels := make(map[string]string)

	if roleGroup.Config != nil && roleGroup.Config.MatchLabels != nil {
		for k, v := range roleGroup.Config.MatchLabels {
			additionalLabels[k] = v
		}
	}

	mergedLabels := make(map[string]string)
	for key, value := range labels {
		mergedLabels[key] = value
	}
	for key, value := range additionalLabels {
		mergedLabels[key] = value
	}

	pt := v1.PathTypeImplementationSpecific

	var host string
	var port int32

	if roleGroup != nil && roleGroup.Config != nil && roleGroup.Config.Ingress != nil {
		if !roleGroup.Config.Ingress.Enabled {
			return nil, nil
		}
		host = roleGroup.Config.Ingress.Host
		port = roleGroup.Config.Service.Port
	} else {
		if !instance.Spec.Ingress.Enabled {
			return nil, nil
		}
		host = instance.Spec.Ingress.Host
		port = instance.Spec.Service.Port
	}

	ing := &v1.Ingress{
		ObjectMeta: metav1.ObjectMeta{
			Name:      instance.GetNameWithSuffix(roleGroupName),
			Namespace: instance.Namespace,
			Labels:    mergedLabels,
		},
		Spec: v1.IngressSpec{
			Rules: []v1.IngressRule{
				{
					Host: host,
					IngressRuleValue: v1.IngressRuleValue{
						HTTP: &v1.HTTPIngressRuleValue{
							Paths: []v1.HTTPIngressPath{
								{
									Path:     "/",
									PathType: &pt,
									Backend: v1.IngressBackend{
										Service: &v1.IngressServiceBackend{
											Name: instance.GetNameWithSuffix(roleGroupName),
											Port: v1.ServiceBackendPort{
												Number: port,
											},
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}

	err := ctrl.SetControllerReference(instance, ing, schema)
	if err != nil {
		r.Log.Error(err, "Failed to set controller reference for ingress")
		return nil, errors.Wrap(err, "Failed to set controller reference for ingress")
	}
	return ing, nil
}

func (r *SparkHistoryServerReconciler) reconcileIngress(ctx context.Context, instance *stackv1alpha1.SparkHistoryServer) error {
	obj, err := r.makeIngress(instance)
	if err != nil {
		return err
	}
	if r != nil && r.Client != nil {
		for _, ingress := range obj {
			if ingress != nil {
				if err := CreateOrUpdate(ctx, r.Client, ingress); err != nil {
					r.Log.Error(err, "Failed to create or update ingress")
					return err
				}
			}
		}
	}
	if instance.Spec.Ingress.Enabled {
		url := fmt.Sprintf("http://%s", instance.Spec.Ingress.Host)
		if instance.Status.URLs == nil {
			instance.Status.URLs = []status.URL{
				{
					Name: "webui",
					URL:  url,
				},
			}
			if err := utils.UpdateStatus(ctx, r.Client, instance); err != nil {
				return err
			}

		} else if instance.Spec.Ingress.Host != instance.Status.URLs[0].Name {
			instance.Status.URLs[0].URL = url
			if err := utils.UpdateStatus(ctx, r.Client, instance); err != nil {
				return err
			}

		}
	}

	return nil
}

func (r *SparkHistoryServerReconciler) makeServices(instance *stackv1alpha1.SparkHistoryServer) ([]*corev1.Service, error) {
	var services []*corev1.Service

	if instance.Spec.RoleGroups != nil {
		for roleGroupName, roleGroup := range instance.Spec.RoleGroups {
			svc, err := r.makeServiceForRoleGroup(instance, roleGroupName, roleGroup, r.Scheme)
			if err != nil {
				return nil, err
			}
			services = append(services, svc)
		}
	}

	return services, nil
}

func (r *SparkHistoryServerReconciler) makeServiceForRoleGroup(instance *stackv1alpha1.SparkHistoryServer, roleGroupName string, roleGroup *stackv1alpha1.RoleGroupSpec, schema *runtime.Scheme) (*corev1.Service, error) {
	labels := instance.GetLabels()

	additionalLabels := make(map[string]string)

	if roleGroup.Config != nil && roleGroup.Config.MatchLabels != nil {
		for k, v := range roleGroup.Config.MatchLabels {
			additionalLabels[k] = v
		}
	}

	mergedLabels := make(map[string]string)
	for key, value := range labels {
		mergedLabels[key] = value
	}
	for key, value := range additionalLabels {
		mergedLabels[key] = value
	}

	var port int32
	var serviceType corev1.ServiceType
	var annotations map[string]string

	if roleGroup != nil && roleGroup.Config != nil && roleGroup.Config.Service != nil {
		port = roleGroup.Config.Service.Port
		serviceType = roleGroup.Config.Service.Type
		annotations = roleGroup.Config.Service.Annotations
	} else {
		port = instance.Spec.Service.Port
		serviceType = instance.Spec.Service.Type
		annotations = instance.Spec.Service.Annotations
	}

	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:        instance.GetNameWithSuffix(roleGroupName),
			Namespace:   instance.Namespace,
			Labels:      mergedLabels,
			Annotations: annotations,
		},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{
				{
					Port:     port,
					Name:     "http",
					Protocol: "TCP",
				},
			},
			Selector: mergedLabels,
			Type:     serviceType,
		},
	}
	err := ctrl.SetControllerReference(instance, svc, schema)
	if err != nil {
		r.Log.Error(err, "Failed to set controller reference for service")
		return nil, errors.Wrap(err, "Failed to set controller reference for service")
	}
	return svc, nil
}

func (r *SparkHistoryServerReconciler) reconcileService(ctx context.Context, instance *stackv1alpha1.SparkHistoryServer) error {
	services, err := r.makeServices(instance)
	if err != nil {
		return err
	}

	for _, svc := range services {
		if svc == nil {
			continue
		}

		if err := CreateOrUpdate(ctx, r.Client, svc); err != nil {
			r.Log.Error(err, "Failed to create or update service", "service", svc.Name)
			return err
		}
	}

	return nil
}

func (r *SparkHistoryServerReconciler) makeDeployments(instance *stackv1alpha1.SparkHistoryServer) []*appsv1.Deployment {
	var deployments []*appsv1.Deployment

	if instance.Spec.RoleGroups != nil {
		for roleGroupName, roleGroup := range instance.Spec.RoleGroups {
			dep := r.makeDeploymentForRoleGroup(instance, roleGroupName, roleGroup, r.Scheme)
			if dep != nil {
				deployments = append(deployments, dep)
			}
		}
	}

	return deployments
}

func (r *SparkHistoryServerReconciler) makeDeploymentForRoleGroup(instance *stackv1alpha1.SparkHistoryServer, roleGroupName string, roleGroup *stackv1alpha1.RoleGroupSpec, schema *runtime.Scheme) *appsv1.Deployment {
	labels := instance.GetLabels()

	additionalLabels := make(map[string]string)

	if roleGroup != nil && roleGroup.Config.MatchLabels != nil {
		for k, v := range roleGroup.Config.MatchLabels {
			additionalLabels[k] = v
		}
	}

	mergedLabels := make(map[string]string)
	for key, value := range labels {
		mergedLabels[key] = value
	}
	for key, value := range additionalLabels {
		mergedLabels[key] = value
	}

	var image stackv1alpha1.ImageSpec
	var securityContext *corev1.PodSecurityContext

	if roleGroup != nil && roleGroup.Config != nil && roleGroup.Config.Image != nil {
		image = *roleGroup.Config.Image
	} else {
		image = *instance.Spec.Image
	}

	if roleGroup != nil && roleGroup.Config != nil && roleGroup.Config.SecurityContext != nil {
		securityContext = roleGroup.Config.SecurityContext
	} else {
		securityContext = instance.Spec.SecurityContext
	}

	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      instance.GetNameWithSuffix(roleGroupName),
			Namespace: instance.Namespace,
			Labels:    mergedLabels,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &roleGroup.Replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: mergedLabels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: mergedLabels,
				},
				Spec: corev1.PodSpec{
					SecurityContext: securityContext,
					Containers: []corev1.Container{
						{
							Name:            instance.Name,
							Image:           image.Repository + ":" + image.Tag,
							ImagePullPolicy: image.PullPolicy,
							Resources:       *roleGroup.Config.Resources,
							Args: []string{
								"/opt/bitnami/spark/sbin/start-history-server.sh",
							},
							Ports: []corev1.ContainerPort{
								{
									ContainerPort: 18080,
									Name:          "http",
									Protocol:      "TCP",
								},
							},
						},
					},
				},
			},
		},
	}

	SparkHistoryServerScheduler(dep, roleGroup)

	mountPath := "/tmp/spark-events"
	if instance.Spec.RoleConfig != nil && instance.Spec.RoleConfig.EventLog != nil && instance.Spec.RoleConfig.EventLog.Dir != "" {
		mountPath = instance.Spec.RoleConfig.EventLog.Dir
	}

	if roleGroup.Config != nil && roleGroup.Config.Persistence != nil {
		if roleGroup.Config.Persistence.Enable {
			dep.Spec.Template.Spec.Volumes = append(dep.Spec.Template.Spec.Volumes, corev1.Volume{
				Name: instance.GetNameWithSuffix("data"),
				VolumeSource: corev1.VolumeSource{
					PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
						ClaimName: instance.GetNameWithSuffix(roleGroupName),
					},
				},
			})
			dep.Spec.Template.Spec.Containers[0].VolumeMounts = append(dep.Spec.Template.Spec.Containers[0].VolumeMounts, corev1.VolumeMount{
				Name:      instance.GetNameWithSuffix("data"),
				MountPath: mountPath,
			})
		}

	} else if instance.Spec.Persistence.Enable {
		dep.Spec.Template.Spec.Volumes = append(dep.Spec.Template.Spec.Volumes, corev1.Volume{
			Name: instance.GetNameWithSuffix("data"),
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
					ClaimName: instance.GetNameWithSuffix(roleGroupName),
				},
			},
		})
		dep.Spec.Template.Spec.Containers[0].VolumeMounts = append(dep.Spec.Template.Spec.Containers[0].VolumeMounts, corev1.VolumeMount{
			Name:      instance.GetNameWithSuffix("data"),
			MountPath: mountPath,
		})
	}

	if instance.Spec.RoleConfig != nil && instance.Spec.RoleConfig.EventLog.Enabled == true {
		dep.Spec.Template.Spec.Volumes = append(dep.Spec.Template.Spec.Volumes, corev1.Volume{
			Name: instance.GetNameWithSuffix("conf" + "-" + roleGroupName),
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: instance.GetNameWithSuffix("conf" + "-" + roleGroupName),
					},
				},
			},
		})
		dep.Spec.Template.Spec.Containers[0].VolumeMounts = append(dep.Spec.Template.Spec.Containers[0].VolumeMounts, corev1.VolumeMount{
			Name:      instance.GetNameWithSuffix("conf" + "-" + roleGroupName),
			MountPath: "/opt/bitnami/spark/conf/spark-defaults.conf",
			SubPath:   "spark-defaults.conf",
		})
	}

	err := ctrl.SetControllerReference(instance, dep, schema)
	if err != nil {
		r.Log.Error(err, "Failed to set controller reference for deployment")
		return nil
	}
	return dep
}

func (r *SparkHistoryServerReconciler) updateStatusConditionWithDeployment(ctx context.Context, instance *stackv1alpha1.SparkHistoryServer, status metav1.ConditionStatus, message string) error {
	instance.SetStatusCondition(metav1.Condition{
		Type:               stackv1alpha1.ConditionTypeProgressing,
		Status:             status,
		Reason:             stackv1alpha1.ConditionReasonReconcileDeployment,
		Message:            message,
		ObservedGeneration: instance.GetGeneration(),
		LastTransitionTime: metav1.Now(),
	})

	if err := utils.UpdateStatus(ctx, r.Client, instance); err != nil {
		return err
	}
	return nil
}

func (r *SparkHistoryServerReconciler) reconcileDeployment(ctx context.Context, instance *stackv1alpha1.SparkHistoryServer) error {
	Deployments := r.makeDeployments(instance)
	for _, dep := range Deployments {
		if dep == nil {
			continue
		}

		if err := CreateOrUpdate(ctx, r.Client, dep); err != nil {
			r.Log.Error(err, "Failed to create or update Deployment", "deployment", dep.Name)
			return err
		}
	}

	return nil
}

func (r *SparkHistoryServerReconciler) makeConfigMaps(instance *stackv1alpha1.SparkHistoryServer, schema *runtime.Scheme) ([]*corev1.ConfigMap, error) {
	var configMaps []*corev1.ConfigMap

	if instance.Spec.RoleGroups != nil {
		for roleGroupName, roleGroup := range instance.Spec.RoleGroups {
			cm := r.makeConfigMaForRoleGroup(instance, roleGroupName, roleGroup, schema)
			if cm != nil {
				configMaps = append(configMaps, cm)
			}
		}
	}

	return configMaps, nil
}

func (r *SparkHistoryServerReconciler) makeConfigMaForRoleGroup(instance *stackv1alpha1.SparkHistoryServer, roleGroupName string, roleGroup *stackv1alpha1.RoleGroupSpec, schema *runtime.Scheme) *corev1.ConfigMap {
	labels := instance.GetLabels()

	additionalLabels := make(map[string]string)

	if roleGroup.Config != nil && roleGroup.Config.MatchLabels != nil {
		for k, v := range roleGroup.Config.MatchLabels {
			additionalLabels[k] = v
		}
	}

	mergedLabels := make(map[string]string)
	for key, value := range labels {
		mergedLabels[key] = value
	}
	for key, value := range additionalLabels {
		mergedLabels[key] = value
	}

	if instance.Spec.RoleConfig == nil {
		return nil
	}

	eventLogDir := ""
	sparkDefaultsConf := ""
	var eventLog *stackv1alpha1.EventLogSpec
	var s3Config *stackv1alpha1.S3Spec
	var history *stackv1alpha1.HistorySpec

	if roleGroup != nil && roleGroup.Config != nil && roleGroup.Config.EventLog != nil {
		eventLog = roleGroup.Config.EventLog
		s3Config = roleGroup.Config.S3
		history = roleGroup.Config.History
	} else {
		eventLog = instance.Spec.RoleConfig.EventLog
		s3Config = instance.Spec.RoleConfig.S3
		history = instance.Spec.RoleConfig.History
	}

	if eventLog != nil {
		eventLogDir = eventLog.Dir
		if eventLog.MountMode == "pvc" {
			eventLogDir = "file://" + eventLogDir
		} else if eventLog.MountMode == "s3" {
			eventLogDir = "s3a:/" + eventLogDir
			sparkDefaultsConf += "spark.hadoop.fs.s3a.endpoint" + " " + s3Config.Endpoint + "\n" +
				"spark.hadoop.fs.s3a.ssl.enabled" + " " + strconv.FormatBool(s3Config.EnableSSL) + "\n" +
				"spark.hadoop.fs.s3a.impl" + " " + s3Config.Impl + "\n" +
				"spark.hadoop.fs.s3a.fast.upload" + " " + strconv.FormatBool(s3Config.FastUpload) + "\n" +
				"spark.hadoop.fs.s3a.access.key" + " " + s3Config.AccessKey + "\n" +
				"spark.hadoop.fs.s3a.secret.key" + " " + s3Config.SecretKey + "\n" +
				"spark.hadoop.fs.s3a.path.style.access" + " " + strconv.FormatBool(s3Config.PathStyleAccess) + "\n"
		}
	}

	if eventLog != nil {
		sparkDefaultsConf += "spark.eventLog.enabled" + " " + strconv.FormatBool(eventLog.Enabled) + "\n" +
			"spark.eventLog.dir" + " " + eventLogDir + "\n" +
			"spark.history.fs.logDirectory" + " " + eventLogDir + "\n"
	}

	if history != nil {
		sparkDefaultsConf += "spark.history.fs.cleaner.enabled" + " " + strconv.FormatBool(history.FsCleaner.Enabled) + "\n" +
			"spark.history.fs.cleaner.maxNum" + " " + strconv.Itoa(int(history.FsCleaner.MaxNum)) + "\n" +
			"spark.history.fs.cleaner.maxAge" + " " + history.FsCleaner.MaxAge + "\n" +
			"spark.history.fs.eventLog.rolling.maxFilesToRetain" + " " + strconv.Itoa(int(history.FsEnentLogRollingMaxFiles)) + "\n"
	}

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      instance.GetNameWithSuffix("conf" + "-" + roleGroupName),
			Namespace: instance.Namespace,
			Labels:    mergedLabels,
		},
		Data: map[string]string{
			"spark-defaults.conf": sparkDefaultsConf,
		},
	}

	err := ctrl.SetControllerReference(instance, cm, schema)
	if err != nil {
		r.Log.Error(err, "Failed to set controller reference for configmap")
		return nil
	}
	return cm
}

func (r *SparkHistoryServerReconciler) reconcileConfigMap(ctx context.Context, instance *stackv1alpha1.SparkHistoryServer) error {
	configMaps, err := r.makeConfigMaps(instance, r.Scheme)
	if err != nil {
		return err
	}

	for _, cm := range configMaps {
		if cm == nil {
			continue
		}

		if err := CreateOrUpdate(ctx, r.Client, cm); err != nil {
			r.Log.Error(err, "Failed to create or update configmap", "configmap", cm.Name)
			return err
		}
	}

	return nil
}
