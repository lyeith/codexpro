# CodexPro · lyeith 分支

在存放代码的机器上运行一个 MCP 服务，让本地 agent、局域网 MCP 客户端及可选的 ChatGPT 连接同一份项目目录。服务支持多个项目、文件检索与编辑、后台任务，以及跨 agent 会话保存计划和交接记录。

本文描述 **[lyeith/codexpro](https://github.com/lyeith/codexpro)**。npm 上的 `codexpro` 包和 `rebel0789.github.io/codexpro` 网站属于上游项目，不能用来判断本分支的功能或版本。请从本仓库源码安装；命令名仍为 `codexpro`。

[English](README.md) · [中文 FAQ](FAQ_ZH.md) · [MIT 许可证](LICENSE)

## 从源码安装

需要 Git、Node.js 和 npm。已验证 Node 22 和 24；包也支持 Node 20，但原生 SQLite 依赖可能需要编译。没有适用的预编译包时，请安装 Python 和平台对应的 C/C++ 编译工具。构建和运行时使用同一套 Node。

```bash
git clone --branch main https://github.com/lyeith/codexpro.git
cd codexpro
npm ci
npm run build
npm link
codexpro --version
git rev-parse --short HEAD
```

这里进入的是 **CodexPro 服务端的源码目录**，不是准备开放给 agent 的项目。`npm link` 会把命令链接到这个目录；请保留目录，源码变化后重新构建。它可能替换已有的全局 `codexpro` 命令。

不需要全局安装时，可直接运行：

```bash
node /absolute/path/to/codexpro/scripts/codexpro.mjs --help
```

将后面例子中的 `codexpro` 换成上述命令即可。`npm ci` 仍从配置的 npm registry 下载依赖；从源码安装不等于离线安装。

需要独立安装快照时，在源码目录运行 `npm pack`，然后用 `npm install -g /absolute/path/to/codexpro-VERSION.tgz` 安装它输出的确切文件。保留对应的 Git commit；`--version` 本身无法区分本分支和上游。

## 配置多个项目

推荐使用持久化项目目录，无需在每个项目中分别启动服务器。创建 `~/.config/codexpro/projects.json`（先创建父目录）：

```json
{
  "version": 1,
  "defaultProject": "web",
  "projects": [
    { "id": "web", "label": "网站", "root": "~/Projects/web" },
    { "id": "api", "label": "API", "root": "~/Projects/api" }
  ],
  "creationRoots": [
    { "id": "projects", "label": "新项目", "root": "~/Projects" }
  ]
}
```

这些目录必须存在于 **CodexPro 服务器** 上。`~` 指运行服务的账号，相对路径相对于目录文件所在位置解析。`defaultProject` 只选择默认项目，不限制其他已登记项目。不要同时使用 `--projects-file` 和 `--root`。

可选的 `creationRoots` 允许 agent 在指定父目录下创建直接子项目，无需将整个父目录开放为工作区。[projects.example.json](projects.example.json) 还包含 Git worktree 的配置例子。

交互式引导：

```bash
codexpro setup --projects-file "$HOME/.config/codexpro/projects.json"
```

自动化和服务启动用下面的 `start` 命令。配置 profile 与默认项目关联，每次显式传入目录文件可避免依赖启动时的工作目录。

直接工作区模式下，agent 用以下工具选择项目：

```text
list_projects()
open_workspace(project_id="web")
open_workspace(project_ids=["web", "api"])
```

后续操作复用返回的 `workspace_id`；编辑前先打开工作区以加载 `AGENTS.md`。一次最多打开 12 个项目。持久化目录和 `--write workspace` 还支持创建并登记项目：

```text
create_project(project_id="new-api", parent_id="projects", source="git")
create_project(project_id="scratch", parent_id="projects", source="empty")
```

`source="git"` 加上 `repository` 可克隆仓库；不提供时初始化新仓库。外部修改目录文件后，需要重启服务器才能继续创建项目。

单项目仍可使用 `codexpro setup --root /path/to/project`。重复 `--project` 也可添加其他目录，但持久化目录提供稳定 ID 和项目创建能力。

需要隔离 Git 工作区时，启动加 `--worktree-mode mcp`，并使用 safe 或关闭 Bash。agent 用 `create_workspace`、`open_workspace`、`release_workspace`、`remove_workspace` 管理生命周期；此模式不能使用 full Bash。下文的持久化 run 独立管理自己保留的 worktree 和 claim。

## 连接客户端

### 本机与局域网

本机客户端使用：

```bash
codexpro start --projects-file "$HOME/.config/codexpro/projects.json" \
  --tunnel none --host 127.0.0.1 --port 8787 --auth-mode static-token
```

局域网客户端使用服务器真实 IP：

```bash
codexpro start --projects-file "$HOME/.config/codexpro/projects.json" \
  --tunnel none --host 192.168.1.50 --port 8787 --auth-mode static-token
```

MCP 客户端选择 Streamable HTTP，URL 为 `http://192.168.1.50:8787/mcp`，请求头为 `Authorization: Bearer <token>`。没有指定 token 时 CLI 会生成；用 `--token-file /path/to/private-token` 可指定稳定凭证。非回环监听必须认证。监听 `0.0.0.0` 时，客户端仍需填写实际服务器 IP。

`--tunnel none` 无需 cloudflared。普通 HTTP 不加密，仅适用于可信网络；需要加密时在前面配置 TLS 反向代理。**当前限制：** `codexpro work` 和 managed `loop-handoff --mcp-url` 拒绝携带认证的非 localhost HTTP。其他 MCP 客户端可连接局域网 HTTP；上述 CLI 适配器需要 HTTPS 或 localhost。

本地 Codex 客户端的配置例子如下，token 需在客户端环境中提供。参见 [OpenAI MCP 配置文档](https://learn.chatgpt.com/docs/extend/mcp)。

```toml
[mcp_servers.codexpro]
url = "http://192.168.1.50:8787/mcp"
bearer_token_env_var = "CODEXPRO_HTTP_TOKEN"
```

本机客户端也可通过 stdio 启动进程：

```bash
codexpro-mcp --projects-file "$HOME/.config/codexpro/projects.json" --write workspace
```

每个 stdio 客户端启动独立服务器。多个客户端共享持久化 run 时应连接同一个 HTTP 服务；独立进程不要共用 job 或 work 存储。

### ChatGPT

通过公网 Server URL 连接时启动 HTTPS tunnel：

```bash
codexpro start --projects-file "$HOME/.config/codexpro/projects.json" \
  --tunnel cloudflare
```

在 ChatGPT 的 **Settings → Security and login** 开启 developer mode，再从 **Plugins → +** 添加 MCP 连接，填写包含 `/mcp` 的完整 Server URL。若使用 CLI 的 URL token 兼容方式，表单选择 **No Authentication / None**；CodexPro 仍会验证 URL 中的 token，请保密完整地址。

资格和工作区策略请以 [OpenAI 当前连接指南](https://developers.openai.com/plugins/deploy/connect-chatgpt) 为准。该指南也介绍私有服务器的 Secure MCP Tunnel；CodexPro 的 tunnel 参数不会配置这个独立服务。

Cloudflare quick URL 会变化。稳定公网地址可用 Cloudflare named tunnel、ngrok 或 Tailscale Funnel，参见 [DOMAIN_SETUP.md](DOMAIN_SETUP.md)。客户端支持时优先用 bearer 请求头。升级服务器后刷新客户端工具列表。

## 工具与权限

| 能力 | 说明 |
| --- | --- |
| 项目发现与创建 | `list_projects`、`open_workspace`、`create_project` |
| 文件检索 | `tree`、`read`、`search`、`ast_grep`，支持有界结果和编辑来源标记 |
| 修改与审阅 | `write`、`edit`、原生或 unified `apply_patch`、`show_changes`、`commit_changes` |
| 批处理 | 并行读取、串行修改，可保存定义并从失败步骤恢复 |
| 命令与日志 | 受监督的 Bash job、总时限、增量日志、本机日志文件检索 |
| 跨会话工作 | 可选的 manual/Ralph run、claim、todo、版本化文档与交接 |
| 变更记录 | 可选审计历史与独立 `/activity` 页面 |

默认 agent 配置为 `--write workspace --bash safe --tool-mode standard`。`safe` 运行允许列表中的验证命令，包括仓库脚本，因此项目本身需要可信。`--bash full` 具有服务器账号的任意 shell 权限，工作区路径检查不是 OS 沙箱。`--bash off` 关闭命令，`--write off` 隐藏工作区修改工具；工具显示模式不授予额外权限。

MCP 返回文字和结构化数据，**不显示 ChatGPT tool cards**；浏览器中的独立活动页面仍可使用。AI-Bridge 需开启 `--handoff-mode on` 或 handoff 模式。

`read`、`search` 和 `ast_grep` 能建立编辑来源，因此 shell `rg` 不完全替代这些检索工具。详细说明：[检索](docs/SEARCH.md)、[结构化检索](docs/AST_GREP.md)、[编辑与批处理](docs/HASH_EDIT_AND_BATCH.md)。

### 后台任务与大输出

`bash` 通常最多等待 120 秒，随后返回后台 job ID；`start_jobs` 立即启动后台任务，`jobs` 查询，`stop_jobs` 停止。转入后台不会重置总时限。

返回包含大小和截断信息。只看状态用 `output="none"`；片段用 `head`/`tail`；分页用 `incremental` 和返回的 cursor。旧参数 `full_output=true` 只返回**有界的日志开头**，不下载完整日志。

full Bash 可利用 `output_files` 与 `input_job_ids` 固定保留日志，再用服务器上的 `grep`、`rg`、`sed` 或脚本处理。默认单任务时限 25 分钟、捕获上限 8 MiB、完成日志保留 24 小时，同时受总数量和容量限制。详见 [JOBS.md](docs/JOBS.md)。

## 持久化 run 与 Ralph 循环

启动 HTTP 服务时加 `--work on`。允许 workspace 写入时提供 `work_status`、`work_manage`、`work_claim`、`work_update`；只读服务仅提供状态查询。普通 agent 无需创建 run。

project 是目录条目；workspace 是选中的代码目录；run 拥有独立保留的 Git worktree、计划和历史；iteration 是一次有时限的 agent 工作认领。

流程为：发现或创建 run → 规划 → 认领工作包 → 更新 todo 和交接 → 结束 iteration → 单独验证整个 run 是否达到验收要求。新 agent 可发现现有 run、查看认领与进行中的任务及最近变更。agent 崩溃后由服务器负责到期、撤销旧凭证和任务对账；不能确定的操作效果保留供恢复检查。

只有 `mode="ralph"` 会收到不足 30 分钟时继续领取工作的提示，时间由服务器单调时钟计算。完成、阻塞、停止请求和预算优先；manual 模式没有该提示。协调器本身不启动新的外部 agent 会话。

[WORK_RUNS.md](docs/WORK_RUNS.md) 说明文档和记忆、验收证据、崩溃恢复及 CLI 适配器。

## 状态、开发与更新

`CODEXPRO_HOME` 默认为 `~/.codexpro`，保存 profile、job、审计和 work 默认存储。开发服务器使用独立 home、端口和测试项目目录；检查显式目录参数，避免覆盖默认隔离：

```bash
CODEXPRO_HOME="$HOME/.codexpro-dev" codexpro start \
  --projects-file "$HOME/.config/codexpro/dev-projects.json" \
  --tunnel none --host 127.0.0.1 --port 8788 --work on --headless
```

先创建指向测试 checkout 的开发目录。`--audit metadata` 启用保留的操作历史和最近变更说明；`/activity` 与 `/healthz` 遵守服务器认证配置。没有历史记录不等于从未发生修改。服务部署参考 [deploy/README.md](deploy/README.md)。

在本分支干净的 `main` 源码目录中更新：

```bash
git pull --ff-only origin main
npm ci
npm run build
codexpro --version
git rev-parse --short HEAD
```

然后重启服务器。链接安装继续指向源码；快照安装需重新打包和安装本地 tarball。不要用 `npm install -g codexpro@latest` 更新本分支，它会选择上游 npm 包。

开发检查为 `npm test`、`npm run smoke`、`npm run stress`。`npm run dev:http -- --projects-file /absolute/path/to/projects.json` 直接运行 TypeScript HTTP 入口，不启动 tunnel、不运行引导，也不会自动重载。参见 [CONTRIBUTING.md](CONTRIBUTING.md)、[SECURITY.md](SECURITY.md) 和 [config.example.env](config.example.env)。继承的 npm 发布脚本不属于源码安装步骤。

本项目基于 [rebel0789/codexpro](https://github.com/rebel0789/codexpro)，遵循 [MIT 许可证](LICENSE)。本分支以 Git commit 标识实际运行代码。
