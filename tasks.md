# Spark-K8s-Operator 功能重构任务规划

## 概述

基于 hdfs-operator 的重构经验，对 spark-k8s-operator 进行一致的功能单元调整。主要涉及以下几个方面：

1. **Kubebuilder 脚手架升级** - 升级到 v4.10.1 ✓
2. **Makefile 优化** - 改进目标定义和工具管理 ✓
3. **cmd/main.go 更新** - 添加证书配置支持 ✓
4. **RBAC 配置增强** - 添加 admin role 和改进权限管理 ✓
5. **工作流优化** - 优化 GitHub Actions 工作流 ✓
6. **Chainsaw E2E 测试** - 重构和优化测试流程 ✓
7. **配置文件整理** - 移除过时配置，优化组织结构 ✓

## 完成状态

### 第一阶段：根目录和主要文件 ✓ 完成

#### 1. ✓ 更新 PROJECT 文件
- 添加 `cliVersion: 4.10.1`

#### 2. ✓ 更新 Makefile
- 动态计算 ENVTEST_K8S_VERSION
- 新增 Helm Charts 和 Chainsaw E2E 目标
- 使用 OPERATOR_DEPENDS（保持项目一致性）

#### 3. ✓ 更新 cmd/main.go
- 调整 import 顺序
- 添加证书相关命令行参数（6 个）
- 更新 webhook/metrics server 配置

#### 4. ✓ 删除过时配置
- config/crd/patches/cainjection_in_sparkhistoryservers.yaml
- config/crd/patches/webhook_in_sparkhistoryservers.yaml

#### 5. ✓ 迁移 Chainsaw 配置
- .chainsaw.yaml → test/e2e/.chainsaw.yaml

#### 6. ✓ 更新 .gitignore
- 简化为 hdfs-operator 风格

#### 7. ✓ 更新 GitHub Workflows
- test.yml: 统一 chainsaw 任务名称和 product-version
- release.yml: 更新 product-version（3.5.2, 3.5.5）
- publish.yml: 优化发布流程
- chart-lint-test.yml: 优化 Helm 图表检查

### 第二阶段：config/ 目录完整重构 ✓ 完成
- `test.yml` - 更新为使用新的 Makefile 目标
- `release.yml` - 更新所有工作流目标名称和步骤
- `publish.yml` - 更新为使用 helm-chart-publish 目标
- `chart-lint-test.yml` - 更新工作流步骤

### 第二阶段：config/ 目录完整重构 ✓ 完成

#### 8. ✓ config/crd/ 目录改变
- 更新 controller-gen 版本从 v0.17.1 → v0.19.0
- 更新 kustomization.yaml 占位符和 configurations 注释

#### 9. ✓ config/default/ 目录改变
- 完整重写 kustomization.yaml（添加 METRICS-WITH-CERTS 和 cert-manager 配置）
- 更新 metrics_service.yaml 标签为 sparkhistoryserver

#### 10. ✓ config/manager/ 目录改变
- 完全重构 manager.yaml（标签、securityContext、字段）
- 启用 seccompProfile
- 添加 ports、volumeMounts、volumes 字段

#### 11. ✓ config/network-policy/ 目录改变
- 修复拼写：gathering → gather
- 更新标签为 sparkhistoryserver
- 添加完整的 podSelector 标签

#### 12. ✓ config/prometheus/ 目录改变
- 删除文件开头空行
- 简化标签为只保留必要项
- 更新 TLS 证书注释说明

#### 13. ✓ config/rbac/ 目录改变
- 所有 6 个文件标签简化完成
- 删除冗余标签（instance、component、created-by、part-of）
- 保留标签：app.kubernetes.io/name、app.kubernetes.io/managed-by
- 更新 editor 和 viewer 角色的文件顶部注释

## 验证结果

✓ config/crd/ 目录更新完成
✓ config/default/ 目录更新完成（包括完整的 cert-manager 配置）
✓ config/manager/ 目录更新完成
✓ config/network-policy/ 目录更新完成
✓ config/prometheus/ 目录更新完成
✓ config/rbac/ 目录更新完成
✓ 所有标签简化和标准化完成
✓ 所有文件 YAML 格式正确
✓ 所有项目标签已统一为 spark-k8s-operator

## 关键改变汇总

### 标签标准化
- **删除**的标签：instance、component、created-by、part-of
- **保留**的标签：
  - `app.kubernetes.io/name: spark-k8s-operator` （项目标识）
  - `app.kubernetes.io/managed-by: kustomize` （管理工具）

### 控制平面标签
- Deployment 标签：`control-plane: controller-manager`

### 项目标识说明
- **项目名**：spark-k8s-operator（所有资源标签使用）
- **CRD 资源名**：sparkhistoryserver/sparkhistoryservers（仅限 CRD 资源名称）
- 这是正确的区分：项目标识 vs CRD 资源标识

## 后续步骤

1. 运行 `make manifests` 生成 CRD
2. 运行 `make test` 执行单元测试
3. 运行 `make lint` 验证代码质量
4. 提交 PR 到上游仓库
