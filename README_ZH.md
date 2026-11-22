# CodexPro

CodexPro 通过 HTTP MCP 提供项目目录。代理可以读取和搜索文件、查看 Git 状态、按快照修改文件，以及运行和监控后台任务。Activity 页面显示近期变更和正在执行的任务。

## 从源码构建

在 Linux 上使用 Go 1.27.3 和 C 编译器（用于 SQLite）。服务器需要 Git 和 ripgrep。使用结构搜索时，在 PATH 上安装 ast-grep，或用 `CODEXPRO_AST_GREP_PATH` 指定其路径。

```sh
git clone https://github.com/lyeith/codexpro.git
cd codexpro/go
go build -o codexpro-go ./cmd/codexpro
```

请参考 [systemd 部署说明](deploy/README.md) 配置套接字激活和升级。

## 配置项目与客户端

复制 [示例项目目录](projects.example.json) 到私有配置目录，将每个项目的根路径设为服务器上的现有目录。

```sh
./codexpro-go \
  --projects-file "$HOME/.config/codexpro/projects.json" \
  --state-dir "$HOME/.codexpro-go/state" \
  --listen 127.0.0.1:7210 \
  --host-resources-helper=
```

在 SSD 主机上请配置 [任务资源助手](docs/HOST_JOB_RESOURCES.md)。为 Streamable HTTP MCP 客户端指定 `http://127.0.0.1:7210/mcp` 和 Authorization bearer 请求头。远程客户端应使用受信任的 HTTPS 地址。通过 `--principals-file` 配置命名凭据。迁移期间可启用 `--legacy-bridge` 以继续接受共享令牌。全部客户端切换后即可关闭。 [认证设置](go/internal/config/README.md#named-credentials)

调用 `list_projects` 后，使用返回的 `workspace_id` 读取文件。修改前调用 `open_workspace(project_id="web")` 加载项目的 [AGENTS 指令](docs/AGENTS_INSTRUCTIONS.md)。用 `jobs` 查看当前后台任务，用 `work_status` 查看已配置归档中的工作记录。Fleet 负责提供隔离的代理工作区。

## 开发

在 `go/` 中运行 `go test ./...`、`go test -race ./...` 和 `go vet ./...`。集成测试使用独立的项目目录和状态目录。 [贡献指南](CONTRIBUTING.md)
