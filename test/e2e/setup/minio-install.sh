SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

helm upgrade --install minio \
  --namespace $NAMESPACE \
  --version 12.6.4 \
  -f "${SCRIPT_DIR}/helm-bitnami-minio-values.yaml" \
  --repo https://charts.bitnami.com/bitnami minio
