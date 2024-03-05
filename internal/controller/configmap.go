package controller

import (
	"context"
	"fmt"

	sparkv1alpha1 "github.com/zncdata-labs/spark-k8s-operator/api/v1alpha1"
	"github.com/zncdata-labs/spark-k8s-operator/internal/common"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type ConfigMapReconciler struct {
	common.ConfigurationStyleReconciler[*sparkv1alpha1.SparkHistoryServer, *sparkv1alpha1.RoleGroupSpec]
}

// NewConfigMap new a ConfigMapReconcile
func NewConfigMap(
	scheme *runtime.Scheme,
	instance *sparkv1alpha1.SparkHistoryServer,
	client client.Client,
	groupName string,
	mergedLabels map[string]string,
	mergedCfg *sparkv1alpha1.RoleGroupSpec,
) *ConfigMapReconciler {
	return &ConfigMapReconciler{
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
func (c *ConfigMapReconciler) Build(ctx context.Context) (client.Object, error) {
	if configContent, err := c.makeSparkConfigData(ctx); err != nil {
		return nil, err
	} else {
		cm := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      createConfigName(c.Instance.Name, c.GroupName),
				Namespace: c.Instance.Namespace,
				Labels:    c.MergedLabels,
			},
			Data: map[string]string{
				"spark-defaults.conf": *configContent,
			},
		}
		return cm, nil
	}
}

// ConfigurationOverride implement the ConfigurationOverride interface
func (c *ConfigMapReconciler) ConfigurationOverride(resource client.Object) {
	cfg := c.MergedCfg
	overrides := cfg.ConfigOverrides
	if overrides != nil {
		configMap := resource.(*corev1.ConfigMap)
		data := &configMap.Data
		for k, v := range overrides.SparkConfig {
			(*data)[k] = v
		}
	}
}

func (c *ConfigMapReconciler) makeSparkConfigData(ctx context.Context) (*string, error) {
	var cfgContent string
	// make s3 config data
	if s3Cfg, err := c.makeS3Config(ctx); err != nil {
		return nil, err
	} else if s3Cfg != nil {
		cfgContent += *s3Cfg
	}
	//make event log data
	if eventLogCfg := c.makeEventLogConfig(); eventLogCfg != nil {
		cfgContent += *eventLogCfg
	}
	// make history data
	if historyCfg := c.makeHistoryConfig(); historyCfg != nil {
		cfgContent += *historyCfg
	}
	return &cfgContent, nil
}

const s3CfgTemp = `
spark.hadoop.fs.s3a.endpoint %s
spark.hadoop.fs.s3a.ssl.enabled %t
spark.hadoop.fs.s3a.impl org.apache.hadoop.fs.s3a.S3AFileSystem
spark.hadoop.fs.s3a.fast.upload true
spark.hadoop.fs.s3a.path.style.access %t
`

func (c *ConfigMapReconciler) makeS3Config(ctx context.Context) (*string, error) {
	s3Cfg := c.createS3Configuration(ctx)
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
	res := fmt.Sprintf(s3CfgTemp, params.Endpoint, params.SSL, params.PathStyle)
	return &res, nil
}

func (c *ConfigMapReconciler) createS3Configuration(
	ctx context.Context) *common.S3Configuration[common.InstanceAttributes] {
	cr := c.Instance
	// create s3 configuration
	return common.NewS3Configuration(
		&SparkHistoryInstance{Instance: cr},
		common.ResourceClient{
			Ctx:       ctx,
			Client:    c.Client,
			Namespace: cr.Namespace,
		},
	)
}

const eventLogCfgTemp = `
spark.eventLog.enabled %t
spark.eventLog.dir %s
spark.history.fs.logDirectory %s
`

// make event-log config
func (c *ConfigMapReconciler) makeEventLogConfig() *string {
	mergeCfg := c.MergedCfg
	eventLog := mergeCfg.Config.EventLog
	if eventLog == nil {
		return nil
	}
	var dir string
	dir = eventLog.Dir
	if eventLog.MountMode == "pvc" {
		dir = "file://" + dir
	} else if eventLog.MountMode == "s3" {
		dir = "s3a:/" + dir
	}
	cfg := fmt.Sprintf(eventLogCfgTemp, eventLog.Enabled, dir, dir)
	return &cfg
}

const historyCfgTemp = `
spark.history.fs.cleaner.enabled %t
spark.history.fs.cleaner.maxNum %d
spark.history.fs.cleaner.maxAge %s
spark.history.fs.eventLog.rolling.maxFilesToRetain %d
`

// make history config
func (c *ConfigMapReconciler) makeHistoryConfig() *string {
	mergeCfg := c.MergedCfg
	history := mergeCfg.Config.History
	if history == nil {
		return nil
	}
	cfg := fmt.Sprintf(historyCfgTemp, history.FsCleaner.Enabled, history.FsCleaner.MaxNum,
		history.FsCleaner.MaxAge, history.FsEnentLogRollingMaxFiles)
	return &cfg
}
