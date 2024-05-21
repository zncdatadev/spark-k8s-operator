package controller

import (
	"context"
	"maps"
	"time"

	sparkv1alpha1 "github.com/zncdatadev/spark-k8s-operator/api/v1alpha1"
	"github.com/zncdatadev/spark-k8s-operator/internal/common"
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

	podTemplate := d.getPodTemplate()

	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      createDeploymentName(d.Instance.Name, d.GroupName),
			Namespace: d.Instance.Namespace,
			Labels:    d.MergedLabels,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: d.getReplicas(ctx),
			Selector: &metav1.LabelSelector{
				MatchLabels: d.MergedLabels,
			},
			Template: podTemplate,
		},
	}
	return dep, nil
}

func (d *DeploymentReconciler) getTerminationGracePeriodSeconds() *int64 {
	if d.MergedCfg.Config.GracefulShutdownTimeout != nil {
		if tiime, err := time.ParseDuration(*d.MergedCfg.Config.GracefulShutdownTimeout); err == nil {
			seconds := int64(tiime.Seconds())
			return &seconds
		}
	}
	return nil
}

func (d *DeploymentReconciler) getPodTemplate() corev1.PodTemplateSpec {

	// use podOverride if it is not nil, other value will merge into podOverride
	podTemplate := corev1.PodTemplateSpec{}
	if d.MergedCfg.PodOverride != nil {
		podTemplate = *d.MergedCfg.PodOverride.DeepCopy()
	}

	if podTemplate.ObjectMeta.Labels == nil {
		podTemplate.ObjectMeta.Labels = make(map[string]string)
	}

	maps.Copy(podTemplate.ObjectMeta.Labels, d.MergedLabels)

	podTemplate.Spec.Containers = append(podTemplate.Spec.Containers, d.createContainer())
	podTemplate.Spec.Volumes = append(podTemplate.Spec.Volumes, d.createVolumes()...)

	// set terminationGracePeriodSeconds, it maybe override podOverride value
	seconds := d.getTerminationGracePeriodSeconds()
	if d.MergedCfg.Config.GracefulShutdownTimeout != nil {
		podTemplate.Spec.TerminationGracePeriodSeconds = seconds
	}

	return podTemplate
}

// CommandOverride implement the WorkloadOverride interface
func (d *DeploymentReconciler) CommandOverride(resource client.Object) {
	dep := resource.(*appsv1.Deployment)
	containers := dep.Spec.Template.Spec.Containers
	if cmdOverride := d.MergedCfg.CommandArgsOverrides; cmdOverride != nil {
		for i := range containers {
			containers[i].Command = cmdOverride
		}
	}
}

// EnvOverride implement the WorkloadOverride interface
func (d *DeploymentReconciler) EnvOverride(resource client.Object) {
	dep := resource.(*appsv1.Deployment)
	containers := dep.Spec.Template.Spec.Containers
	if envOverride := d.MergedCfg.EnvOverrides; envOverride != nil {
		for i := range containers {
			envVars := containers[i].Env
			common.OverrideEnvVars(&envVars, d.MergedCfg.EnvOverrides)
		}
	}
}

// LogOverride implement the WorkloadOverride interface
func (d *DeploymentReconciler) LogOverride(resource client.Object) {
	if d.isLoggersOverrideEnabled() {
		d.logVolumesOverride(resource)
		d.logVolumeMountsOverride(resource)
	}
}

// is loggers override enabled
func (d *DeploymentReconciler) isLoggersOverrideEnabled() bool {
	return d.MergedCfg.Config.Logging != nil
}

func (d *DeploymentReconciler) logVolumesOverride(resource client.Object) {
	dep := resource.(*appsv1.Deployment)
	volumes := dep.Spec.Template.Spec.Volumes
	if len(volumes) == 0 {
		volumes = make([]corev1.Volume, 1)
	}
	volumes = append(volumes, corev1.Volume{
		Name: d.log4jVolumeName(),
		VolumeSource: corev1.VolumeSource{
			ConfigMap: &corev1.ConfigMapVolumeSource{
				LocalObjectReference: corev1.LocalObjectReference{
					Name: CreateRoleGroupLoggingConfigMapName(d.Instance.Name, d.GroupName),
				},
				Items: []corev1.KeyToPath{
					{
						Key:  Log4jCfgName,
						Path: Log4jCfgName,
					},
				},
			},
		},
	})
	dep.Spec.Template.Spec.Volumes = volumes
}

func (d *DeploymentReconciler) logVolumeMountsOverride(resource client.Object) {
	dep := resource.(*appsv1.Deployment)
	containers := dep.Spec.Template.Spec.Containers
	for i := range containers {
		containers[i].VolumeMounts = append(containers[i].VolumeMounts, corev1.VolumeMount{
			Name:      d.log4jVolumeName(),
			MountPath: "/opt/bitnami/spark/conf/log4j2.properties",
			SubPath:   Log4jCfgName,
		})
	}
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
				ContainerPort: SparkHistoryHTTPPortNumber,
				Name:          "http",
				Protocol:      "TCP",
			},
		},
		VolumeMounts: []corev1.VolumeMount{
			{
				Name:      d.configVolumeName(),
				MountPath: "/opt/bitnami/spark/conf/spark-defaults.conf",
				SubPath:   "spark-defaults.conf",
			},
			{
				Name:      d.dataVolumeName(),
				MountPath: d.getMountPath(),
			},
		},
	}
}

// create volumes
func (d *DeploymentReconciler) createVolumes() []corev1.Volume {
	return []corev1.Volume{
		{
			Name: d.configVolumeName(),
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: createConfigName(d.Instance.Name, d.GroupName),
					},
				},
			},
		},
		{
			Name: d.dataVolumeName(),
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

func (d *DeploymentReconciler) shouldStop(ctx context.Context) bool {

	clusterOperation := common.NewClusterOperation(
		&SparkHistoryInstance{Instance: d.Instance},
		common.ResourceClient{
			Ctx:       ctx,
			Client:    d.Client,
			Namespace: d.Instance.Namespace,
		},
	)

	return clusterOperation.ClusterStop()
}

// get replicas
func (d *DeploymentReconciler) getReplicas(ctx context.Context) *int32 {
	if d.shouldStop(ctx) {
		logger.Info("Stop the cluster, set replicas to 0")
		reps := int32(0)
		return &reps
	}
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

func (d *DeploymentReconciler) configVolumeName() string {
	return "config-volume"
}

func (d *DeploymentReconciler) dataVolumeName() string {
	return "data-volume"
}

// create log4j2 volume name
func (d *DeploymentReconciler) log4jVolumeName() string {
	return "log4j2-volume"
}
