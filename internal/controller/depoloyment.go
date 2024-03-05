package controller

import (
	"context"
	sparkv1alpha1 "github.com/zncdata-labs/spark-k8s-operator/api/v1alpha1"
	"github.com/zncdata-labs/spark-k8s-operator/internal/common"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type DeploymentReconciler struct {
	common.DeploymentStyleReconciler[*sparkv1alpha1.SparkHistoryServer, *sparkv1alpha1.RoleGroupSpec]
}

// NewDeployment new a DeploymentReconcile
func NewDeployment(
	scheme *runtime.Scheme,
	instance *sparkv1alpha1.SparkHistoryServer,
	client client.Client,
	groupName string,
	mergedLabels map[string]string,
	mergedCfg *sparkv1alpha1.RoleGroupSpec,
	replicates int32,

) *DeploymentReconciler {
	return &DeploymentReconciler{
		DeploymentStyleReconciler: *common.NewDeploymentStyleReconciler[*sparkv1alpha1.SparkHistoryServer,
			*sparkv1alpha1.RoleGroupSpec](
			scheme,
			instance,
			client,
			groupName,
			mergedLabels,
			mergedCfg,
			replicates,
		),
	}
}

// GetConditions implement the ConditionGetter interface
func (d *DeploymentReconciler) GetConditions() *[]metav1.Condition {
	return &d.Instance.Status.Conditions
}

// Build implements the ResourceBuilder interface
func (d *DeploymentReconciler) Build(ctx context.Context) (client.Object, error) {
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      createDeploymentName(d.Instance.Name, d.GroupName),
			Namespace: d.Instance.Namespace,
			Labels:    d.MergedLabels,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: d.getReplicas(),
			Selector: &metav1.LabelSelector{
				MatchLabels: d.MergedLabels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: d.MergedLabels,
				},
				Spec: corev1.PodSpec{
					SecurityContext: d.getSecurityContext(),
					Containers: []corev1.Container{
						d.createContainer(),
					},
					Volumes: d.createVolumes(),
				},
			},
		},
	}
	return dep, nil
}

// CommandOverride implement the WorkloadOverride interface
func (d *DeploymentReconciler) CommandOverride(resource client.Object) {

}

// EnvOverride implement the WorkloadOverride interface
func (d *DeploymentReconciler) EnvOverride(resource client.Object) {

}

// LogOverride implement the WorkloadOverride interface
func (d *DeploymentReconciler) LogOverride(resource client.Object) {

}

// create container
func (d *DeploymentReconciler) createContainer() corev1.Container {
	image := d.getImageSpec()
	return corev1.Container{
		Name:            d.Instance.Name,
		Image:           image.Repository + ":" + image.Tag,
		ImagePullPolicy: image.PullPolicy,
		EnvFrom: []corev1.EnvFromSource{
			{
				SecretRef: &corev1.SecretEnvSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: createSecretName(d.Instance.Name, d.GroupName),
					},
				},
			},
		},
		Resources: d.getResources(),
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
		VolumeMounts: []corev1.VolumeMount{
			{
				Name:      d.createConfigVolumeName(),
				MountPath: "/opt/bitnami/spark/conf/spark-defaults.conf",
				SubPath:   "spark-defaults.conf",
			},
			{
				Name:      d.createDataVolumeName(),
				MountPath: d.getMountPath(),
			},
		},
	}
}

// create volumes
func (d *DeploymentReconciler) createVolumes() []corev1.Volume {
	return []corev1.Volume{
		{
			Name: d.createConfigVolumeName(),
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: createConfigName(d.Instance.Name, d.GroupName),
					},
				},
			},
		},
		{
			Name: d.createDataVolumeName(),
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
					ClaimName: createPvcName(d.Instance.Name, d.GroupName),
				},
			},
		},
	}
}

func (d *DeploymentReconciler) getImageSpec() *sparkv1alpha1.ImageSpec {
	return d.Instance.Spec.Image
}

// get security context
func (d *DeploymentReconciler) getSecurityContext() *corev1.PodSecurityContext {
	return d.MergedCfg.Config.SecurityContext
}

// get replicas
func (d *DeploymentReconciler) getReplicas() *int32 {
	return &d.MergedCfg.Replicas
}

// get resources
func (d *DeploymentReconciler) getResources() corev1.ResourceRequirements {
	resourcesSpec := d.MergedCfg.Config.Resources
	return *common.ConvertToResourceRequirements(resourcesSpec)
}

// get mountPath
func (d *DeploymentReconciler) getMountPath() string {
	var mountPath = "/tmp/spark-events"
	if d.MergedCfg.Config.EventLog != nil {
		mountPath = d.MergedCfg.Config.EventLog.Dir
	}
	return mountPath
}

func (d *DeploymentReconciler) createConfigVolumeName() string {
	return "config-volume"
}

func (d *DeploymentReconciler) createDataVolumeName() string {
	return "data-volume"
}
