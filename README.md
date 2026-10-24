# kiro2api

Anthropic Messages 兼容网关。后端是一池 Kiro 账号和 Claude Messages API 账号，同一套优先级、均衡和分组调度。

从 [kiro.rs](https://github.com/FoyonaCZY/kiro.rs) 迁移而来，后端改用 Go 重写，管理界面沿用原来的 React 版本。`config.json`、`credentials.json`、`accounts.sqlite`、`usage.sqlite`、`access.sqlite` 的格式都没变，可以直接接管原来的数据目录。

## 功能

- `/v1/messages`、`/v1/messages/count_tokens`、`/v1/models`；`/cc/v1/messages` 缓冲到流结束，用 `contextUsageEvent` 的准确值写 `message_start`
- Kiro 端点：对齐 1.4.26，对话和 MCP 都打 `runtime.{region}.kiro.dev`。凭据上的 `krs` / `ide` 名字还保留，主机已经是同一个；Social / IdC 刷新、`invalid_grant` 立即停用
- Claude Messages API 账号：请求和响应原样双向透传；同一请求里每个账号只试一次，超时和 5xx 只换号
- 接入 Key 和调度分组；限流冷却、连续失败停用、全组自动禁用后自愈
- 模拟 prompt cache 计费（按接入 Key 隔离）；单价、账号人民币成本和成本倍率，改价后历史按新价重算；失败和断流请求不计费
- 凭据存 `accounts.sqlite`，管理端修改立即生效；凭据级 SOCKS5 / HTTP 代理，`direct` 表示直连

## 本地运行

```bash
cd admin-ui && pnpm install --frozen-lockfile && pnpm build && cd ..
go build -o bin/kiro2api ./cmd/kiro2api
cp config.example.json config.json   # 改 apiKey / adminApiKey
./bin/kiro2api -c config.json --credentials credentials.json
```

管理界面在 `http://127.0.0.1:8990/admin`。管理界面必须先构建，Go 编译时会把 `admin-ui/dist` 嵌进二进制。

数据文件都放在 `credentials.json` 所在目录：`accounts.sqlite` 空的时候从 `credentials.json` 导入一次，之后以数据库为准，JSON 留作备份。

## 测试

```bash
go vet ./... && go test ./...
```

## 部署

`Deploy` 工作流只能在 Actions 页面手动运行，需要仓库 Secrets：`DEPLOY_HOST`、`DEPLOY_USER`、`DEPLOY_PASSWORD`。

它在 `103.245.231.162` 上启动 `kiro2api`，数据目录是 `/opt/kiro2api/config`。这台机器已有 nginx，`https://kiro.skd.moe` 反代到 `127.0.0.1:8990`。第一次部署前，这个目录里要已经有 `config.json`。

## 与 kiro.rs 的差异

- `tlsBackend` 配置项不再使用，统一用 Go 标准库 TLS
- 客户端先断开的流式请求记为 499（不计费），原来不记录
- 非流式响应里遇到损坏帧会跳过继续解析，原来会停在损坏帧
- 日志不再打印任何一段 `apiKey`
