# ssh-handoff

用户在终端中建立的 SSH 连接由其中运行的 `ssh` 进程持有，Agent 无法直接使用。Agent 的每次命令调用都会启动独立进程，因此即使用户已经登录，Agent 也没有可复用的远端执行入口。

ssh-handoff 将用户建立的 SSH 连接保存为本地可发现、可复用的 session。Agent 通过 session ID 复用同一条已认证 SSH transport，并为每条命令创建独立 channel；用户同时持有原始 Shell。

ssh-handoff 支持 Linux、macOS、WSL 和原生 Windows。Linux、macOS 与 WSL 使用系统 OpenSSH 的 `ControlMaster`；Windows 使用 Plink connection sharing，并通过 ConPTY 保留完整的交互终端。

## 安装

从源码构建需要 Go 1.26 或更新版本。Linux、macOS 与 WSL 还需要支持 `ControlMaster` 和 `ControlPath` 的系统 OpenSSH 客户端。原生 Windows 需要 Windows 10 1809 或更新版本以及 Plink 0.84 或更新版本；`plink.exe` 可放在 `ssh-handoff.exe` 同目录、同目录的 `bin` 子目录或 `PATH` 中，也可以通过 `SSH_HANDOFF_PLINK` 指定完整路径。

可以直接安装最新版：

```sh
go install github.com/cyhforlight/ssh-handoff@latest
```

也可以克隆仓库后，在源码目录中构建：

```sh
go build -o ssh-handoff .
```

在 PowerShell 中构建 Windows 可执行文件：

```powershell
go build -o ssh-handoff.exe .
```

## 快速开始

在一个真实终端中提供目标并建立 SSH 连接：

```sh
ssh-handoff open --host example.com --user user
```

`open` 会先显示生成的 session ID（如 A3B4）。随后像平时一样完成密码、MFA、扫码、主机指纹确认或堡垒机选机等操作，以进入目标服务器。

随后，在另一个终端中使用 session ID 执行命令：

```sh
ssh-handoff run A3B4 'uname -a'
```

`run` 在未托管和托管状态下都可使用。未托管时，原始 Shell 可以照常用于人工操作，包括运行 `htop`、编辑器等交互程序；`run` 使用独立 channel，不会占用该终端。

ID 输入不区分大小写，`a3b4` 和 `A3B4` 指向同一个 session。

托管状态用于原始 Shell 的定时保活。需要启用时，在空闲 Shell 中按 `Ctrl-]`：

```text
[ssh-handoff] A3B4 已托管；按 Ctrl-] 恢复交互。
```

托管期间，ssh-handoff 会阻止普通键盘输入，并每 10 分钟向原始 Shell 写入一次空操作保持活跃。再次按 `Ctrl-]` 可以恢复人工交互。**两次切换都应在空 Shell prompt 上进行，不要在 `htop`、编辑器或其他交互程序中切换。**

## 命令

### `open`

```sh
ssh-handoff open --host HOST --user USER [--port PORT] [--identity FILE] [--mode exec|shell-pty] [--note NOTE]
ssh-handoff open --profile NAME [--mode exec|shell-pty] [--note NOTE]
```

直接连接在所有平台上可用。`--host` 和 `--user` 必填，`--port` 默认为 22；host 可以是域名、IPv4 或 IPv6，ssh-handoff 会原样交给当前平台的 SSH 客户端：

```sh
ssh-handoff open --host example.com --user operator
ssh-handoff open --host 2001:db8::10 --user operator --port 2222
ssh-handoff open --host internal.example.com --user operator --identity ~/.ssh/id_ed25519
```

Linux、macOS 与 WSL 还支持 OpenSSH profile：

```sh
ssh-handoff open --profile myserver
ssh-handoff open --profile jump-alias --mode shell-pty --note '生产环境'
```

profile 等价于执行 `ssh myserver`，HostName、User、Port、IdentityFile、ProxyJump 等配置继续由 OpenSSH 从 `~/.ssh/config` 原生读取。profile 与 direct 参数完全互斥。

原生 Windows 使用 Plink，不支持 OpenSSH profile、PuTTY saved session 或 OpenSSH-to-PuTTY 配置转换。Windows 上的 `--identity` 应指向 Plink 能够读取的私钥文件；Unix 上则应使用 OpenSSH 能够读取的格式。主机指纹、密码、MFA、ssh-agent、Pageant 和密钥认证均由底层客户端在原始终端中处理。

`open` 不提供 IPv4/IPv6、compression、任意 `-o` 或 extra args。复杂的 Unix OpenSSH 配置应放入 profile。端口转发和文件传输以后可分别作为 `forward`、`copy` 操作加入，不属于连接建立参数。

一个 `open` 进程对应一个 session。关闭该进程或终端会结束 session。

### `run`

```sh
ssh-handoff run [--stream] [--timeout DURATION] [--shell-ready-delay DURATION] <session-id> '<command>'
ssh-handoff run [--stream] [--timeout DURATION] [--shell-ready-delay DURATION] <session-id> - < script.sh
```

`run` 通过已认证 transport 新建 channel，同步执行一条非交互命令。默认超时为一分钟：

```sh
ssh-handoff run --timeout 2m A3B4 'kubectl get nodes -o wide'
```

`--shell-ready-delay` 只作用于 `shell-pty` session，默认为 1 秒。新建命令 channel 后，ssh-handoff 先发送一个回车，等待远端 Shell 就绪，再发送工作命令和 `exit`；设为 `0` 可以取消等待。该等待不会缩短 `--timeout` 指定的命令执行时间，SSH 子进程提前退出时也会立即结束。

命令参数为 `-` 时，ssh-handoff 从本地标准输入读取完整命令文本，适合执行包含多层引号或多行内容的脚本：

```sh
ssh-handoff run A3B4 - < deploy.sh
```

在 PowerShell 中可以使用：

```powershell
Get-Content -Raw -Encoding utf8 .\deploy.sh | .\ssh-handoff.exe run A3B4 -
```

标准输入只用于提供命令文本；读取完成后，命令仍作为一次非交互执行发送，不会继续向远端程序转发输入。stdin 文本中的 CRLF 会规范化为 LF，以便 Windows 文本管道和脚本能够交给远端 POSIX Shell；单独的 CR 不会被改写。

需要提权的命令应使用非交互模式：

```sh
ssh-handoff run A3B4 'sudo -n systemctl restart myapp'
```

`sudo -n` 的权限错误会直接返回。`run` 超时只终止本地 SSH 调用，远端进程可能继续运行；超时后先检查远端进程和已有状态，再决定是否重试。

长时间运行的命令可以加 `--stream`，让输出在命令结束前持续返回：

```sh
ssh-handoff run --stream --timeout 10m A3B4 'docker compose pull'
```

流式模式使用 NDJSON，每行都是一条完整 JSON 事件。`exec` session 产生 `stdout` 和 `stderr` 事件，`shell-pty` session 产生 `output` 事件；最后一行只会是一个 `result` 或 `error` 事件。已经发送的输出不会在 `result` 中重复：

```jsonl
{"type":"stdout","data":"pulling api\n"}
{"type":"stderr","data":"warning: retrying\n"}
{"type":"result","session":"A3B4","mode":"exec","exit_code":0,"timed_out":false}
```

默认模式在命令结束后输出单个 JSON 结果，适合只关心最终状态的短命令。

### `list`

```sh
ssh-handoff list
```

列出仍然存活的 session，包括 ID、规范化连接信息、备注和执行模式。

### `close`

```sh
ssh-handoff close A3B4
```

关闭指定 session 及其 SSH transport。ID 输入不区分大小写；该 session 中正在执行的 `run` 可能随之中断。

### 帮助

使用 `ssh-handoff --help` 查看命令总览，或使用 `ssh-handoff <command> --help` 查看子命令帮助。

## 工作模型

一个 session 持有一条已经完成认证的底层 SSH transport，它可以同时承载多个相互独立的 channel：

```text
已认证的 SSH transport（OpenSSH ControlMaster / Plink upstream）
├── 原始 Shell channel：人工操作、托管切换和定时保活
└── 命令 channel：每次 run 单独新建
```

`run` 的命令和输出不会进入原始终端，托管状态只作用于原始 Shell channel。每次 `run` 都是一次完整、同步、非交互的命令执行，各次调用通过独立 channel 彼此隔离，可以在同一 session 上并发；有先后依赖的操作应顺序发起。命令 channel 在调用结束后关闭，不继承原始 Shell 或上一次 `run` 的工作目录、环境变量和历史状态。新 channel 的初始目录、环境和 Shell 初始化由远端 SSH 服务决定；需要共享状态的多个步骤应组合在同一个命令字符串中，并显式包含所需的 `cd` 和环境变量。

session 属于创建它的本地系统用户。Agent 需要以同一用户运行，并能访问相同的 runtime 目录及 OpenSSH control socket 或 PuTTY connection-sharing 命名管道；未共享这些路径的容器、其他 Windows 安全令牌、WSL 实例或隔离环境无法发现该 session。

PTY（pseudo-terminal，伪终端）让远端程序以连接到终端的方式运行，因此输出中可能包含输入回显、Shell prompt 和控制字符。原始 Shell channel 使用 PTY，以保留用户操作终端所需的交互能力。部分堡垒机或设备入口拒绝标准 SSH `exec` 请求，只接受带 PTY 的 Shell channel；`shell-pty` session 用于兼容这类入口。

`open --mode` 创建下面两类 session 之一，并固定该 session 后续命令 channel 的类型；`run` 没有 mode 参数。

### `exec` session

`open` 默认创建 `exec` session。此类 session 的命令 channel 不分配 PTY，工作命令通过 SSH `exec` 请求发送。stdout、stderr 和命令退出状态可以分别取得。

### `shell-pty` session

使用 `open --mode shell-pty` 创建此类 session。它的命令 channel 请求 PTY 并启动远端 Shell，发送一个回车并等待 Shell 就绪，再输入预先给定的工作命令和 `exit`。这个 Shell 用于适配远端入口；ssh-handoff 不会在执行期间继续转发用户输入，因此工作命令仍须能够一次性、非交互地完成。需要持续输入、菜单操作或全屏终端的程序应在原始 Shell 中运行。

## 输出

### `run`

`run` 的结果结构取决于 session 的执行模式。

以下单个 JSON 结构用于未指定 `--stream` 的默认模式。流式模式的 NDJSON 事件见 [`run`](#run) 命令说明。

#### `exec`

`exec` session 成功执行后的结果分别提供 `stdout` 和 `stderr` 字段：

```json
{
  "session": "A3B4",
  "mode": "exec",
  "stdout": "server\n",
  "stderr": "",
  "exit_code": 0,
  "timed_out": false
}
```

`exit_code` 正常情况下等于远端工作命令的退出状态；SSH 客户端自身发生错误时通常为 255。

#### `shell-pty`

`shell-pty` session 成功执行后的结果使用单一的 `output` 字段：

```json
{
  "session": "A3B4",
  "mode": "shell-pty",
  "output": "server\r\n",
  "exit_code": 0,
  "timed_out": false
}
```

`shell-pty` 的 `output` 记录该命令 channel 从 Shell 启动到关闭期间产生的完整终端流，可能包含启动信息、命令回显、提示符、工作命令输出、`exit` 回显和控制字符，具体内容由远端入口和 Shell 决定。`exit_code` 表示整个 PTY Shell 会话的退出状态，只用于辅助判断会话是否正常结束；该模式不提供工作命令的退出状态。

`run` 的参数错误、session 相关错误和本地执行错误使用统一的 JSON 结构：

```json
{
  "error": {
    "code": "session_not_found",
    "message": "session not found: A3B4"
  }
}
```

错误代码包括 `invalid_arguments`、`session_not_found`、`session_unavailable`、`execution_error` 和 `local_error`。`run` 超时时返回 `timed_out: true`，ssh-handoff 进程以状态码 124 退出。

### `list`

`list` 成功时输出表格，例如：

```text
ID    MODE  CONNECTION                   NOTE
A3B4  exec  user@example.com:22          生产环境
B5C6  exec  profile internal-production
```

`list` 失败时将错误写入 stderr，并以非零状态退出。

### `close`

成功关闭 session 后输出：

```text
closed A3B4
```

`close` 失败时将错误写入 stderr，并以非零状态退出。

## Agent Prompt

可以直接把 session ID 和下面这段提示词交给 Agent：

```text
请使用本机的 ssh-handoff 操作我已经认证的 SSH session <SESSION_ID>。

- 使用 `ssh-handoff run [--timeout DURATION] <SESSION_ID> '<command>'` 执行普通命令；长时间运行的命令加 `--stream` 实时接收 NDJSON；复杂或多行命令可通过 `ssh-handoff run <SESSION_ID> - < SCRIPT` 从本地标准输入读取。
- 如果没有给出 session ID，先运行 `ssh-handoff list`，根据 ID、连接信息和备注识别；无法确定时询问我。
- 命令应包含所需的 `cd`、环境变量和完整参数，并能够非交互执行。
- 需要提权时使用 `sudo -n`，权限失败时告诉我。
- `timed_out: true` 时检查远端实际状态，再决定是否重试。
- 默认模式根据单个 JSON 结果判断；`--stream` 模式持续处理输出事件，并以最后的 `result` 或 `error` 事件判断完成状态。
- 任务完成后保持 session 存活。
```

## 设计文档

项目目标、核心语义和范围说明见 [`docs/PROJECT.md`](docs/PROJECT.md)。
