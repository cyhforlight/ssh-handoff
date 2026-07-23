# ssh-handoff

用户在终端中建立的 SSH 连接属于该终端进程，Agent 无法直接使用。Agent 的每次命令调用都会启动独立进程，用户已经登录成功也不会为它提供可用的远端执行入口。

ssh-handoff 将用户建立的 SSH 连接保存为本地可发现、可复用的 session。Agent 通过 session ID 复用同一条 OpenSSH transport，并为每条命令创建独立 channel；用户继续保留原始 Shell。

ssh-handoff 当前支持 Linux、macOS 和 WSL，依赖系统 OpenSSH；尚未支持原生 Windows。

## 安装

ssh-handoff 需要 Go 1.26 或更新版本，以及支持 `ControlMaster` 和 `ControlPath` 的系统 OpenSSH 客户端。可以直接安装最新版：

```sh
go install github.com/cyhforlight/ssh-handoff@latest
```

也可以克隆仓库后，在源码目录中构建：

```sh
go build -o ssh-handoff .
```

## 快速开始

在一个真实终端中运行平时使用的 SSH 命令：

```sh
ssh-handoff open 'ssh user@example.com'
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

托管期间，ssh-handoff 会阻止普通键盘输入，并每 30 分钟向原始 Shell 写入一次空操作保持活跃。再次按 `Ctrl-]` 可以恢复人工交互。**两次切换都应在空 Shell prompt 上进行，不要在 `htop`、编辑器或其他交互程序中切换。**

## 命令

### `open`

```sh
ssh-handoff open [--note NOTE] [--mode exec|shell-pty] 'ssh ...'
```

连接命令必须是以 `ssh` 开头、用于登录的单条命令。ssh-handoff 会插入连接复用参数，其余内容保持不变：

```sh
ssh-handoff open 'ssh -p 2222 user@example.com'
ssh-handoff open --note '生产环境' 'ssh -J bastion user@internal.example.com'
ssh-handoff open --mode shell-pty 'ssh jump-alias'
```

一个 `open` 进程对应一个 session。关闭该进程或终端会结束 session。

### `run`

```sh
ssh-handoff run [--timeout DURATION] <session-id> '<command>'
```

`run` 通过已认证 transport 新建 channel，同步执行一条非交互命令。默认超时为一分钟：

```sh
ssh-handoff run --timeout 2m A3B4 'kubectl get nodes -o wide'
```

需要提权的命令应使用非交互模式：

```sh
ssh-handoff run A3B4 'sudo -n systemctl restart myapp'
```

`sudo -n` 的权限错误会直接返回。`run` 超时只终止本地 SSH 调用，远端进程可能继续运行；超时后先检查远端进程和已有状态，再决定是否重试。

### `list`

```sh
ssh-handoff list
```

列出仍然存活的 session，包括 ID、连接命令、备注、执行模式和当前状态。

### `close`

```sh
ssh-handoff close A3B4
```

关闭指定 session。ID 输入不区分大小写。

### 帮助

使用 `ssh-handoff --help` 查看命令总览，或使用 `ssh-handoff <command> --help` 查看子命令帮助。

## 工作模型

一个 session 持有一条已经完成认证的 OpenSSH transport。这里的 transport 是底层 SSH 连接，一条连接可以同时承载多个相互独立的 channel：

```text
已认证的 OpenSSH transport
├── 原始 Shell channel：人工操作、托管切换和定时保活
└── 命令 channel：每次 run 单独新建
```

`run` 的命令和输出不会进入原始终端。每次 `run` 都是一次完整、同步、非交互的命令执行，调用方会等待该次结果，同一 session 的多次调用按顺序执行。命令 channel 在调用结束后关闭，不继承原始 Shell 或上一次 `run` 的工作目录、环境变量和历史状态。新 channel 的初始目录、环境和 Shell 初始化由远端 SSH 服务决定；需要共享状态的多个步骤应组合在同一个命令字符串中，并显式包含所需的 `cd` 和环境变量。托管状态的作用范围是原始 Shell channel。

session 属于创建它的本地系统用户。Agent 需要以同一用户运行，并能访问相同的 runtime 目录和 OpenSSH control socket；未共享这些路径的容器、WSL 实例或其他隔离环境无法发现该 session。

PTY（pseudo-terminal，伪终端）让远端程序以连接到终端的方式运行，因此输出中可能包含输入回显、Shell prompt 和控制字符。原始 Shell channel 使用 PTY，以保留用户操作终端所需的交互能力。部分堡垒机或设备入口拒绝标准 SSH `exec` 请求，只接受带 PTY 的 Shell channel；`shell-pty` session 用于兼容这类入口。

`open --mode` 创建下面两类 session 之一，并固定该 session 后续命令 channel 的类型；`run` 没有 mode 参数。

### `exec` session

`open` 默认创建 `exec` session。此类 session 的命令 channel 不分配 PTY，工作命令通过 SSH `exec` 请求发送。stdout、stderr 和命令退出状态可以分别取得。

### `shell-pty` session

使用 `open --mode shell-pty` 创建此类 session。它的命令 channel 请求 PTY 并启动远端 Shell，再向 Shell 输入预先给定的工作命令和 `exit`。这里的交互式 Shell 是远端入口提供的 SSH channel 形式；ssh-handoff 不会在执行期间继续转发用户输入，因此工作命令仍须能够一次性、非交互地完成。需要持续输入、菜单操作或全屏终端的程序应在原始 Shell 中运行。

```sh
ssh-handoff open --mode shell-pty 'ssh ...'
```

## 输出

`run`、`list` 和 `close` 面向 Agent 输出 JSON。

### `run`

`run` 的结果结构取决于 session 的执行模式。

#### `exec`

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

不同于 `exec`，`shell-pty` 使用单一的 `output` 字段：

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

### `list`

```json
{
  "sessions": [
    {
      "id": "A3B4",
      "connection_command": "ssh user@example.com",
      "note": "生产环境",
      "mode": "exec",
      "state": "interactive"
    }
  ]
}
```

`note` 为空时不会出现。`starting` 表示原始 SSH 进程正在启动；`interactive` 和 `managed` 表示原始 Shell 当前处于人工交互或托管状态。

### `close`

```json
{
  "session": "A3B4",
  "closed": true
}
```

### 错误与退出状态

参数、session 和本地执行错误使用统一结构：

```json
{
  "error": {
    "code": "session_not_found",
    "message": "session not found: A3B4"
  }
}
```

错误代码包括 `invalid_arguments`、`session_not_found`、`session_unavailable`、`execution_error` 和 `local_error`。`run` 超时时返回 `timed_out: true`，ssh-handoff 进程以状态码 124 退出。

## Agent Prompt

可以直接把 session ID 和下面这段提示词交给 Agent：

```text
请使用本机的 ssh-handoff 操作我已经认证的 SSH session <SESSION_ID>。

- 使用 `ssh-handoff run [--timeout DURATION] <SESSION_ID> '<command>'` 执行所有远端命令。
- 如果没有给出 session ID，先运行 `ssh-handoff list`，根据 ID、连接命令和备注识别；无法确定时询问我。
- 命令应包含所需的 `cd`、环境变量和完整参数，并能够非交互执行。
- 需要提权时使用 `sudo -n`，权限失败时告诉我。
- `timed_out: true` 时检查远端实际状态，再决定是否重试。
- `exec` 根据 `stdout`、`stderr`、`exit_code` 和 `timed_out` 判断结果；`shell-pty` 根据 `output` 和 `timed_out` 判断结果。
- 任务完成后保持 session 存活。
```

## 设计文档

项目目标、核心语义和范围说明见 [`docs/PROJECT.md`](docs/PROJECT.md)。
