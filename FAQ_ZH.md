# CodexPro 常见问题

## 在哪里安装？

从 Go 模块构建 `codexpro-go`，并在保存项目文件的机器上运行它。一个项目目录可以提供多个项目。 [安装说明](README_ZH.md#从源码构建)

## 客户端如何认证？

客户端通过 HTTP MCP 发送 bearer 凭据。使用私有 Plans Access 策略配置命名凭据，迁移期间可使用共享令牌。个人代理可读取获授权的项目。 [认证设置](go/internal/config/README.md#named-credentials)

## 如何设置固定域名？

将 HTTPS 反向代理或隧道指向 CodexPro 监听地址，并在客户端配置 `/mcp` 路径。 [域名配置](DOMAIN_SETUP.md)

## 服务重启后如何查看任务？

重新初始化 MCP 会话，用 `jobs` 查看已接受的后台任务。配置了工作归档时，可用 `work_status` 查看历史记录。 [后台任务](docs/JOBS.md) · [工作状态](docs/WORK_RUNS.md)
