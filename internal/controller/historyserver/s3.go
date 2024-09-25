package historyserver

import (
	"context"
	"net/url"
	"path"
	"strconv"
	"strings"

	commonsv1alpha1 "github.com/zncdatadev/operator-go/pkg/apis/commons/v1alpha1"
	"github.com/zncdatadev/operator-go/pkg/apis/s3/v1alpha1"
	"github.com/zncdatadev/operator-go/pkg/client"
	"github.com/zncdatadev/operator-go/pkg/constants"
	"github.com/zncdatadev/operator-go/pkg/util"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	shsv1alpha1 "github.com/zncdatadev/spark-k8s-operator/api/v1alpha1"
)

const (
	S3AccessKeyName = "ACCESS_KEY"
	S3SecretKeyName = "SECRET_KEY"

	S3VolumeName = "s3-credentials"
)

// TODO: Add the tls verification
type S3BucketConnect struct {
	Endpoint   url.URL
	Bucket     string
	Region     string
	PathStyle  bool
	credential *commonsv1alpha1.Credentials
}

func GetS3BucketConnect(ctx context.Context, client *client.Client, s3 *shsv1alpha1.S3BucketSpec) (*S3BucketConnect, error) {
	if s3.Inline != nil {
		return GetInlineS3Bucket(ctx, client, s3.Inline)
	}

	if s3.Reference != "" {
		return GetReferenceS3Bucket(ctx, client, s3.Reference)
	}

	return nil, nil
}

func GetReferenceS3Bucket(ctx context.Context, client *client.Client, name string) (*S3BucketConnect, error) {
	s3Bucket := &v1alpha1.S3Bucket{}
	if err := client.GetWithOwnerNamespace(ctx, name, s3Bucket); err != nil {
		return nil, err
	}

	return GetInlineS3Bucket(ctx, client, &s3Bucket.Spec)
}

func GetInlineS3Bucket(ctx context.Context, client *client.Client, s3Bucket *v1alpha1.S3BucketSpec) (*S3BucketConnect, error) {
	refConnection := s3Bucket.Connection.Reference
	s3ConnectionSpec := s3Bucket.Connection.Inline
	if refConnection != "" {
		s3Connection, err := GetRefreenceS3Connection(ctx, client, refConnection)
		if err != nil {
			return nil, err
		}
		s3ConnectionSpec = &s3Connection.Spec
	}

	endpoint := url.URL{
		Scheme: "http",
		Host:   s3ConnectionSpec.Host,
	}
	if s3ConnectionSpec.Port != 0 {
		endpoint.Host += ":" + strconv.Itoa(s3ConnectionSpec.Port)
	}

	return &S3BucketConnect{
		Endpoint:   endpoint,
		Bucket:     s3Bucket.BucketName,
		Region:     "us-west-1",
		PathStyle:  s3ConnectionSpec.PathStyle,
		credential: s3ConnectionSpec.Credentials,
	}, nil
}

func GetRefreenceS3Connection(ctx context.Context, client *client.Client, name string) (*v1alpha1.S3Connection, error) {
	s3Connection := &v1alpha1.S3Connection{}
	if err := client.GetWithOwnerNamespace(ctx, name, s3Connection); err != nil {
		return nil, err
	}
	return s3Connection, nil
}

type S3Logconfig struct {
	S3BucketConnect *S3BucketConnect
	LogPath         string
}

func NewS3Logconfig(
	ctx context.Context,
	client *client.Client,
	s3 *shsv1alpha1.S3Spec,
) (*S3Logconfig, error) {
	s3BucketConnect, err := GetS3BucketConnect(ctx, client, s3.Bucket)
	if err != nil {
		return nil, err
	}

	return &S3Logconfig{
		S3BucketConnect: s3BucketConnect,
		LogPath:         s3.Prefix,
	}, nil
}

func (s *S3Logconfig) GetMountPath() string {
	return path.Join(constants.KubedoopSecretDir, "s3-credentials")
}

func (s *S3Logconfig) GetVolumeName() string {
	return S3VolumeName
}

func (s *S3Logconfig) GetLogDirectory() string {
	s3path := url.URL{
		Scheme: "s3a",
		Host:   s.S3BucketConnect.Bucket,
		Path:   s.LogPath,
	}
	return s3path.String()
}

func (s *S3Logconfig) GetEndpoint() string {
	return s.S3BucketConnect.Endpoint.String()
}

func (s *S3Logconfig) GetPartialProperties() map[string]string {

	sslEnabled := s.S3BucketConnect.Endpoint.Scheme == "https"

	properties := map[string]string{
		"spark.history.fs.logDirectory":              s.GetLogDirectory(),
		"spark.hadoop.fs.s3a.endpoint":               s.GetEndpoint(),
		"spark.hadoop.fs.s3a.path.style.access":      "true",
		"spark.hadoop.fs.s3a.connection.ssl.enabled": strconv.FormatBool(sslEnabled),
	}
	return properties
}

func (s *S3Logconfig) GetVolume() *corev1.Volume {

	credential := s.S3BucketConnect.credential

	secretClass := credential.SecretClass

	annotations := map[string]string{
		constants.AnnotationSecretsClass: secretClass,
	}

	if credential.Scope != nil {
		scopes := []string{}
		if credential.Scope.Node {
			scopes = append(scopes, string(constants.NodeScope))
		}
		if credential.Scope.Pod {
			scopes = append(scopes, string(constants.PodScope))
		}
		scopes = append(scopes, credential.Scope.Services...)

		annotations[constants.AnnotationSecretsScope] = strings.Join(scopes, constants.CommonDelimiter)
	}
	secretVolume := &corev1.Volume{
		Name: s.GetVolumeName(),
		VolumeSource: corev1.VolumeSource{
			Ephemeral: &corev1.EphemeralVolumeSource{
				VolumeClaimTemplate: &corev1.PersistentVolumeClaimTemplate{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: annotations,
					},
					Spec: corev1.PersistentVolumeClaimSpec{
						AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
						StorageClassName: constants.SecretStorageClassPtr(),
						Resources: corev1.VolumeResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceStorage: resource.MustParse("1Mi"),
							},
						},
					},
				},
			},
		},
	}
	return secretVolume
}

func (s *S3Logconfig) GetVolumeMount() *corev1.VolumeMount {
	secretVolumeMount := &corev1.VolumeMount{
		Name:      s.GetVolumeName(),
		MountPath: s.GetMountPath(),
	}

	return secretVolumeMount
}

func (s *S3Logconfig) GetPartialCmdArgs() string {
	args := `
export AWS_ACCESS_KEY_ID=$(cat ` + path.Join(s.GetMountPath(), S3AccessKeyName) + `)
export AWS_SECRET_ACCESS_KEY=$(cat ` + path.Join(s.GetMountPath(), S3SecretKeyName) + `)
`

	return util.IndentTab4Spaces(args)
}
