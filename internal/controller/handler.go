package controller

import (
	"context"
	"fmt"
	"github.com/zncdata-labs/operator-go/pkg/utils"

	"github.com/zncdata-labs/operator-go/pkg/errors"
	"github.com/zncdata-labs/operator-go/pkg/status"

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

func (r *SparkHistoryServerReconciler) makePVC(instance *stackv1alpha1.SparkHistoryServer, schema *runtime.Scheme) (*corev1.PersistentVolumeClaim, error) {
	labels := instance.GetLabels()

	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      instance.GetPvcName(),
			Namespace: instance.Namespace,
			Labels:    labels,
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			StorageClassName: instance.Spec.Persistence.StorageClassName,
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.PersistentVolumeAccessMode(instance.Spec.Persistence.AccessMode)},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: resource.MustParse(instance.Spec.Persistence.StorageSize),
				},
			},
			VolumeMode: instance.Spec.Persistence.VolumeMode,
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
	pvc, err := r.makePVC(instance, r.Scheme)
	if err != nil {
		return err
	}

	if instance.Spec.Persistence.Enable == true {
		if err := CreateOrUpdate(ctx, r.Client, pvc); err != nil {
			r.Log.Error(err, "Failed to create or update pvc")
			return err
		}
	}
	return nil
}

func (r *SparkHistoryServerReconciler) makeIngress(instance *stackv1alpha1.SparkHistoryServer, schema *runtime.Scheme) (*v1.Ingress, error) {
	labels := instance.GetLabels()

	pt := v1.PathTypeImplementationSpecific

	ing := &v1.Ingress{
		ObjectMeta: metav1.ObjectMeta{
			Name:      instance.Name,
			Namespace: instance.Namespace,
			Labels:    labels,
		},
		Spec: v1.IngressSpec{
			Rules: []v1.IngressRule{
				{
					Host: instance.Spec.Ingress.Host,
					IngressRuleValue: v1.IngressRuleValue{
						HTTP: &v1.HTTPIngressRuleValue{
							Paths: []v1.HTTPIngressPath{
								{
									Path:     "/",
									PathType: &pt,
									Backend: v1.IngressBackend{
										Service: &v1.IngressServiceBackend{
											Name: instance.GetName(),
											Port: v1.ServiceBackendPort{
												Number: instance.Spec.Service.Port,
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
	obj, err := r.makeIngress(instance, r.Scheme)
	if err != nil {
		return err
	}

	if err := CreateOrUpdate(ctx, r.Client, obj); err != nil {
		r.Log.Error(err, "Failed to create or update ingress")
		return err
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

func (r *SparkHistoryServerReconciler) makeService(instance *stackv1alpha1.SparkHistoryServer, schema *runtime.Scheme) (*corev1.Service, error) {
	labels := instance.GetLabels()
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:        instance.Name,
			Namespace:   instance.Namespace,
			Labels:      labels,
			Annotations: instance.Spec.Service.Annotations,
		},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{
				{
					Port:     instance.Spec.Service.Port,
					Name:     "http",
					Protocol: "TCP",
				},
			},
			Selector: labels,
			Type:     instance.Spec.Service.Type,
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
	obj, err := r.makeService(instance, r.Scheme)
	if err != nil {
		return err
	}

	if err := CreateOrUpdate(ctx, r.Client, obj); err != nil {
		r.Log.Error(err, "Failed to create or update service")
		return err
	}
	return nil
}

func (r *SparkHistoryServerReconciler) makeDeployments(instance *stackv1alpha1.SparkHistoryServer) []*appsv1.Deployment {
	var deployments []*appsv1.Deployment

	if instance.Spec.RoleGroups != nil {
		for roleGroupName, roleGroup := range instance.Spec.RoleGroups {
			if roleGroup != nil {
				if len(instance.Spec.Selectors) == 0 {
					dep := r.makeDefaultDeploymentForRoleGroup(instance, roleGroupName, roleGroup, r.Scheme)
					if dep != nil {
						deployments = append(deployments, dep)
					}
				} else {
					for _, selectors := range instance.Spec.Selectors {
						dep := r.makeDeploymentForRoleGroup(instance, roleGroupName, roleGroup, selectors, r.Scheme)
						if dep != nil {
							deployments = append(deployments, dep)
						}
					}
				}
			}
		}
	}

	return deployments
}

func (r *SparkHistoryServerReconciler) makeDeploymentForRoleGroup(instance *stackv1alpha1.SparkHistoryServer, roleGroupName string, roleGroup *stackv1alpha1.RoleGroupSpec, selectors *stackv1alpha1.SelectorSpec, schema *runtime.Scheme) *appsv1.Deployment {
	labels := instance.GetLabels()

	additionalLabels := make(map[string]string)

	if instance.Spec.Selectors != nil {
		for _, selectorSpec := range instance.Spec.Selectors {
			if selectorSpec != nil && selectorSpec.Selector.MatchLabels != nil {
				for k, v := range selectorSpec.Selector.MatchLabels {
					additionalLabels[k] = v
				}
			}
		}
	}

	mergedLabels := make(map[string]string)
	for key, value := range labels {
		mergedLabels[key] = value
	}
	for key, value := range additionalLabels {
		mergedLabels[key] = value
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
					SecurityContext: instance.Spec.SecurityContext,
					Containers: []corev1.Container{
						{
							Name:            instance.Name,
							Image:           instance.Spec.Image.Repository + ":" + instance.Spec.Image.Tag,
							ImagePullPolicy: instance.Spec.Image.PullPolicy,
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

	if &selectors.NodeSelector != nil {
		dep.Spec.Template.Spec.NodeSelector = selectors.NodeSelector
	}

	SparkHistoryServerScheduler(instance, dep, roleGroup)

	mountPath := "/tmp/spark-events"
	if instance.Spec.RoleConfig != nil && instance.Spec.RoleConfig.EventLog != nil && instance.Spec.RoleConfig.EventLog.Dir != "" {
		mountPath = instance.Spec.RoleConfig.EventLog.Dir
	}

	if instance.Spec.Persistence.Enable == true {
		dep.Spec.Template.Spec.Volumes = append(dep.Spec.Template.Spec.Volumes, corev1.Volume{
			Name: instance.GetNameWithSuffix("data"),
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
					ClaimName: instance.GetPvcName(),
				},
			},
		})
		dep.Spec.Template.Spec.Containers[0].VolumeMounts = append(dep.Spec.Template.Spec.Containers[0].VolumeMounts, corev1.VolumeMount{
			Name:      instance.GetNameWithSuffix("data"),
			MountPath: mountPath,
		})
	} else {
		dep.Spec.Template.Spec.Volumes = append(dep.Spec.Template.Spec.Volumes, corev1.Volume{
			Name: instance.GetNameWithSuffix("data"),
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		})
	}

	if instance.Spec.RoleConfig != nil && instance.Spec.RoleConfig.EventLog.Enabled == true {
		dep.Spec.Template.Spec.Volumes = append(dep.Spec.Template.Spec.Volumes, corev1.Volume{
			Name: instance.GetNameWithSuffix("conf"),
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: instance.GetNameWithSuffix("conf"),
					},
				},
			},
		})
		dep.Spec.Template.Spec.Containers[0].VolumeMounts = append(dep.Spec.Template.Spec.Containers[0].VolumeMounts, corev1.VolumeMount{
			Name:      instance.GetNameWithSuffix("conf"),
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

func (r *SparkHistoryServerReconciler) makeDefaultDeploymentForRoleGroup(instance *stackv1alpha1.SparkHistoryServer, roleGroupName string, roleGroup *stackv1alpha1.RoleGroupSpec, schema *runtime.Scheme) *appsv1.Deployment {
	labels := instance.GetLabels()

	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      instance.GetNameWithSuffix(roleGroupName),
			Namespace: instance.Namespace,
			Labels:    labels,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &roleGroup.Replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
				Spec: corev1.PodSpec{
					SecurityContext: instance.Spec.SecurityContext,
					Containers: []corev1.Container{
						{
							Name:            instance.Name,
							Image:           instance.Spec.Image.Repository + ":" + instance.Spec.Image.Tag,
							ImagePullPolicy: instance.Spec.Image.PullPolicy,
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

	SparkHistoryServerScheduler(instance, dep, roleGroup)

	mountPath := "/tmp/spark-events"
	if instance.Spec.RoleConfig != nil && instance.Spec.RoleConfig.EventLog != nil && instance.Spec.RoleConfig.EventLog.Dir != "" {
		mountPath = instance.Spec.RoleConfig.EventLog.Dir
	}

	if instance.Spec.Persistence.Enable == true {
		dep.Spec.Template.Spec.Volumes = append(dep.Spec.Template.Spec.Volumes, corev1.Volume{
			Name: instance.GetNameWithSuffix("data"),
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
					ClaimName: instance.GetPvcName(),
				},
			},
		})
		dep.Spec.Template.Spec.Containers[0].VolumeMounts = append(dep.Spec.Template.Spec.Containers[0].VolumeMounts, corev1.VolumeMount{
			Name:      instance.GetNameWithSuffix("data"),
			MountPath: mountPath,
		})
	} else {
		dep.Spec.Template.Spec.Volumes = append(dep.Spec.Template.Spec.Volumes, corev1.Volume{
			Name: instance.GetNameWithSuffix("data"),
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		})
	}

	if instance.Spec.RoleConfig != nil && instance.Spec.RoleConfig.EventLog.Enabled == true {
		dep.Spec.Template.Spec.Volumes = append(dep.Spec.Template.Spec.Volumes, corev1.Volume{
			Name: instance.GetNameWithSuffix("conf"),
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: instance.GetNameWithSuffix("conf"),
					},
				},
			},
		})
		dep.Spec.Template.Spec.Containers[0].VolumeMounts = append(dep.Spec.Template.Spec.Containers[0].VolumeMounts, corev1.VolumeMount{
			Name:      instance.GetNameWithSuffix("conf"),
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

func (r *SparkHistoryServerReconciler) makeConfigMap(instance *stackv1alpha1.SparkHistoryServer, schema *runtime.Scheme) (*corev1.ConfigMap, error) {
	labels := instance.GetLabels()

	if instance.Spec.RoleConfig == nil {
		return nil, nil
	}

	eventLogDir := ""
	sparkDefaultsConf := ""
	if instance.Spec.RoleConfig.EventLog != nil {
		eventLogDir = instance.Spec.RoleConfig.EventLog.Dir
		if instance.Spec.RoleConfig.EventLog.MountMode == "pvc" {
			eventLogDir = "file://" + eventLogDir
		} else if instance.Spec.RoleConfig.EventLog.MountMode == "s3" {
			eventLogDir = "s3a:/" + eventLogDir
			sparkDefaultsConf += "spark.hadoop.fs.s3a.endpoint" + " " + instance.Spec.RoleConfig.S3.Endpoint + "\n" +
				"spark.hadoop.fs.s3a.ssl.enabled" + " " + strconv.FormatBool(instance.Spec.RoleConfig.S3.EnableSSL) + "\n" +
				"spark.hadoop.fs.s3a.impl" + " " + instance.Spec.RoleConfig.S3.Impl + "\n" +
				"spark.hadoop.fs.s3a.fast.upload" + " " + strconv.FormatBool(instance.Spec.RoleConfig.S3.FastUpload) + "\n" +
				"spark.hadoop.fs.s3a.access.key" + " " + instance.Spec.RoleConfig.S3.AccessKey + "\n" +
				"spark.hadoop.fs.s3a.secret.key" + " " + instance.Spec.RoleConfig.S3.SecretKey + "\n" +
				"spark.hadoop.fs.s3a.path.style.access" + " " + strconv.FormatBool(instance.Spec.RoleConfig.S3.PathStyleAccess) + "\n"
		}
	}

	if instance.Spec.RoleConfig.EventLog != nil {
		sparkDefaultsConf += "spark.eventLog.enabled" + " " + strconv.FormatBool(instance.Spec.RoleConfig.EventLog.Enabled) + "\n" +
			"spark.eventLog.dir" + " " + eventLogDir + "\n" +
			"spark.history.fs.logDirectory" + " " + eventLogDir + "\n"
	}

	if instance.Spec.RoleConfig.History != nil {
		sparkDefaultsConf += "spark.history.fs.cleaner.enabled" + " " + strconv.FormatBool(instance.Spec.RoleConfig.History.FsCleaner.Enabled) + "\n" +
			"spark.history.fs.cleaner.maxNum" + " " + strconv.Itoa(int(instance.Spec.RoleConfig.History.FsCleaner.MaxNum)) + "\n" +
			"spark.history.fs.cleaner.maxAge" + " " + instance.Spec.RoleConfig.History.FsCleaner.MaxAge + "\n" +
			"spark.history.fs.eventLog.rolling.maxFilesToRetain" + " " + strconv.Itoa(int(instance.Spec.RoleConfig.History.FsEnentLogRollingMaxFiles)) + "\n"
	}

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      instance.GetNameWithSuffix("conf"),
			Namespace: instance.Namespace,
			Labels:    labels,
		},
		Data: map[string]string{
			"spark-defaults.conf": sparkDefaultsConf,
		},
	}

	err := ctrl.SetControllerReference(instance, cm, schema)
	if err != nil {
		r.Log.Error(err, "Failed to set controller reference for configmap")
		return nil, errors.Wrap(err, "Failed to set controller reference for configmap")
	}
	return cm, nil
}

func (r *SparkHistoryServerReconciler) reconcileConfigMap(ctx context.Context, instance *stackv1alpha1.SparkHistoryServer) error {
	obj, err := r.makeConfigMap(instance, r.Scheme)
	if err != nil {
		return err
	}

	if instance.Spec.RoleConfig != nil {
		if err := CreateOrUpdate(ctx, r.Client, obj); err != nil {
			r.Log.Error(err, "Failed to create or update configmap")
			return err
		}
	}
	return nil
}
