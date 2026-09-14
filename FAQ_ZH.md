# CodexPro 中文 FAQ · lyeith 分支

本文描述本分支 `main`，不代表上游 npm 包。安装、多项目和连接方式请先看 [中文 README](README_ZH.md)。

## 我应该用什么 ChatGPT 账号？

本地 agent 和普通 MCP 客户端不需要 ChatGPT 账号。连接 ChatGPT 时，需要账号和工作区允许自定义 MCP 连接，且当前模型支持工具调用。资格会变化，请查阅 [OpenAI developer mode 文档](https://developers.openai.com/api/docs/guides/developer-mode)。

CodexPro 提供工具，不提供模型或账号权限。

## 推荐安装方式是什么？

从 [lyeith/codexpro](https://github.com/lyeith/codexpro) 克隆源码，运行 `npm ci`、`npm run build`、`npm link`。不需要全局安装时可直接运行 `node /absolute/path/to/codexpro/scripts/codexpro.mjs`。具体步骤见 [中文 README](README_ZH.md)。

配置持久化项目目录后运行：

```bash
codexpro setup --projects-file "$HOME/.config/codexpro/projects.json"
```

服务端源码目录和开放的项目目录是两回事，无需进入每个项目重复启动服务器。npm registry 中的 `codexpro` 是上游包，不是本分支。

## 怎么更新 CodexPro？

在本分支干净的 `main` 源码目录运行：

```bash
git pull --ff-only origin main
npm ci
npm run build
codexpro --version
git rev-parse --short HEAD
```

然后以相同项目目录和状态配置重启服务。链接安装会使用重新构建的源码；快照安装需要重新打包和安装本地 tarball。保留 Git commit 以识别本分支构建，不能只看版本号。

## CodexPro 和网页版自带 Agent 有什么区别？

CodexPro 是运行在代码所在机器上的 MCP 服务，为已认证的客户端提供项目、文件、命令和审阅工具。ChatGPT 可以作为客户端，也可换成本地 agent 或其他 MCP 客户端。

它不附加到已有浏览器或 Codex 对话。开启 full Bash 后可以使用服务器账号执行任意命令。

## 怎么把 ChatGPT 附件导入仓库？

在 workspace write 模式下，CodexPro 会暴露 `import_file`。ChatGPT 需要传入 Apps SDK 文件对象：

```json
{
  "download_url": "https://...",
  "file_id": "file_...",
  "mime_type": "image/png",
  "file_name": "screenshot.png"
}
```

该参数通过 `_meta["openai/fileParams"]` 声明。CodexPro 只会从已批准的 ChatGPT/OpenAI 文件域名下载临时 HTTPS URL，遵守 `CODEXPRO_MAX_IMPORT_BYTES`，拒绝私网/回环重定向，并且只写入已允许的工作区。默认不允许覆盖。任意用户或模型自行提供的下载 URL 会被拒绝。

如果客户端没有同时提供 `download_url` 和 `file_id`，工具会返回 unsupported-reference 错误，并且不会创建任何文件。

## ChatGPT 里要打开什么设置？

按 [中文 README](README_ZH.md) 的 ChatGPT 连接步骤开启 developer mode 并添加 Server URL。只有使用完整 URL 携带 token 的兼容方式时才在表单选择 **No Authentication / None**；服务器仍然验证该凭证。客户端支持时优先用 bearer 请求头。

界面和资格以 [OpenAI 当前连接指南](https://developers.openai.com/plugins/deploy/connect-chatgpt) 为准。升级服务器后刷新客户端工具列表。

## CSP 要保持开启吗？

保留客户端正常安全设置。本分支不显示 ChatGPT tool cards 或 MCP 小组件，连接不需要关闭 CSP。独立的认证 `/activity` 浏览器页面仍然可用。

## CodexPro 会绕过速率限制吗？

不会。

CodexPro 不绕过、不提升、不合并、不转售、不修改 ChatGPT、Codex、OpenAI 或第三方模型限制。所有请求仍然通过你自己的 ChatGPT 会话，并受该账号当前限制约束。

它的价值在于 ChatGPT 和 Codex 是不同产品界面。某个工作流暂时不可用时，如果另一个你本来就有权限的界面仍可用，CodexPro 可以让它继续操作同一个本地仓库。

## CodexPro 提供或选择模型吗？

不提供。模型由外部 agent 或 ChatGPT 会话选择，并且需要支持 MCP 工具调用。

不能调用工具的客户端可使用手动上下文包：

```bash
codexpro pro-bundle --root /path/to/repo --copy
```

这会生成 `.ai-bridge/pro-context.md` 供规划和交接，不会赋予客户端工具调用能力。

## 为什么 Pro 账号也可能连不上某个模型？

账号权限和模型工具能力是两回事。

账号权限和具体模型界面的工具调用能力是两回事，而且可用范围可能变化。遇到不能调用 MCP 工具的界面时，用 `codexpro pro-bundle --copy` 导出上下文，再把计划交给本地代理执行。

## ChatGPT 能通过 CodexPro 看到什么？

ChatGPT 能看到工具显式暴露的工作区内容：

- `AGENTS.md`
- `.ai-bridge` 计划、状态、执行记录
- git status
- git diff
- 文件树和搜索结果
- 你让它读取的源码文件

普通文件工具遵守工作区路径限制；显式开启的 Codex history 工具另行提供本地历史。full Bash 可以访问服务器账号可访问的内容，不受普通文件工具路径检查约束。

## ChatGPT 可以编辑什么？

Normal coding 模式下，ChatGPT 可以在配置的工作区内写入和精确编辑文件。

默认会阻止：

- `.env`
- 私钥
- `.git`
- `node_modules`
- 生成目录和缓存目录
- symlink 逃逸
- 工作区外路径

如果你只想让 ChatGPT 规划，不想让它直接改源码，用 handoff 模式。

## CodexPro 能把 bash 绑定到某个会话 id 吗？

CodexPro 不能附加到、读取或复用某一个 Codex App 聊天会话或终端会话。

MCP 的 `bash` 工具是在你启动的 CodexPro 本地服务器进程里，针对配置的 workspace root 执行。MCP session id 只是 ChatGPT 和 CodexPro HTTP 服务器之间的传输状态，不是 Codex 会话 id。

但 CodexPro 可以要求 bash 调用带上匹配的本地 session 标签：

```bash
codexpro start --bash-session main --require-bash-session
```

之后 `bash` 调用必须包含 `session_id: "main"`。这能避免误触发到错误的 CodexPro 终端，但不是远程控制某个已有的 Codex App 聊天。

如果你显式开启，CodexPro 可以列出本地 Codex session id 和标题：

```bash
codexpro start --tool-mode full --codex-sessions metadata
```

它会读取 `~/.codex/sessions` 和 `~/.codex/archived_sessions` 下的本地 Codex JSONL 历史，返回 metadata 和 `codex resume <session-id>` 命令。只有需要有限长度 transcript 读取时才使用 `--codex-sessions read`。它不会附加到正在运行的 Codex App 聊天。

如果你正在 Codex 里工作，不希望 ChatGPT 触发 shell 命令，可以关闭 bash：

```bash
codexpro start --no-bash
```

如果只想让 ChatGPT 写计划，由 Codex 或其他本地 agent 执行：

```bash
codexpro start --mode handoff --no-bash
```

## 选择哪种 tunnel？

本机和局域网 MCP 客户端可使用 `--tunnel none`，通过 `--host` 指定接口。局域网监听需要认证，详见 [中文 README](README_ZH.md)。

公网 HTTPS 可选择 Cloudflare quick tunnel、Cloudflare named tunnel、ngrok 或 Tailscale Funnel。Quick Cloudflare URL 会变化；稳定地址需要相应 provider 配置。Tailscale Funnel 是公网暴露，不是仅限 tailnet 的端点。命令见 [DOMAIN_SETUP.md](DOMAIN_SETUP.md)。

## ChatGPT 创建 connector 时显示 “Something went wrong” 怎么办？

通常是 ChatGPT 无法访问公网 MCP URL。生成 `trycloudflare.com` URL 不代表 `cloudflared` 一直连通。

运行连接测试：

```bash
codexpro connection-test --projects-file "$HOME/.config/codexpro/projects.json"
```

这个模式保留 `read`、`tree`、`search` 和 `load_skill`，关闭文件写入和 bash，并记录请求是否到达本地 MCP endpoint。所有模式都不显示 tool cards。在 ChatGPT 的
`Settings -> Plugins` 创建 development plugin，粘贴完整 Server URL，
Authentication 选择 `No Authentication`。

- 没有 `POST /mcp received`：请求没有到达 CodexPro，检查 ChatGPT Plugins 页面和 tunnel。
- `POST /mcp -> 401`：请粘贴包含 `codexpro_token` 的完整 URL。
- `POST /mcp -> 2xx`：ChatGPT 已到达 CodexPro，MCP endpoint 也已响应。

URL token 只适合作为个人 connector 的兼容方式。共享或多用户生产部署必须使用 OAuth 或
`Authorization: Bearer <token>`。CodexPro 要求 token 至少 24 个字节，本地引导页加载后
会从浏览器地址中移除 token 参数，并限制重复失败的认证尝试。

测试期间保持 CodexPro 运行。Cloudflare quick tunnel 每次重启都会更换 URL。
如果 Cloudflare 返回 `530` / `Error 1033`，检查运行 `cloudflared` 的机器上的
DNS 或代理客户端 DNS 设置。

ChatGPT 现在在 Plugins 中管理 development app。浏览器错误
`Failed to execute 'removeChild' on 'Node'` 发生在 ChatGPT 页面中，早于任何
CodexPro MCP 请求。请在 Plugins 页面删除或重建旧条目，再使用当前 URL 重试；
CodexPro 无法修复浏览器端的旧条目。

## 能每天使用同一个 ChatGPT App URL 吗？

可以，使用稳定 hostname 和 token。在 `codexpro setup --projects-file /absolute/path/to/projects.json` 中保存 provider 配置，每次启动继续传同一个目录文件。Quick tunnel URL 是临时的；稳定地址命令见 [DOMAIN_SETUP.md](DOMAIN_SETUP.md)。

## quick mode 为什么每次都要改 URL？

Cloudflare quick tunnel 是一次性的临时地址。每次重新启动 tunnel，Cloudflare 会分配一个新的 `trycloudflare.com` URL。

如果你不想改 ChatGPT 设置，用 ngrok free dev domain 或 Cloudflare named tunnel。

## 同时跑两个仓库怎么办？

将它们登记到一个持久化项目目录，由一个服务器提供。agent 调用 `list_projects`，再用 `open_workspace(project_id="...")` 或 `open_workspace(project_ids=[...])` 选择项目，后续复用 workspace handle。切回默认项目时仍用 `open_workspace(project_id="...")`；`open_current_workspace` 仅在单项目服务中提供。

重复 `--project` 仍可添加其他根目录，但持久化目录提供稳定 ID 和项目创建能力。项目选择属于 MCP session，不保证与客户端会话一一对应，后续工具应显式传 workspace handle。

需要独立服务器时，分别使用不同端口、凭证、运行状态存储和项目 checkout。只有端口或 profile 不同不能隔离同一份源码上的写入。共享持久化 run 时连接同一个 coordinator。

## 多个 ChatGPT session 怎么避免互相覆盖？

项目选择按 MCP session 隔离。对于整文件 `write`，先读取共享文件，再把返回的 SHA-256 作为 `expected_sha256` 传入。对于 `edit`，使用 `read` 返回的四字符 `edit_tag`；CodexPro 会将它解析为当前认证 connector principal 的精确完整快照。有界缓存会在同一进程的多个 HTTP server instance 之间共享，因此 transport 轮换不会破坏紧接着的 read/edit；不同 principal 和进程重启仍然隔离。系统会拒绝过期内容、标签碰撞，以及未曾显示的行范围。新文件采用原子替换；已有文件原位更新，以保留与 inode 绑定的元数据和硬链接。

这能防止旧内容静默覆盖新内容，但不会把 CodexPro 变成协同 merge server。大范围重叠修改仍建议使用独立 worktree。

后台运行或交给 service manager 时，使用 `codexpro start --headless`。它不会提问、访问剪贴板或打开浏览器；会用 `CODEXPRO_READY` 报告就绪，HTTP runtime 意外退出时 launcher 会以非零状态退出。

## 哪个网站和发布属于本分支？

请使用 [lyeith/codexpro](https://github.com/lyeith/codexpro) 及其 README。`rebel0789.github.io/codexpro` 和 npm 上的 `codexpro` 属于上游。仓库中继承的营销网页和发布清单是历史参考，不是本分支的安装或发布流程。

## CodexPro 是否违反服务条款？

CodexPro 使用 ChatGPT 的官方 Plugins + MCP 接入路径，让你自己的 ChatGPT 会话连接到你自己的本地工具。Developer mode 只是创建自定义插件所需的设置开关。

它不绕过限制，不抓取隐藏接口，不共享账号，不转售模型，不伪造请求来源，也不把第三方模型包装成别的模型。

用户仍然需要遵守 ChatGPT、Codex、OpenAI 和任何第三方服务的条款。

## CodexPro 生产环境安全吗？

CodexPro 是本地开发桥，不是操作系统级沙箱。

只在你信任的仓库里使用。公网 tunnel 保持 token auth 开启。保持 safe bash，除非你明确知道为什么需要 full bash。公网暴露前先读 [SECURITY.md](SECURITY.md)。

## 保存的设置在哪里？

工作区配置保存在：

```text
~/.codexpro/profiles/
```

管理命令：

```bash
codexpro settings
codexpro settings list
codexpro settings delete --yes
```

显示设置时，保存的 token 会被打码。

## CodexPro 能帮助 ChatGPT 维持上下文吗？

可以保存显式文档与上下文包，不依赖模型的隐藏记忆。普通项目规则放在 `AGENTS.md`，AI-Bridge 的计划、决策和结果放在 `.ai-bridge/`。

需要跨 agent 会话管理工作时，开启 `--work on`：run 保留计划、todo、版本化交接文档、iteration、claim 与操作证据；新 agent 可通过 `work_status` 发现并恢复已有工作。崩溃后的 claim 到期和恢复由服务器负责。只有 Ralph 模式有基于服务器时钟的 30 分钟继续工作提示。详见 [WORK_RUNS.md](docs/WORK_RUNS.md)。
