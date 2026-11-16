<div align="center">

# KHA

**Kimi Hosted Agents 的官方 Go SDK 与 CLI**

[English](../../README.md) · [简体中文](README.md) · [产品文档](https://platform.kimi.com/docs/hosted-agents/overview) · [API 参考](https://platform.kimi.com/docs/api-reference) · [Go Package 参考](https://pkg.go.dev/github.com/MoonshotAI/kha)

[![CI](https://github.com/MoonshotAI/kha/actions/workflows/ci.yml/badge.svg)](https://github.com/MoonshotAI/kha/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/MoonshotAI/kha.svg)](https://pkg.go.dev/github.com/MoonshotAI/kha)
[![GitHub Release](https://img.shields.io/github/v/release/MoonshotAI/kha)](https://github.com/MoonshotAI/kha/releases)
[![Go 1.27+](https://img.shields.io/badge/go-1.27%2B-00ADD8?logo=go&logoColor=white)](https://go.dev)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue)](../../LICENSE)

</div>

**KHA** 是 [Kimi Hosted Agents](https://platform.kimi.com/docs/hosted-agents/overview) 的官方 Go SDK 与命令行工具（CLI）。
Kimi Hosted Agents 是面向长程与异步任务的托管 Harness 服务（Harness as a Service），让你在云端沙箱中可靠地运行、观测和恢复 AI Agent 任务。

无论你是在 Go 后端服务中集成 Agent 工作流、在 Shell 脚本和 CI 流水线中自动化执行任务，还是让 AI 编程助手操作远端沙箱，KHA 都提供了一致的开发体验：

- **零第三方依赖的 Go SDK**（`github.com/MoonshotAI/kha`）：基于公共 OpenAPI 契约生成强类型模型与资源客户端，并内置 `iter.Seq2` 分页迭代器、SSE 事件流断线重连与过期 cursor 回补、异步资源等待器（Waiter）、感知幂等性的重试策略以及 Webhook 签名校验。
- **可脚本化的 CLI**（`kha`）：终端默认输出易读的表格，脚本下支持精确的 JSON/YAML 输出、基于 GJSON 的字段提取（`--transform`）、实时事件流跟踪（`attach` 与 `events --watch`）、交互式 YAML 编辑（`edit`），以及供 AI 编程助手直接调用的内置技能包（`kha install-skill`）。

---

## 核心概念

如果你刚开始接触 Kimi Hosted Agents，先了解以下四个核心概念就能快速理清调用主线：

1. **Agent**（`agent_...`）：定义助手的「能力蓝图」，包括模型、系统提示词（system prompt）、内置或 MCP 工具集、Skill、Plugin 以及多智能体协作名册（multiagent roster），每次修改都会生成不可变的新版本。
2. **Environment**（`env_...`）：代码与工具的「执行环境」，可以是托管云端沙箱（支持预装 `apt`、`pip`、`npm`、`go`、`cargo` 依赖包、执行初始化脚本及配置出站网络规则），也可以是接入自有机器的 `self-hosted` 工作队列。
3. **Session**（`sesn_...`）：一次有状态、持久化的「会话与工作区」，将一个 **Agent** 与一个 **Environment** 绑定在一起，并可挂载上传的 **File**、保存凭据的 **Vault** 以及共享的 **Memory Store**；任务按轮次（turn）在云端运行，即使本地客户端断开连接也不会中断。
4. **Events 与 Artifacts**：用户输入、模型思考、工具调用、Agent 回复以及状态切换都会追加写入 Session 的 **Event** 日志；Agent 生成的交付文件则可以发布为带版本的 **Artifact**（`art_...`），或保存在 Session 文件系统中供随时下载。

> **说明**：KHA 是托管平台的客户端，不是本地 Agent 运行时；模型推理、沙箱编排、Session 状态持久化以及平台侧故障恢复均在 Kimi Hosted Agents 云端执行。

---

## 安装

### 前置条件

- 拥有已开通 Hosted Agents 权限的 [Kimi 开放平台](https://platform.kimi.com/console)账号，并在[组织管理 - API 管理](https://platform.kimi.com/console/api-keys)中创建 API Key。
- 使用 Go SDK 或从源码编译 CLI 需要 **Go 1.27 或更高版本**；直接安装预编译的 CLI 二进制不需要安装 Go。

### 安装 Go SDK

```sh
go get github.com/MoonshotAI/kha@latest
```

### 安装 CLI

#### macOS 与 Linux

```sh
curl -fsSL https://ha-static.moonshot.cn/kha/install.sh | sh
export PATH="$HOME/.local/bin:$PATH"
kha version
```

安装脚本会自动识别操作系统与 CPU 架构、校验 SHA-256 摘要，并默认将 `kha` 安装到 `~/.local/bin`。
请将 `~/.local/bin` 添加到你的 shell 配置文件（如 `~/.zshrc` 或 `~/.bashrc`），以便在新打开的终端中直接使用 `kha`。

#### Windows (PowerShell)

```powershell
$installer = Join-Path ([IO.Path]::GetTempPath()) 'kha-install.ps1'
Invoke-WebRequest https://ha-static.moonshot.cn/kha/install.ps1 -UseBasicParsing -OutFile $installer
& $installer
$env:Path = "$(Join-Path $env:LOCALAPPDATA 'kha\bin');$env:Path"
kha version
```

默认安装路径为 `%LOCALAPPDATA%\kha\bin`。
如需在新打开的 PowerShell 窗口中持久生效，请将该目录添加到系统的用户 `Path` 环境变量。

#### 从源码或 GitHub Releases 安装

```sh
go install github.com/MoonshotAI/kha/cmd/kha@latest
```

你也可以直接从 [GitHub Releases](https://github.com/MoonshotAI/kha/releases) 下载适用于 macOS、Linux 和 Windows（`amd64` 与 `arm64`）的预编译压缩包与 `checksums.txt`。

<details>
<summary><strong>自定义 CLI 安装选项（安装目录、固定版本与下载镜像）</strong></summary>

- **自定义安装目录**：在 macOS/Linux 上设置 `KHA_INSTALL_DIR=/custom/dir`，或在 Windows 上传入 `-InstallDir C:\custom\dir`。
- **安装指定版本**：在 macOS/Linux 上设置 `KHA_VERSION=0.X.Y`，或在 Windows 上传入 `-Version 0.X.Y`（已安装 CLI 时也可运行 `kha upgrade --version 0.X.Y`）。
- **切换下载源（`KHA_RELEASE_BASE_URL`）**：默认下载源为 `https://ha-static.moonshot.cn/kha`；如需直接从 GitHub Releases 安装或升级指定版本，可将 `KHA_RELEASE_BASE_URL` 设为 `https://github.com/MoonshotAI/kha/releases/download`，同时显式指定版本号（`KHA_VERSION=0.X.Y` 或 `kha upgrade --version 0.X.Y`）；自动解析最新稳定版依赖默认下载源上的 `stable.txt`，因此日常自动更新检查请保持默认地址；由于二进制和校验文件均从该地址获取，请仅使用可信的 HTTPS 镜像源。

</details>

---

## 快速开始

### 1. 使用 CLI 运行第一个任务

首先登录保存 API Key（默认安全存储在系统钥匙串中），并验证连接：

```sh
kha login
kha whoami
```

> `kha login` 采用无回显提示读取密钥；在 CI 或没有系统钥匙串的服务器上，请通过密钥管理器注入环境变量 `export KIMI_API_KEY="sk-..."`，切勿将 API Key 直接写在命令行参数、Shell 历史或代码仓库中。

接下来，选择已有的 Agent 和 Environment，或者直接创建一套最简配置：

```sh
# 方式 A：查看并复用已有的 Agent 和 Environment
kha agents list
kha environments list

# 方式 B：从零创建一个默认云端 Environment 和示例 Agent
export ENVIRONMENT_ID=$(kha environments create \
  --name "Quickstart runtime" \
  --wait \
  --transform id -r)

export AGENT_ID=$(kha agents create \
  --name "Quickstart assistant" \
  --model kimi-k2.5 \
  --system "你是一位简洁清晰的工程助手。" \
  --transform id -r)
```

确认 Environment 构建就绪后，创建 Session 并发送消息：

```sh
kha environments wait-ready "$ENVIRONMENT_ID"

export SESSION_ID=$(kha sessions create \
  --agent "$AGENT_ID" \
  --environment "$ENVIRONMENT_ID" \
  --title "Hello KHA" \
  --transform id -r)

kha sessions send "$SESSION_ID" \
  "请用三句话介绍 Kimi Hosted Agents。" \
  --wait --timeout 10m
```

`kha sessions send --wait` 会阻塞等待当前轮次结束，并返回本轮的结束原因（`stop_reason`），而不是直接打印回答正文。
要查看完整的执行事件或提取 Agent 的文本回答，运行：

```sh
# 查看 Session 的全部持久化事件
kha sessions events "$SESSION_ID" --all

# 仅提取 Agent 回复中的纯文本内容
kha sessions events "$SESSION_ID" --all \
  --transform '#(type=="agent.message")#.data.content.#(type=="text")#.text|@flatten' -r
```

**运行任务时的常见操作提示：**

- **实时流式查看**：在另一个终端运行 `kha sessions attach "$SESSION_ID"` 可以实时流式打印输出（加上 `--since now` 可跳过历史重放，只看新事件）。
- **终端内对话**：`kha sessions attach "$SESSION_ID" --since now --input` 会把输入的每一行作为 `user.message` 发送，同时继续流式输出；轮次进行中发送的消息进入输入队列（`kha sessions pending` 可查看与撤回）。也可以通过管道输入：最后一行发送后，Session 回到 idle 时 `attach` 自动退出。
- **本地断开与远端中断的区别**：在 `attach` 时按 `Ctrl-C`，或者 `send --wait` 触发本地 `--timeout`，都只会结束本地 CLI 的等待，云端 Session 仍在继续运行；决定重试前请先用 `kha sessions get "$SESSION_ID"` 查看状态，如果确实需要停止云端正在执行的轮次，请运行 `kha sessions interrupt "$SESSION_ID"`。

---

### 2. 使用 Go SDK 运行任务

设置环境变量中的 API 凭据和资源 ID：

```sh
export KIMI_API_KEY="sk-..."
export AGENT_ID="agent_..."
export ENVIRONMENT_ID="env_..."
```

使用 Go SDK 完整走通「等待环境就绪 → 创建 Session → 发送消息并等待完成 → 读取回复」：

```go
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/MoonshotAI/kha"
)

func main() {
	ctx := context.Background()

	// kha.New("") 在传入空字符串时自动读取 KIMI_API_KEY，
	// 并配置默认服务地址、Kimi-Api-Version 请求头和重试策略。
	client, err := kha.New("")
	if err != nil {
		log.Fatal(err)
	}

	// 1. 等待 Environment 快照构建就绪。
	envID := os.Getenv("ENVIRONMENT_ID")
	if _, err := client.Environments.WaitForReady(ctx, envID); err != nil {
		log.Fatal(err)
	}

	// 2. 创建绑定该 Agent 与 Environment 的 Session。
	sessionResult, err := client.Sessions.Create(ctx, kha.CreateSessionHTTPRequest{
		Body: &kha.CreateSessionRequest{
			AgentID:       os.Getenv("AGENT_ID"),
			EnvironmentID: envID,
			Title:         new("KHA Go SDK 快速开始"),
		},
	})
	if err != nil {
		log.Fatal(err)
	}
	sessionID := sessionResult.Body.ID

	// 3. 发送用户消息并等待当前轮次结束。
	turn, err := client.Sessions.SendMessageAndWait(
		ctx,
		sessionID,
		"请用三句话介绍 Kimi Hosted Agents。",
	)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("Turn stopped with reason:", turn.StopReason)

	// 4. 遍历持久化的 Session 事件并打印 Agent 的文本回复。
	events := client.Sessions.AllEvents(ctx, kha.ListSessionEventsRequest{
		ID: sessionID,
	})
	for event, err := range events {
		if err != nil {
			log.Fatal(err)
		}
		if message := event.AsAgentMessageEvent(); message != nil {
			for _, part := range message.Data.Content {
				if text := part.AsTextContentBlock(); text != nil {
					fmt.Println(text.Text)
				}
			}
		}
	}
}
```

更多经过编译检查的可运行示例见 [`example_test.go`](../../example_test.go)。

---

## Go SDK 开发指南

### 初始化客户端

绝大多数应用推荐使用 `kha.New`，它会自动配置生产环境地址（`https://api.moonshot.cn`）、`Kimi-Api-Version` 请求头（`kha.DefaultAPIVersion`）、Bearer 认证以及产品级重试策略：

```go
// apiKey 传空字符串时自动读取环境变量 KIMI_API_KEY。
client, err := kha.New("",
	kha.WithBaseURL("https://api.moonshot.cn"),
	kha.WithMaxRetries(3),
)
```

如果你需要完全显式地控制底层配置（不启用环境变量回退或产品默认项），可以使用生成的构造函数 `kha.NewClient`：

```go
client, err := kha.NewClient("https://api.moonshot.cn",
	kha.WithBearerToken("sk-..."),
	kha.WithHeader("Kimi-Api-Version", kha.DefaultAPIVersion),
	kha.WithMaxRetries(0),
)
```

> **提示**：通过 `kha.WithHTTPClient(hc)` 自定义 `http.Client` 时，避免设置过短的全局 `http.Client.Timeout`；全局超时会同时作用于长连接 SSE 事件流和大文件下载，建议通过每个请求的 `context.WithTimeout` 控制单次调用超时。

### 请求、响应 `HTTPResult[T]` 与可选字段

所有普通单次操作都接收生成的请求结构体，并返回 `(kha.HTTPResult[T], error)`。
解码后的业务模型位于 `result.Body`，同时保留 HTTP 状态码和响应头：

```go
result, err := client.Agents.Get(ctx, kha.GetAgentRequest{ID: "agent_..."})
if err != nil {
	return err
}
fmt.Println("Agent:", result.Body.Name)
fmt.Println("Status:", result.StatusCode, "Request ID:", result.Header.Get("Msh-Request-Id"))
```

可选字段使用指针（`*T`）；需要区分「缺省」「显式 `null`」「具体值」的三态更新字段使用 `kha.Optional[T]`（`kha.NewOptional(v)` 设值，`SetNull()` 置空）。下载接口返回 `kha.HTTPResult[io.ReadCloser]`，读完请 `Close()`。

### 分页查询（`List*` 与 `All*`）

所有分页资源都提供两种访问方式：

- `List*` 请求单页数据，返回 `result.Body.Items` 和下一页的 `result.Body.NextPageToken`。
- `All*` 返回 Go 标准库的 `iter.Seq2[*T, error]` 迭代器，在循环过程中按需拉取后续页面：

```go
for agent, err := range client.Agents.All(ctx, kha.ListAgentsRequest{}) {
	if err != nil {
		return err
	}
	fmt.Println(agent.ID, agent.Name)
}
```

在循环中提前 `break` 会立即停止后续分页请求；如果某一页请求失败，迭代器会返回一次该错误并结束遍历。

### 实时事件流与断线自动恢复

- **有限历史**（`Sessions.AllEvents` / `Sessions.AllThreadEvents`）：从旧到新遍历已持久化事件，翻完即停。
- **实时 SSE 流**（`Sessions.Events` / `Sessions.ThreadEvents`）：默认先回放保留历史，再持续推送新事件：

```go
for event, err := range client.Sessions.Events(ctx, "sesn_...", kha.WithDeltas(true)) {
	if err != nil {
		return err
	}
	// 处理 agent.delta 预览、agent.message、工具调用与状态变化。
}
```

事件流自动处理心跳、去重、`Last-Event-ID` 断线重连与过期 cursor 的历史回补；恢复选项见 [pkg.go.dev](https://pkg.go.dev/github.com/MoonshotAI/kha)。

### 异步等待器（Waiter）与安全重试

SDK 为常见的异步操作内置了开箱即用的等待器：

| 方法 | 等待机制 | 可选配置 |
| --- | --- | --- |
| `client.Sessions.SendMessageAndWait(ctx, sessionID, text, opts...)` | 发送消息并基于 Session 事件流等待本轮回到 `idle`（若运行失败则返回 `*kha.SessionError`）。 | `kha.WithTimeout(d)`（默认 `10m`；事件驱动，忽略 `WithInterval`） |
| `client.Environments.WaitForReady(ctx, envID, opts...)` | 轮询 Environment 直到快照构建状态变为 `ready`（失败返回 `*kha.EnvironmentWaitFailedError`）。 | `kha.WithEnvironmentTimeout(d)`、`kha.WithEnvironmentInterval(d)` |
| `client.Vaults.WaitOAuthSession(ctx, vaultID, sessionID, opts...)` | 轮询 MCP OAuth 授权会话直到变为 `authorized`（失败返回 `*kha.WaitFailedError`）。 | `kha.WithTimeout(d)`、`kha.WithInterval(d)` |
| `client.Triggers.WaitForTerminal(ctx, triggerID, runID, opts...)` | 轮询 Trigger 运行记录直到进入 `completed`、`failed` 或 `skipped` 终态。 | `kha.WithTimeout(d)`、`kha.WithInterval(d)` |

### 运行自定义工具

Agent 可以声明由你的应用执行的 `custom` 工具。`Sessions.RunTools` 跟随 Session 事件流，对每个 `agent.custom_tool_use` 调用执行已注册的 handler，并把结果作为 `user.custom_tool_result` 写回：

```go
err := client.Sessions.RunTools(ctx, sessionID,
	kha.WithCustomTool("get_weather", func(ctx context.Context, call *kha.AgentCustomToolUseEvent) ([]*kha.ToolResultContent, error) {
		city, _ := call.Data.Input["city"].(string)
		return []*kha.ToolResultContent{kha.NewToolResultContentTextContentBlock(&kha.TextContentBlock{
			Type: kha.TextContentBlockTypeText, Text: lookupWeather(city),
		})}, nil
	}),
	// 可选：对权限策略要求确认的工具调用做出决定。
	kha.WithToolConfirmation(func(ctx context.Context, call *kha.AgentToolUseEvent) (kha.ToolConfirmation, error) {
		return kha.ToolConfirmation{Allow: call.Data.ToolUse.Name != "bash", DenyMessage: "not allowed"}, nil
	}),
)
```

- handler 返回的错误会作为错误结果（`is_error: true`）回写给 Agent；没有注册 handler 的工具调用同样回写错误，Agent 不会一直等待。
- 启动时先读取 Session 历史，只回答尚无结果的调用；即使断线重连，同一个调用也不会执行两次。
- 每次回写都带有由调用事件 ID 派生的 `Idempotency-Key`，写入丢失响应时可以安全重试，不会重复回答。
- `RunTools` 持续运行到 `ctx` 结束（返回 `ctx.Err()`），或事件流、确认回调、回写出错为止；未配置 `WithToolConfirmation` 时，等待确认的调用留给其他客户端处理。

重试机制（安全读、幂等写、in-flight 409 重放）见 [pkg.go.dev](https://pkg.go.dev/github.com/MoonshotAI/kha)。

### 错误处理

契约声明的非 2xx 响应统一返回 `*kha.Error`（即 `*kha.ResponseError[*kha.APIError]`），其 `Error()` 文本会自动包含 HTTP 状态码以及服务端返回的 `type`、`message`、`param` 和 `request_id`；未声明的状态码或无法解码的响应则返回同样实现 `kha.HTTPError` 的 `*kha.UnexpectedResponseError`。
你可以按需提取不同层级的错误信息：

```go
result, err := client.Agents.Get(ctx, kha.GetAgentRequest{ID: "agent_..."})
if err != nil {
	// 1. 使用状态辅助函数快速判断常见分支
	if kha.IsNotFound(err) {
		log.Println("Agent 不存在")
		return
	}

	// 2. 一步获取解码后的 API 错误模型（type、message、param、request_id）与 HTTP 状态码
	if apiErr, ok := errors.AsType[*kha.Error](err); ok {
		log.Printf("HTTP %d [%s]: %s",
			apiErr.StatusCode(),
			apiErr.Body.Error.Type,
			apiErr.Body.Error.Message,
		)
	} else if httpErr, ok := errors.AsType[kha.HTTPError](err); ok {
		// 3. 获取任意 HTTP 响应错误（含 *kha.UnexpectedResponseError）的状态码与响应头
		log.Printf("HTTP %d (request id: %s)",
			httpErr.StatusCode(),
			httpErr.Header().Get("Msh-Request-Id"),
		)
	}
}
```

内置的状态判断函数包括：`kha.IsInvalidRequest`（`400`）、`kha.IsUnauthorized`（`401`）、`kha.IsForbidden`（`403`）、`kha.IsNotFound`（`404`）、`kha.IsConflict`（`409`）、`kha.IsRateLimited`（`429`）和 `kha.IsServerError`（`5xx`）；`kha.ErrorDetails(err)` 则可同时从 HTTP `*kha.Error` 或 SSE `*kha.StreamError` 中提取 `*kha.ErrorObject`。

### 链路追踪与传输层中间件

根 SDK package 不引入第三方依赖，链路追踪和响应侧中间件都通过 `kha.WithHTTPClient` 接入。每个响应都带有 `Msh-Request-Id` 响应头（CLI 的错误输出也会附带），反馈问题时请提供它。

如果希望调用方的 trace 与平台日志对上，可以用 [`otelhttp`](https://pkg.go.dev/go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp) 包装 transport。平台会读取传播过来的 `traceparent` 请求头，并把其中的 trace ID 作为 `Msh-Request-Id` 返回：

```go
import "go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

client, err := kha.New("", kha.WithHTTPClient(&http.Client{
	Transport: otelhttp.NewTransport(http.DefaultTransport),
}))
```

日志、指标等响应侧逻辑可以用自定义的 `http.RoundTripper` 包装 transport。使用 `kha.New` 时，产品重试策略位于你的 transport 之上，因此包装层能观察到每一次实际尝试：

```go
type logTransport struct{ next http.RoundTripper }

func (t logTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	start := time.Now()
	resp, err := t.next.RoundTrip(req)
	if err == nil {
		log.Printf("%s %s -> %d in %s (request id: %s)", req.Method, req.URL.Path,
			resp.StatusCode, time.Since(start), resp.Header.Get("Msh-Request-Id"))
	}
	return resp, err
}

client, err := kha.New("", kha.WithHTTPClient(&http.Client{
	Transport: logTransport{next: http.DefaultTransport},
}))
```

`kha.New` 会复制传入的 `http.Client`，不会修改它。不符合 API 契约的响应返回 `*kha.UnexpectedResponseError`，其中保留最多 1 MiB 的原始 body 供排查。

### 嵌入 self-hosted worker

`worker` 与 `tools` 包支持在进程内运行自托管环境 worker，替代 shell 调用 `kha worker poll`。poll/run 生命周期见 [pkg.go.dev](https://pkg.go.dev/github.com/MoonshotAI/kha)。

### 校验 Trigger Webhook 签名

Trigger webhook 通知使用 Vault 中 `webhook_secret` 按 standard-webhooks HMAC-SHA256 签名。用原始请求体验签：

```go
event, err := kha.VerifyWebhookSignature(secret, body, r.Header)
if err != nil {
	// 拒绝该请求。
}
```

已验签的 payload 可用 `kha.ParseWebhookEvent` 解码为类型化的 `TriggerWebhookEvent`。

---

## CLI 使用指南：终端交互、脚本自动化与 AI 编程助手

### 常用命令与离线参考文档

所有资源命令遵循统一结构：`kha <resource> <verb> [flags]`。

```sh
# 查阅与当前二进制版本完全匹配的内置离线文档（无需联网）
kha docs
kha docs sessions
kha docs sessions send

# 在 $KHA_EDITOR / $EDITOR 中以 YAML 交互式编辑 Agent 或 Environment
kha agents edit "$AGENT_ID"

# 生成 Shell 自动补全脚本（支持补全子命令、枚举值及实时资源 ID）
kha completion zsh > "${fpath[1]}/_kha"
```

### 脚本输出格式与 GJSON 提取

数据命令遵循严格的流分离：**`stdout` 只输出纯数据**，进度提示、告警、加载动画和错误信息全部输出到 `stderr`。

```sh
# 切换输出格式：table（默认）、json、jsonl 或 yaml
kha agents list -o json
kha agents get "$AGENT_ID" -o yaml

# 使用 --all 自动翻页并合并全部结果
kha sessions list --all -o json

# 每行一个紧凑 JSON 对象，边翻页边输出；--transform 逐条作用
kha sessions list --all -o jsonl | jq -c 'select(.status == "idle")'
kha sessions list --all -o jsonl --transform id -r

# 服务端过滤：RFC 3339 时间范围、仅官方资源、分类与归属
kha sessions list --since 24h --all -o json
kha agents list --official-only
kha skills list --category engineering
kha triggers list --agent "$AGENT_ID"

# 使用 --transform（GJSON 语法）和 -r（去引号原始字符串）直接提取字段
kha agents list --transform 'items.#.id' -r
kha agents list --transform '{id,name}'

# 让 stderr 输出结构化 JSON/YAML 错误，方便外层脚本解析
kha sessions get sesn_invalid --error-output json
```

### 传入请求配置、文件内容与敏感凭据

`create` 与 `update` 命令既接受标量参数（如 `--name`、`--model`、`--system-file`），也为请求体中的对象与数组字段提供**结构化参数**。结构化参数的值同时接受宽松 YAML（`{name: x}`）与严格 JSON；可重复参数每出现一次向数组追加一个元素：

```sh
kha agents create --name analyst --model kimi-k3 \
  --tool '{type: custom, name: search, description: web search, input_schema: {type: object}}' \
  --mcp-server '{name: github, url: https://mcp.example}' \
  --skill "sk_xxx@2" \
  --metadata team=infra

kha sessions create --agent "$AGENT_ID" --environment "$ENVIRONMENT_ID" \
  --resource '{type: vault, vault_id: vlt_xxx}' \
  --override '{model: kimi-k3}'

kha environments create --name analysis \
  --networking limited --allow-host example.com \
  --pip pandas==2.3.2 --setup-script-file setup.sh
```

字段较多时，建议把完整请求体写成一个 JSON/YAML 文档经 `--file` 传入——字段参数与 `--file` 不可在同一条命令中混用：

```sh
# 从文件或标准输入（-）读取完整的 JSON/YAML 请求体
kha agents create --file agent.yaml
jq -n '{title: "新标题"}' | kha sessions update "$SESSION_ID" --file -

# 从文件或标准输入读取多行提示词或消息正文（保留末尾换行）
kha agents update "$AGENT_ID" --version "$VERSION" --system-file system.md
kha sessions send "$SESSION_ID" --text-file prompt.md --wait

# 以完整 user.message 事件发送富文本内容块（如文件引用）
kha sessions send "$SESSION_ID" --wait \
  --event '{type: user.message, data: {content: [{type: text, text: "总结这个 CSV"}, {type: file, file_id: "file_xxx", mime_type: text/csv, title: data.csv, path: upload/data.csv}]}}'

# 通过 stdin 传入敏感凭据，避免密钥残留在 Shell 历史或进程列表中
kha login --api-key-stdin < api-key.txt
kha vaults credentials create "$VAULT_ID" --type environment_variable --name GITHUB_TOKEN --value-stdin
```

### 退出码约定

| 退出码 | 含义 |
| --- | --- |
| `0` | 执行成功（`plugins validate` 在校验通过或仅有告警时也返回 `0`）。 |
| `1` | 运行时错误、API 报错或校验未通过（`plugins validate` 的诊断结果仍输出在 `stdout`）。 |
| `2` | 命令用法错误（参数非法、缺少必填项或发送空消息；未发出任何 API 请求）。 |
| `130` | 收到 `SIGINT`/`SIGTERM` 中断（在 `sessions attach` 中按 `Ctrl-C` 仅断开本地流，云端轮次继续运行）。 |

### 在 AI 编程助手中使用（`kha install-skill`）

KHA 二进制内嵌了面向 AI 编程助手的技能包（含 `SKILL.md` 及分场景参考手册），可让 **Kimi Code**、**Claude Code**、**Codex**、**Cursor** 等助手直接掌握 Kimi Hosted Agents 的操作规范：

```sh
kha install-skill      # 安装到当前项目：./.agents/skills/kha/
kha install-skill -g   # 安装到当前用户：~/.agents/skills/kha/
```

安装后按客户端要求刷新或重开会话即可。
运行 `kha upgrade` 升级 CLI 时，会自动同步刷新标准项目目录和用户目录中已安装的技能包副本。

### 环境变量与 Profile 优先级

连接配置按 `--api-key` > `KIMI_API_KEY` > profile、`--base-url` > `KHA_BASE_URL` > profile > `https://api.moonshot.cn` 解析；生效 profile 由 `--profile`、`KHA_PROFILE` 或配置文件的 `current` 条目决定（`kha profile list` / `kha profile use`）。完整优先级规则见 `kha docs`。

---

## 文档与资源导航

| 主题 | 链接 | 内容说明 |
| --- | --- | --- |
| 了解平台 | [Hosted Agents 产品概览](https://platform.kimi.com/docs/hosted-agents/overview) | 平台架构、云端沙箱、Skill、Plugin 与典型场景 |
| 跑通首个任务 | [官方快速开始](https://platform.kimi.com/docs/hosted-agents/quickstart) | 准备 Agent 与 Environment、运行 Session 及下载 Artifact |
| 命令行手册 | [CLI 指南](https://platform.kimi.com/docs/hosted-agents/kha) 与 `kha docs` | 认证、profile 管理、输出提取、请求文件与排障 |
| 事件流机制 | [事件流指南](https://platform.kimi.com/docs/hosted-agents/event-stream) | 事件类型、cursor 续传、SSE 重连与历史回补机制 |
| REST 接口契约 | [API 参考](https://platform.kimi.com/docs/api-reference) | 全部公开端点、请求/响应字段与状态码说明 |
| Go SDK 参考 | [pkg.go.dev/github.com/MoonshotAI/kha](https://pkg.go.dev/github.com/MoonshotAI/kha) | 所有公开客户端、模型、选项与错误类型的 Go 文档 |
| 可编译代码示例 | [`example_test.go`](../../example_test.go) | Session、分页、事件流、错误处理与自定义客户端示例 |
| 版本更新记录 | [中文 CHANGELOG](CHANGELOG.md) · [English](../../CHANGELOG.md) | 版本演进、新增特性、不兼容变更与迁移说明 |

---

## 版本策略

KHA 的 Go SDK 与 CLI 遵循语义化版本（`v0.X.Y`）。
在达到 `v1.0.0` 之前，公开 Go API 与 CLI 选项可能会在次版本间演进；所有不兼容变更与迁移步骤均记录在 [中文 CHANGELOG](CHANGELOG.md)（及英文 [`CHANGELOG.md`](../../CHANGELOG.md)）中。

Kimi Hosted Agents 服务端 REST API 通过 `Kimi-Api-Version` 请求头独立进行版本管理，当前默认版本为 `2026-09-01-beta`（对应常量 `kha.DefaultAPIVersion`）。

## 参与贡献与问题反馈

欢迎提交代码改进、文档修订、Bug 报告或功能建议！

- **参与开发**：请阅读 [`CONTRIBUTING.md`](../../CONTRIBUTING.md) 了解构建、测试、代码检查与 Pull Request 合入流程；本仓库完全自包含，生成代码已直接签入仓库，只需安装 Go 1.27+ 即可在本地编译并运行全部测试。
- **反馈问题**：请通过 [GitHub Issues](https://github.com/MoonshotAI/kha/issues) 提交问题，并附上 `kha version` 输出、最小复现命令或 Go 代码片段，以及响应头中的 `Msh-Request-Id`（如有）；提交前请务必隐去 API Key、Token、私有资源信息及敏感事件内容。

## 许可证

KHA 基于 [Apache-2.0 License](../../LICENSE) 开源发布。
