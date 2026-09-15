# AGENTS.md

## 语言规范
- 文档、代码注释、与用户的对话、Git 提交记录（commit message）一律使用**中文**
- 标识符、代码、API 字段名等技术实体保持英文

## 提交规范
- **每完成一轮任务（一个需求、修复或文档更新收尾）即自动 commit**，无需再次向用户确认
- commit 后**询问用户是否执行 git push、是否构建镜像并 docker push**，确认后再执行，不主动推送
- commit message 采用 conventional-commit 前缀 + 中文描述，如：
  `docs: 补充缓存计价需求`、`feat: 实现路由与失败切换引擎`
- 提交前仅暂存本轮任务涉及的文件；密钥、运行时数据（`./data`）、构建产物不入库
- 提交前运行可用的一致性检查（lint / test / 构建），失败则先修复再提交
- 镜像推送惯例：`harbor.xg.bytedance.net/octarray/keyway`，打 commit 短哈希 + `latest` 双标签

## 项目文档
- `docs/PRD.md`：需求文档（功能需求 FR-*、验收标准 A*、开放问题 Q*）
- `docs/DESIGN.md`：技术方案（架构、DDL、协议转换决策表、里程碑 M1–M6）
- 需求或方案变更时，两份文档必须**同步更新**并追加变更记录；实现阶段的验收以
  PRD 的 A1–A18 为准
