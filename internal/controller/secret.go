package controller

import (
	"context"

	sparkv1alpha1 "github.com/zncdata-labs/spark-k8s-operator/api/v1alpha1"
	"github.com/zncdata-labs/spark-k8s-operator/internal/common"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type SecretReconciler struct {
	common.ConfigurationStyleReconciler[*sparkv1alpha1.SparkHistoryServer, *sparkv1alpha1.RoleGroupSpec]
}

// NewSecret new a SecretReconcile
func NewSecret(
	scheme *runtime.Scheme,
	instance *sparkv1alpha1.SparkHistoryServer,
	client client.Client,
	groupName string,
	mergedLabels map[string]string,
	mergedCfg *sparkv1alpha1.RoleGroupSpec,

) *SecretReconciler {
	return &SecretReconciler{
		ConfigurationStyleReconciler: *common.NewConfigurationStyleReconciler[*sparkv1alpha1.SparkHistoryServer,
			*sparkv1alpha1.RoleGroupSpec](
			scheme,
			instance,
			client,
			groupName,
			mergedLabels,
			mergedCfg,
		),
	}
}

// Build implements the ResourceBuilder interface
func (s *SecretReconciler) Build(ctx context.Context) (client.Object, error) {
	if secretContent, err := s.makeSparkSecretData(ctx); err != nil {
		return nil, err
	} else {
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      createSecretName(s.Instance.Name, s.GroupName),
				Namespace: s.Instance.Namespace,
				Labels:    s.MergedLabels,
			},
			Data: secretContent,
		}
		return secret, nil
	}
}

// ConfigurationOverride implement the ConfigurationOverride interface
func (s *SecretReconciler) ConfigurationOverride(resource client.Object) {
	cfg := s.MergedCfg
	overrides := cfg.ConfigOverrides
	if overrides != nil {
		configMap := resource.(*corev1.Secret)
		data := &configMap.Data
		for k, v := range overrides.SparkConfig {
			(*data)[k] = []byte(v)
		}
	}
}

// makeSparkSecretData creates the secret data
func (s *SecretReconciler) makeSparkSecretData(ctx context.Context) (map[string][]byte, error) {
	s3Cfg := s.createS3Configuration(ctx)
	if !s3Cfg.Enabled() {
		return nil, nil
	}
	var params *common.S3Params
	var err error
	if !s3Cfg.ExistingS3Bucket() {
		params, err = s3Cfg.GetS3ParamsFromResource()
	} else {
		params, err = s3Cfg.GetS3ParamsFromInline()
	}
	if err != nil {
		return nil, err
	}
	return s.makeS3Data(params), nil
}

func (s *SecretReconciler) createS3Configuration(
	ctx context.Context) *common.S3Configuration[common.InstanceAttributes] {
	cr := s.Instance
	// create s3 configuration
	return common.NewS3Configuration(
		&SparkHistoryInstance{Instance: cr},
		common.ResourceClient{
			Ctx:       ctx,
			Client:    s.Client,
			Namespace: cr.Namespace,
		},
	)
}
func (s *SecretReconciler) makeS3Data(s3Params *common.S3Params) map[string][]byte {
	if s3Params != nil {
		data := make(map[string][]byte)
		data["AWS_ACCESS_KEY_ID"] = []byte(s3Params.AccessKey)
		data["AWS_SECRET_ACCESS_KEY"] = []byte(s3Params.SecretKey)
		data["AWS_DEFAULT_REGION"] = []byte(s3Params.Region)
		return data
	}
	return nil
}
