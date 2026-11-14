# GitHub 仓库设置清单

> 文件表达不了、必须在 GitHub 界面完成的设置。建好远程仓库并首次 push 后逐项勾选。

- [ ] 新建私有仓库 `v2-hub`，本地 `git remote add origin <地址>` 后 push `main`
- [ ] Settings → General → Pull Requests：只勾 **Allow merge commits**；勾 **Automatically delete head branches**
- [ ] Settings → Rules → Rulesets：对 `main` 新建规则
  - Restrict deletions、Block force pushes
  - Require a pull request before merging；Required approvals = **0**（个人 + agent，Ruling-10；有新成员后改为 1）
  - Require status checks to pass：`preflight`、`v2-hub-server / test`、`v2-hub-admin-web / test`、`v2-hub-user-web / test`
    （后三个由 paths 过滤，PR 不涉及时不会出现，如被卡住可改为只要求 `preflight`）
- [ ] Settings → Rules → Rulesets：对 tag `v*` 限制创建与删除
- [ ] Settings → Actions → General：Workflow permissions = Read repository contents
- [ ] Settings → Secrets and variables → Actions：部署流水线启用时再添加（数据库密码、MinIO 密钥等），当前无需
- [ ] Issues → Labels：`mod:server`、`mod:admin-web`、`mod:user-web`、`mod:contract`、`type:feat/fix/docs/chore/refactor`
- [ ] Issues → Milestones：M0 脚手架与登录 / M1 聚合 / M2 派发 / M3 订阅 / M4 客户端链式 / M5 服务端链式 / M6 运营
