<p align="center">
  <img src="docs/favicon.svg" width="72" height="72" alt="CodexPro logo">
</p>

<h1 align="center">CodexPro</h1>

<p align="center">
  让 ChatGPT 在你明确允许的本地仓库上使用编码工具。
</p>

<p align="center">
  <a href="https://www.npmjs.com/package/codexpro"><img alt="npm" src="https://img.shields.io/npm/v/codexpro?style=flat-square"></a>
  <a href="https://github.com/rebel0789/codexpro/actions"><img alt="CI" src="https://img.shields.io/github/actions/workflow/status/rebel0789/codexpro/ci.yml?branch=main&style=flat-square"></a>
  <a href="https://github.com/rebel0789/codexpro/blob/main/LICENSE"><img alt="License" src="https://img.shields.io/github/license/rebel0789/codexpro?style=flat-square"></a>
  <a href="https://rebel0789.github.io/codexpro/zh.html"><img alt="中文站点" src="https://img.shields.io/badge/site-%E4%B8%AD%E6%96%87%E6%96%87%E6%A1%A3-67e8f9?style=flat-square"></a>
</p>

<p align="center">
  <a href="README.md">English</a>
  ·
  <a href="https://rebel0789.github.io/codexpro/zh.html">中文网站</a>
  ·
  <a href="FAQ_ZH.md">中文 FAQ</a>
  ·
  <a href="SECURITY.md">安全说明</a>
</p>

## 它是什么

CodexPro 是本地 MCP server。它连接**你的 ChatGPT 会话**、**你的机器**和**你允许的仓库**。

ChatGPT 可以读取、搜索、编辑、审查、验证、导入附件，并写 handoff 计划。范围始终限制在这些 root 内。

它不是托管 SaaS、模型代理、配额绕过、账号池或远程 shell 服务。

## 安装

需要：

- Node.js 20+
- 能创建自定义 MCP 插件的 ChatGPT 账号
- ChatGPT Web 可用的 HTTPS 地址（tunnel 或 Tailscale Funnel）

```bash
npm install -g codexpro
cd /path/to/your/repo
codexpro setup
```

## 在 ChatGPT 中连接

1. `Settings -> Security and login` → 打开 **Developer mode**（保持 CSP 开启）。
2. `Settings -> Plugins` → Plugins 标签页 → 搜索框旁的 **+**。
3. 创建名为 `CodexPro` 的插件。
4. 连接方式：**Server URL** → 粘贴 CodexPro 复制的 URL。
5. 认证：**No Authentication / None**（表单可能默认 OAuth，创建前改掉）。

CodexPro 的认证就在这个 URL 里的 token。不要分享该 URL。

| 打开 Plugins 并点击 `+` | 填写 New Plugin 表单 |
| --- | --- |
| ![打开 Plugins 并点击加号](docs/images/chatgpt-plugins-add.png) | ![填写 New Plugin 表单](docs/images/chatgpt-plugin-details.png) |

同一仓库日常启动：

```bash
codexpro start
```

如果创建插件失败，运行 `codexpro connection-test`，确认 ChatGPT 请求是否到达本地 server。

## ChatGPT 能做什么

在 workspace write 模式（常规 agent 设置）下：

- 读取、搜索、检查仓库
- 用 `write`、四字符标签多段 `edit` 或受保护的 `apply_patch` 编辑
- 用可编辑、可续跑的 `batch` 文件合并相关操作
- 用 `import_file` 导入 ChatGPT 附件
- 用 `bash` 运行白名单检查
- 用 `show_changes` 审查 diff
- 在 `.ai-bridge` 下写计划
- 为不能调工具的会话导出 context bundle

`read` 会返回由当前 connector principal 的精确完整文件快照支撑的四字符 `edit_tag`。有界快照缓存会在同一 CodexPro 进程的多个 HTTP server instance 之间共享，因此 `read` 后的 `edit` 不会因 transport 轮换而失效；不同认证 principal 和进程重启不会共享缓存。同一次 `edit` 中所有修改都使用原始显示行号。失败时应遵循 `error_code`、`recovery` 和 `retry_unchanged`，不要原样重试。

一两个普通读取，以及一次文件修改后仅接 `read` 或 `show_changes`，应直接调用相应工具。三个以上互不依赖的并行读取、文件修改后需要实际 Bash 验证，或明确需要续跑的流程，才适合使用一个合并后的 `batch`；不要连续发送多个很小的 batch。

包含 Bash 验证的内联 `batch` 默认保存为 `.codexpro-batches/` 下的普通 JSON 文件；其他内联 batch 默认一次性执行，除非显式传 `persist=true`。每个 workspace 保留最近创建、修改或运行的 20 个定义，并通过 Git 本地 `info/exclude` 排除。运行已有定义只会刷新其保留顺序，不会修改文件内容或 edit tag。操作失败后，读取返回的 `batch_path`，用普通标签式 `edit` 修改定义，再从失败的操作继续：

```text
batch(path=".codexpro-batches/7A3C.json", from="tests")
```

也可以使用从零开始的 `from_index`。如果上游源代码修改有误，先正常修复源文件，再从失败的测试或检查继续；成功的前缀不会重复执行。串行 batch 仍允许一次文件修改，随后运行白名单测试、类型检查、lint、build 或 Git 检查，再读取并调用 `show_changes`；并行 batch 仍只允许读取操作。`apply_patch` 只接受原始 Git unified diff，主要用于明确的多文件修改；普通单文件修改应优先使用标签式 `edit`。

## 多项目

一个 CodexPro 进程可以允许多个仓库。额外保存的 root 是轻量方案：

```bash
codexpro settings set --project ~/code/web --project ~/code/api
codexpro settings show
codexpro start
```

让 ChatGPT 对已允许的 root 执行 `open_workspace`。`open_current_workspace` 切回启动仓库。

如果需要让 ChatGPT 按项目 id 选择项目，或直接创建新项目，请使用持久化的命名目录：

```bash
cp projects.example.json ~/.config/codexpro/projects.json
codexpro start --projects-file ~/.config/codexpro/projects.json
```

使用 `open_workspace(project_id="web")` 打开一个目录项目，或一次解析多个 workspace handle：

```text
open_workspace(project_ids=["web", "api", "shared"])
```

重复 id 会被合并；所有 id 都会先完成验证，然后才打开任何请求的 workspace；数组第一项成为当前选中的主 workspace。多项目打开默认不返回文件树；显式提供 `max_files` 时，该总预算会分配给各 workspace。后续应复用返回的 `workspace_ids`。重复打开单个已知 workspace 是幂等的，并默认省略文件树；需要刷新时再显式传 `include_tree=true`。

在目录文件中加入一个或多个 `creationRoots`，用于指定可以容纳新项目、但自身不能被打开或 provision 为项目的目录：

```json
"creationRoots": [
  { "id": "projects", "label": "Projects directory", "root": "~/Projects" }
]
```

在 workspace write 模式下，connector 会暴露 `create_project`。新目录只能创建在所选 `parent_id` 的直接子目录中；`parent_id` 可以指向 creation root 或现有项目。建议优先使用 creation root，让各仓库保持同级。新项目会以原子方式写入目录文件，并立即可供 `open_workspace` 或 `create_workspace` 使用：

```text
create_project(project_id="scratch", parent_id="projects", source="empty")
create_project(project_id="new-api", parent_id="projects", source="git")
create_project(project_id="fork", parent_id="projects", source="git", repository="https://example.com/team/repo.git")
```

`source="git"` 且未提供 `repository` 时，会初始化 Git，并默认在 `main` 上创建一个空的初始提交。提供 `repository` 时会克隆，但不会递归克隆 submodule。本地克隆源必须位于允许的 root 内。原始空项目只适用于 direct-workspace 模式；隔离的 MCP worktree 模式要求项目由 Git 支持。

在只读、handoff、connection-test 模式下，或未配置持久化 projects file 时，项目创建工具不会暴露。Creation root 与项目的 id/path 必须唯一。如果 server 运行期间目录文件被外部修改，创建操作会 fail closed，要求重启，而不会覆盖外部修改。

两个 ChatGPT 账号或需要硬隔离时，用不同端口和 Server URL 跑两个 CodexPro 进程。

## 命令

```bash
codexpro setup
codexpro start
codexpro start --root /path/to/repo
codexpro doctor
codexpro connection-test
codexpro settings
codexpro inspect
codexpro review
```

常用模式：

```bash
codexpro start --no-bash
codexpro start --tool-mode minimal
codexpro start --tool-mode full
codexpro start --mode handoff
codexpro start --mode pro
codexpro start --headless
```

可选工具卡片：

```bash
CODEXPRO_TOOL_CARDS=1 codexpro start
```

## 公网 HTTPS

ChatGPT Web 需要 HTTPS：

```bash
codexpro start --tunnel cloudflare
codexpro ngrok --hostname your.ngrok-free.dev
codexpro stable --hostname codexpro.example.com --tunnel-name codexpro
codexpro tailscale --hostname your-device.your-tailnet.ts.net
codexpro start --tunnel none
```

稳定主机名请固定 token：

```bash
mkdir -p ~/.codexpro
openssl rand -hex 32 > ~/.codexpro/http-token
chmod 600 ~/.codexpro/http-token
```

客户端支持 header 时优先用 `Authorization: Bearer <token>`。`?codexpro_token=` 只是个人兼容回退。

## 安全默认

- 公网 tunnel 需要 CodexPro HTTP token（至少 24 bytes）
- 非 workspace write 模式不暴露写入工具
- 默认 safe bash
- 拦截 `.env`、密钥、`.git`、构建缓存等路径
- 附件导入只接受已批准 HTTPS 主机上的 ChatGPT Apps SDK 文件对象

公网暴露前先读 [SECURITY.md](SECURITY.md)。

## 更新

```bash
npm install -g codexpro@latest
codexpro --version
```

更新后重启 `codexpro start`。`~/.codexpro` 下的配置会保留。

## 文档

- [中文网站](https://rebel0789.github.io/codexpro/zh.html)
- [中文 FAQ](FAQ_ZH.md)
- [Security](SECURITY.md)
- [稳定 URL 指南](DOMAIN_SETUP.md)
- [Changelog](CHANGELOG.md)
- [Contributors](CONTRIBUTORS.md)
