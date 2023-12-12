package controller

import (
	"context"
	"fmt"

	stackv1alpha1 "github.com/zncdata-labs/spark-k8s-operator/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
)

func (r *SparkHistoryServerReconciler) makePVC(instance *stackv1alpha1.SparkHistoryServer, schema *runtime.Scheme) *corev1.PersistentVolumeClaim {
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
			Resources: corev1.ResourceRequirements{
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
		return nil
	}
	return pvc
}

func (r *SparkHistoryServerReconciler) reconcilePVC(ctx context.Context, instance *stackv1alpha1.SparkHistoryServer) error {
	if instance.Spec.Persistence.Enable == false {
		return nil
	}

	pvc := &corev1.PersistentVolumeClaim{}
	err := r.Client.Get(ctx, types.NamespacedName{Namespace: instance.Namespace, Name: instance.GetPvcName()}, pvc)
	if err != nil && errors.IsNotFound(err) {
		pvc := r.makePVC(instance, r.Scheme)
		r.Log.Info("Creating a new PVC", "PVC.Namespace", pvc.Namespace, "PVC.Name", pvc.Name)
		err := r.Client.Create(ctx, pvc)
		if err != nil {
			return err
		}
	} else if err != nil {
		r.Log.Error(err, "Failed to get PVC")
		return err
	}
	return nil
}

func (r *SparkHistoryServerReconciler) makeIngress(instance *stackv1alpha1.SparkHistoryServer, schema *runtime.Scheme) *v1.Ingress {
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
		return nil
	}
	return ing
}

func (r *SparkHistoryServerReconciler) reconcileIngress(ctx context.Context, instance *stackv1alpha1.SparkHistoryServer) error {
	obj := r.makeIngress(instance, r.Scheme)
	if obj == nil {
		return nil
	}

	if err := CreateOrUpdate(ctx, r.Client, obj); err != nil {
		r.Log.Error(err, "Failed to create or update ingress")
		return err
	}

	if instance.Spec.Ingress.Enabled {
		url := fmt.Sprintf("http://%s", instance.Spec.Ingress.Host)
		if instance.Status.URLs == nil {
			instance.Status.URLs = []stackv1alpha1.StatusURL{
				{
					Name: "webui",
					URL:  url,
				},
			}
			if err := r.UpdateStatus(ctx, instance); err != nil {
				return err
			}

		} else if instance.Spec.Ingress.Host != instance.Status.URLs[0].Name {
			instance.Status.URLs[0].URL = url
			if err := r.UpdateStatus(ctx, instance); err != nil {
				return err
			}

		}
	}

	return nil
}

func (r *SparkHistoryServerReconciler) makeService(instance *stackv1alpha1.SparkHistoryServer, schema *runtime.Scheme) *corev1.Service {
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
		return nil
	}
	return svc
}

func (r *SparkHistoryServerReconciler) reconcileService(ctx context.Context, instance *stackv1alpha1.SparkHistoryServer) error {
	obj := r.makeService(instance, r.Scheme)
	if obj == nil {
		return nil
	}

	if err := CreateOrUpdate(ctx, r.Client, obj); err != nil {
		r.Log.Error(err, "Failed to create or update service")
		return err
	}
	return nil
}

func (r *SparkHistoryServerReconciler) makeDeployment(instance *stackv1alpha1.SparkHistoryServer, schema *runtime.Scheme) *appsv1.Deployment {
	labels := instance.GetLabels()

	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      instance.Name,
			Namespace: instance.Namespace,
			Labels:    labels,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &instance.Spec.Replicas,
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
							Args: []string{
								"/opt/bitnami/spark/sbin/start-history-server.sh",
							},
							Resources: *instance.Spec.Resources,
							Ports: []corev1.ContainerPort{
								{
									ContainerPort: 18080,
									Name:          "http",
									Protocol:      "TCP",
								},
							},
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      instance.GetNameWithSuffix("-data"),
									MountPath: "/tmp/spark-events",
								},
							},
						},
					},
				},
			},
		},
	}

	SparkHistoryServerScheduler(instance, dep)

	if instance.Spec.Persistence.Enable == true {
		dep.Spec.Template.Spec.Volumes = append(dep.Spec.Template.Spec.Volumes, corev1.Volume{
			Name: instance.GetNameWithSuffix("-data"),
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
					ClaimName: instance.GetPvcName(),
				},
			},
		})
	} else {
		dep.Spec.Template.Spec.Volumes = append(dep.Spec.Template.Spec.Volumes, corev1.Volume{
			Name: instance.GetNameWithSuffix("-data"),
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
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

	if err := r.UpdateStatus(ctx, instance); err != nil {
		return err
	}
	return nil
}

func (r *SparkHistoryServerReconciler) reconcileDeployment(ctx context.Context, instance *stackv1alpha1.SparkHistoryServer) error {
	obj := r.makeDeployment(instance, r.Scheme)
	if obj == nil {
		return nil
	}
	if err := CreateOrUpdate(ctx, r.Client, obj); err != nil {
		logger.Error(err, "Failed to create or update deployment")
		return err
	}

	return nil
}
