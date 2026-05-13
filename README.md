# zed2api

将 Zed 编辑器的 LLM API 代理为 OpenAI / Anthropic 兼容接口的本地服务器。单文件 Go 二进制，内嵌 Web 管理界面。

## 功能

- OpenAI 兼容接口：`POST /v1/chat/completions`
- Anthropic 原生接口：`POST /v1/messages`
- 模型列表：`GET /v1/models`
- 多账号管理 + 自动故障转移
- SSE 流式输出
- 多模型供应商：Anthropic / OpenAI / Google / xAI
- 扩展思考 (thinking) 支持
- 内嵌 Web UI 管理界面
- HTTPS 代理支持（`HTTPS_PROXY` / `ALL_PROXY` 环境变量）
- API Key 鉴权（可选）
- Docker 一键部署

## 支持的模型

| 供应商 | 模型 |
|---------|------|
| Anthropic | claude-opus-4-6, claude-opus-4-5, claude-sonnet-4-5, claude-sonnet-4, claude-haiku-4-5 等 |
| OpenAI | gpt-5.2, gpt-5.1, gpt-5, gpt-5-mini, gpt-5-nano 等 |
| Google | gemini-3-pro-preview, gemini-2.5-pro, gemini-3-flash 等 |
| xAI | grok-4, grok-4-fast-reasoning, grok-code-fast-1 等 |

## Docker 部署（推荐）

### 1. 准备 accounts.json

参考 `accounts.example.json` 在服务器上创建账号文件：

```json
{
  "accounts": {
    "my-account": {
      "user_id": "123456",
      "credential": {"github_user_id": 123456, "github_user_login": "username", "access_token": "zed_token_here"}
    }
  }
}
```

### 2. 配置 .env

```bash
cp .env.example .env
```

编辑 `.env`：

```env
# accounts.json 所在的目录（不是文件路径，是目录）
ACCOUNTS_DIR=/root/myfile/zed2api

# 可选：API 鉴权 Key，不设置则完全开放
API_KEY=your-secret-key

# 可选：出站代理
# HTTPS_PROXY=http://127.0.0.1:7890
```

### 3. 启动

```bash
docker compose up -d
```

## 本地编译运行

需要 Go 1.25+ 和 Node.js。

```bash
# 安装 WebUI 依赖并编译
cd webui && npm install && npm run build && cd ..

# 编译二进制
go build -o zed2api .

# 启动服务
./zed2api serve 8000
```

## 账号配置

手动创建 `accounts.json`（参考 `accounts.example.json`），放在程序同目录或通过 `ACCOUNTS_FILE` 环境变量指定路径：

```bash
ACCOUNTS_FILE=/path/to/accounts.json ./zed2api serve
```

查看已加载账号：

```bash
./zed2api accounts
```

## API 鉴权

设置环境变量 `API_KEY` 后，所有 `/v1/*` 和 `/zed/*` 请求需要携带：

```
Authorization: Bearer <your-key>
```

或：

```
X-API-Key: <your-key>
```

Web UI 不需要鉴权。未设置 `API_KEY` 时完全开放。

## Claude Code 集成

```bash
export ANTHROPIC_BASE_URL=http://your-server:8000
export ANTHROPIC_AUTH_TOKEN=your-api-key   # 若未设置 API_KEY 则填 dummy
claude
```

## 代理设置

```bash
export HTTPS_PROXY=http://127.0.0.1:7890
./zed2api serve
```

## 项目结构

```
main.go        - 入口，CLI 命令
server.go      - HTTP 服务器，路由，中间件（CORS / 鉴权 / 日志）
stream.go      - SSE 流式代理，多账号故障转移
providers.go   - 多供应商请求构建 & 响应转换
accounts.go    - 账号管理，JSON 持久化
zed.go         - JWT Token 管理，upstream HTTP 调用，代理配置
webui/         - Vite + TypeScript Web UI（编译后嵌入二进制）
Dockerfile     - 3-stage 构建（node → golang:alpine → distroless）
docker-compose.yml
.env.example
accounts.example.json
```
