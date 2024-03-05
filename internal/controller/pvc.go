package controller

import (
	"context"
	sparkv1alpha1 "github.com/zncdata-labs/spark-k8s-operator/api/v1alpha1"
	"github.com/zncdata-labs/spark-k8s-operator/internal/common"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type PvcReconciler struct {
	common.GeneralResourceStyleReconciler[*sparkv1alpha1.SparkHistoryServer, *sparkv1alpha1.RoleGroupSpec]
}

// NewPvc new a PvcReconcile
func NewPvc(
	scheme *runtime.Scheme,
	instance *sparkv1alpha1.SparkHistoryServer,
	client client.Client,
	groupName string,
	mergedLabels map[string]string,
	mergedCfg *sparkv1alpha1.RoleGroupSpec,
) *PvcReconciler {
	return &PvcReconciler{
		GeneralResourceStyleReconciler: *common.NewGeneraResourceStyleReconciler[*sparkv1alpha1.SparkHistoryServer,
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
func (p *PvcReconciler) Build(_ context.Context) (client.Object, error) {
	cfg := p.MergedCfg
	var storageClassName *string
	var storageSize = resource.MustParse("10G")
	if cfg.Config.StorageClass != "" {
		storageClassName = &cfg.Config.StorageClass
	}
	if cfg.Config.StorageSize != "" {
		storageSize = resource.MustParse(cfg.Config.StorageSize)
	}
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      createPvcName(p.Instance.Name, p.GroupName),
			Namespace: p.Instance.Namespace,
			Labels:    p.MergedLabels,
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			StorageClassName: storageClassName,
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: storageSize,
				},
			},
		},
	}
	return pvc, nil
}
