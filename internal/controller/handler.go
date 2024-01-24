package controller

import (
	"context"
	"fmt"
	opgo "github.com/zncdata-labs/operator-go/pkg/apis/commons/v1alpha1"
	"github.com/zncdata-labs/operator-go/pkg/errors"
	stackv1alpha1 "github.com/zncdata-labs/spark-k8s-operator/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// reconcile config map
func (r *SparkHistoryServerReconciler) reconcileConfigMap(ctx context.Context,
	instance *stackv1alpha1.SparkHistoryServer) error {
	reconcileParams := r.createReconcileParams(ctx, instance, r.extractConfigMapForRoleGroup)
	if err := reconcileParams.createOrUpdateResource(); err != nil {
		return err
	}
	return nil
}

// extract configmap for role group
func (r *SparkHistoryServerReconciler) extractConfigMapForRoleGroup(params ExtractorParams) (client.Object, error) {
	realRg := params.roleGroup.(stackv1alpha1.RoleConfigSpec)
	mergeLabels := r.mergeLabels(params.instance.GetLabels(), &realRg)
	realInstance := params.instance.(*stackv1alpha1.SparkHistoryServer)
	realGroup := params.roleGroup.(*stackv1alpha1.RoleConfigSpec)
	eventLog, s3Config, history := r.getConfigMapInfo(realInstance, realGroup)

	var configContent *string
	// make s3 config content
	if err := r.makeS3Config(configContent, params.ctx, realInstance.Namespace, s3Config); err != nil {
		return nil, err
	}
	// make event-log  config content
	r.makeEventLogConfig(configContent, eventLog)
	// make history config content
	r.makeHistoryConfig(configContent, history)

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      realInstance.GetNameWithSuffix(params.roleGroupName),
			Namespace: realInstance.Namespace,
			Labels:    mergeLabels,
		},
		Data: map[string]string{
			"spark-defaults.conf": *configContent,
		},
	}

	err := ctrl.SetControllerReference(realInstance, cm, params.scheme)
	if err != nil {
		r.Log.Error(err, "Failed to set controller reference for configmap")
		return nil, errors.Wrap(err, "Failed to set controller reference for configmap")
	}
	return cm, nil
}

// reconcile secret
func (r *SparkHistoryServerReconciler) reconcileSecret(ctx context.Context,
	instance *stackv1alpha1.SparkHistoryServer) error {
	reconcileParams := r.createReconcileParams(ctx, instance, r.extractSecretForRoleGroup)
	if err := reconcileParams.createOrUpdateResource(); err != nil {
		return err
	}
	return nil
}

func (r *SparkHistoryServerReconciler) extractSecretForRoleGroup(params ExtractorParams) (client.Object, error) {
	instance := params.instance.(*stackv1alpha1.SparkHistoryServer)
	roleGroup := params.roleGroup.(*stackv1alpha1.RoleConfigSpec)
	roleGroupName := params.roleGroupName
	mergeLabels := r.mergeLabels(instance.GetLabels(), roleGroup)
	schema := params.scheme
	ctx := params.ctx

	// https://cwiki.apache.org/confluence/display/Hive/Setting+Up+Hive+with+Docker
	var s3 *stackv1alpha1.S3Spec = r.getS3Info(instance, roleGroup)
	var (
		s3Connection *opgo.S3Connection
		err          error
	)
	if s3 != nil {
		s3Bucket := &opgo.S3Bucket{
			ObjectMeta: metav1.ObjectMeta{Name: s3.Reference},
		}
		s3Connection, err = r.fetchS3(s3Bucket, ctx, instance.GetNamespace())
		if err != nil {
			return nil, err
		}
	}

	data := make(map[string][]byte)
	r.makeS3Data(s3Connection, &data)
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      instance.GetNameWithSuffix(roleGroupName),
			Namespace: instance.Namespace,
			Labels:    mergeLabels,
		},
		Type: corev1.SecretTypeOpaque,
		Data: data,
	}

	if err := ctrl.SetControllerReference(instance, secret, schema); err != nil {
		r.Log.Error(err, "Failed to set controller reference for secret")
		return nil, err
	}
	return secret, nil
}

// reconcile pvc
func (r *SparkHistoryServerReconciler) reconcilePVC(ctx context.Context,
	instance *stackv1alpha1.SparkHistoryServer) error {
	reconcileParams := r.createReconcileParams(ctx, instance, r.extractPvcForRoleGroup)
	if err := reconcileParams.createOrUpdateResource(); err != nil {
		return err
	}
	return nil
}

// extract pvc for role group
func (r *SparkHistoryServerReconciler) extractPvcForRoleGroup(params ExtractorParams) (client.Object, error) {
	realRg := params.roleGroup.(stackv1alpha1.RoleConfigSpec)
	mergeLabels := r.mergeLabels(params.instance.GetLabels(), &realRg)
	realInstance := params.instance.(*stackv1alpha1.SparkHistoryServer)
	storageClass, size, mode, _ := r.getPvcInfo(realInstance, &realRg)

	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      realInstance.GetNameWithSuffix(params.roleGroupName),
			Namespace: realInstance.Namespace,
			Labels:    mergeLabels,
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			StorageClassName: storageClass,
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: resource.MustParse(size),
				},
			},
			VolumeMode: &mode,
		},
	}

	err := ctrl.SetControllerReference(realInstance, pvc, params.scheme)
	if err != nil {
		r.Log.Error(err, "Failed to set controller reference for pvc")
		return nil, errors.Wrap(err, "Failed to set controller reference for pvc")
	}
	return pvc, nil
}

// reconcile deployment
func (r *SparkHistoryServerReconciler) reconcileDeployment(ctx context.Context,
	instance *stackv1alpha1.SparkHistoryServer) error {
	reconcileParams := r.createReconcileParams(ctx, instance, r.extractDeploymentForRoleGroup)
	if err := reconcileParams.createOrUpdateResource(); err != nil {
		return err
	}
	return nil
}

// extract deployment for role group
func (r *SparkHistoryServerReconciler) extractDeploymentForRoleGroup(params ExtractorParams) (client.Object, error) {
	instance := params.instance.(*stackv1alpha1.SparkHistoryServer)
	roleGroup := params.roleGroup.(*stackv1alpha1.RoleConfigSpec)
	groupName := params.roleGroupName
	mergeLabels := r.mergeLabels(instance.GetLabels(), roleGroup)
	image, securityContext, replicas, resources, mountPath := r.getDeploymentInfo(instance, roleGroup)

	confVolumeNameFunc := func() string { return instance.GetNameWithSuffix(groupName + "-conf") }
	dataVolumeNameFunc := func() string { return instance.GetNameWithSuffix(groupName + "-data") }
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      instance.GetNameWithSuffix(groupName),
			Namespace: instance.Namespace,
			Labels:    mergeLabels,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: mergeLabels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: mergeLabels,
				},
				Spec: corev1.PodSpec{
					SecurityContext: securityContext,
					Containers: []corev1.Container{
						{
							Name:            instance.Name,
							Image:           image.Repository + ":" + image.Tag,
							ImagePullPolicy: image.PullPolicy,
							EnvFrom: []corev1.EnvFromSource{
								{
									SecretRef: &corev1.SecretEnvSource{
										LocalObjectReference: corev1.LocalObjectReference{
											Name: instance.GetNameWithSuffix(groupName),
										},
									},
								},
							},
							Resources: *resources,
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
									Name:      confVolumeNameFunc(),
									MountPath: "/opt/bitnami/spark/conf/spark-defaults.conf",
									SubPath:   "spark-defaults.conf",
								},
								{
									Name:      dataVolumeNameFunc(),
									MountPath: mountPath,
								},
							},
						},
					},
					Volumes: []corev1.Volume{
						{
							Name: confVolumeNameFunc(),
							VolumeSource: corev1.VolumeSource{
								ConfigMap: &corev1.ConfigMapVolumeSource{
									LocalObjectReference: corev1.LocalObjectReference{
										Name: instance.GetNameWithSuffix(groupName),
									},
								},
							},
						},
						{
							Name: dataVolumeNameFunc(),
							VolumeSource: corev1.VolumeSource{
								PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
									ClaimName: instance.GetNameWithSuffix(groupName),
								},
							},
						},
					},
				},
			},
		},
	}
	// create scheduler for affinity and tolerations and nodeSelector
	SparkHistoryServerScheduler(instance, dep, roleGroup)

	if err := ctrl.SetControllerReference(instance, dep, r.Scheme); err != nil {
		r.Log.Error(err, "Failed to set controller reference for deployment")
		return nil, err
	}
	return dep, nil
}

// reconcile service
func (r *SparkHistoryServerReconciler) reconcileService(ctx context.Context,
	instance *stackv1alpha1.SparkHistoryServer) error {
	reconcileParams := r.createReconcileParams(ctx, instance, r.extractServiceForRoleGroup)
	if err := reconcileParams.createOrUpdateResource(); err != nil {
		return err
	}
	return nil
}

// extract service for role group
func (r *SparkHistoryServerReconciler) extractServiceForRoleGroup(params ExtractorParams) (client.Object, error) {
	instance := params.instance.(*stackv1alpha1.SparkHistoryServer)
	roleGroup := params.roleGroup.(*stackv1alpha1.RoleConfigSpec)
	groupName := params.roleGroupName
	mergeLabels := r.mergeLabels(instance.GetLabels(), roleGroup)
	port, svcType, annotations := r.getServiceInfo(instance.Spec.RoleConfig.Service, roleGroup)

	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:        instance.GetNameWithSuffix(groupName),
			Namespace:   instance.Namespace,
			Labels:      mergeLabels,
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
			Selector: mergeLabels,
			Type:     svcType,
		},
	}
	err := ctrl.SetControllerReference(instance, svc, params.scheme)
	if err != nil {
		r.Log.Error(err, "Failed to set controller reference for service")
		return nil, errors.Wrap(err, "Failed to set controller reference for service")
	}
	return svc, nil
}

// reconcile ingress
func (r *SparkHistoryServerReconciler) reconcileIngress(ctx context.Context,
	instance *stackv1alpha1.SparkHistoryServer) error {
	reconcilePrams := r.createReconcileParams(ctx, instance, r.extractIngressForRoleGroup)
	if err := reconcilePrams.createOrUpdateResource(); err != nil {
		return err
	}
	return nil
}

// extract ingres for role group
func (r *SparkHistoryServerReconciler) extractIngressForRoleGroup(params ExtractorParams) (client.Object, error) {
	instance := params.instance.(*stackv1alpha1.SparkHistoryServer)
	roleGroup := params.roleGroup.(*stackv1alpha1.RoleConfigSpec)
	groupName := params.roleGroupName
	mergeLabels := r.mergeLabels(instance.GetLabels(), roleGroup)
	pt := v1.PathTypeImplementationSpecific
	host, port := r.getIngressInfo(instance, roleGroup)

	ing := &v1.Ingress{
		ObjectMeta: metav1.ObjectMeta{
			Name:      instance.GetNameWithSuffix(groupName),
			Namespace: instance.Namespace,
			Labels:    mergeLabels,
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
											Name: instance.GetNameWithSuffix(groupName),
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

	err := ctrl.SetControllerReference(instance, ing, params.scheme)
	if err != nil {
		r.Log.Error(err, "Failed to set controller reference for ingress")
		return nil, errors.Wrap(err, "Failed to set controller reference for ingress")
	}
	return ing, nil
}

// create Reconcile params
func (r *SparkHistoryServerReconciler) createReconcileParams(ctx context.Context,
	instance *stackv1alpha1.SparkHistoryServer,
	method func(params ExtractorParams) (client.Object, error)) *ReconcileParams {
	return &ReconcileParams{
		instance:           instance,
		Client:             r.Client,
		ctx:                ctx,
		scheme:             r.Scheme,
		log:                r.Log,
		roleGroupExtractor: method,
	}
}

func (r *SparkHistoryServerReconciler) makeS3Data(s3Connection *opgo.S3Connection, data *map[string][]byte) {
	if s3Connection != nil {
		s3Credential := s3Connection.Spec.S3Credential
		(*data)["AWS_ACCESS_KEY_ID"] = []byte(s3Credential.AccessKey)
		(*data)["AWS_SECRET_ACCESS_KEY"] = []byte(s3Credential.SecretKey)
		(*data)["AWS_DEFAULT_REGION"] = []byte(s3Connection.Spec.Region)
	}
}

// make s3 config
func (r *SparkHistoryServerReconciler) makeS3Config(configContent *string, ctx context.Context, namespace string,
	s3Config *stackv1alpha1.S3Spec) error {
	if s3Config == nil {
		return nil
	}
	s3Bucket := &opgo.S3Bucket{
		ObjectMeta: metav1.ObjectMeta{Name: s3Config.Reference},
	}
	s3Connection, err := r.fetchS3(s3Bucket, ctx, namespace)
	if err != nil {
		return err
	}
	*configContent = fmt.Sprintf(s3CfgTemp, s3Connection.Spec.Endpoint, s3Connection.Spec.SSL,
		"org.apache.hadoop.fs.s3a.S3AFileSystem", true, s3Config.PathStyleAccess)
	return nil
}

// make event-log config
func (r *SparkHistoryServerReconciler) makeEventLogConfig(configContent *string, eventLog *stackv1alpha1.EventLogSpec) {
	if eventLog == nil {
		return
	}
	var dir string
	if eventLog != nil {
		dir = eventLog.Dir
		if eventLog.MountMode == "pvc" {
			dir = "file://" + dir
		} else if eventLog.MountMode == "s3" {
			dir = "s3a:/" + dir
		}
	}
	*configContent += fmt.Sprintf(eventLogCfgTemp, eventLog.Enabled, dir, dir)
}

// make history config
func (r *SparkHistoryServerReconciler) makeHistoryConfig(configContent *string, history *stackv1alpha1.HistorySpec) {
	if history == nil {
		return
	}
	*configContent += fmt.Sprintf(historyCfgTemp, history.FsCleaner.Enabled, history.FsCleaner.MaxNum,
		history.FsCleaner.MaxAge, history.FsEnentLogRollingMaxFiles)

}

func (r *SparkHistoryServerReconciler) fetchS3(s3Bucket *opgo.S3Bucket, ctx context.Context,
	namespace string) (*opgo.S3Connection, error) {
	// 1 - fetch exists s3-bucket by reference
	req := &ResourceRequest{
		Ctx:       ctx,
		Client:    r.Client,
		Obj:       s3Bucket,
		Namespace: namespace,
		Log:       r.Log,
	}
	if err := req.fetchResource(); err != nil {
		return nil, err
	}
	//2 - fetch exist s3-connection by pre-fetch bucketName
	s3Connection := &opgo.S3Connection{
		ObjectMeta: metav1.ObjectMeta{Name: s3Bucket.Spec.Reference},
	}
	req.Obj = s3Connection
	if err := req.fetchResource(); err != nil {
		return nil, err
	}
	return s3Connection, nil
}
