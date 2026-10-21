# 发版

稳定版本只从 `VERSION` 发布。`web/package.json`、`web/package-lock.json` 和 `core/cmd/monitord/main.go` 里的 `var version` 必须与 `VERSION` 相同。CI 在 `REQUIRE_GIT_TAG_VERSION=1` 时还要求：从当前提交能走到的最高稳定标签名等于 `VERSION`，并且该标签所在提交里的 `VERSION` 文件也是这个号。

## 打一个新版本

1. 在要发布的提交上把上述文件改成同一个 `MAJOR.MINOR.PATCH`（不要带 `v`）。
2. 合入 `main`。Release 工作流读该提交的 `VERSION`，打带注释的标签 `v$VERSION`，再构建安装包。
3. 标签已经存在且指向别的提交时，工作流不会重打、也不会重新打包。它打印 `already released` 并跳过。
4. 不要把较低版本的标签挪到较新的提交上之后，又让 GitHub 把那个旧标签显示成 Latest。`v3.0.16` 是在 `v3.0.17` 之后补发的，页面上的 Latest 徽章因此会落在 `v3.0.16`。下载时以版本号更高的标签为准。

## 标签指错提交

`v3.0.17` 曾经打在 `cbd6d66` 上，该提交的 `VERSION` 仍是 `3.0.16`。版本号改成 `3.0.17` 的提交是 `d8e0e25`。把标签移到那个提交：

```bash
git tag -f -a v3.0.17 d8e0e25 -m "Release v3.0.17"
git push --force origin refs/tags/v3.0.17
```

这次推送会按标签再跑一遍 Release，用 `d8e0e25` 的源码重新打包。不要把 `v3.0.17` 挪到更新的 `main`：那之后的提交不是另一次发版。

`scripts/build.sh core` 在版本检查失败时只打印警告，仍然编译。`scripts/build.sh --dev` 同样只警告。`scripts/build.sh all` 里的前端步骤在检查失败时会停下来。

## 更新日志

### 2026-10-20 外部检查 `output`

`POST /api/v1/checks` 增加可选字段 `output`，含义与 `message` 相同。只提交 `output` 时按说明文字保存。两个字段都有且内容不同时返回 400，不写入。只提交 `message` 的客户端不用改。给外部脚本看的条目在仓库根目录 [CHANGELOG.md](../CHANGELOG.md)，字段表在 [外部服务检查](06-monitor-2.0.md)。

### 2026-10-21 问题处置草稿与刷新失败保护

同时编辑责任人和处理进度时，保存其中一项后，剩余草稿继续以本次保存返回的版本号进行并发检查。若其他用户随后更新了记录，提交剩余草稿会触发冲突提示并保留草稿，避免套用刷新后的版本号而覆盖他人的修改。

总览刷新失败、正在展示旧快照时，单条问题的确认、指派、进度和备注提交与批量处置一样暂停；草稿保留，刷新成功后恢复提交。新增桌面与手机端回归测试覆盖这两种情况。
