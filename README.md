# ssh-handoff

ssh-handoff 让用户先在真实终端中完成 SSH 认证，再把已经授权的连接交给 Agent 复用。
它不会接收、保存或代填密码、密钥口令和 MFA 信息。

当前支持 Linux、macOS 和 WSL，依赖系统 OpenSSH。原生 Windows 尚未实现。

## 安装

需要 Go 1.26 或更新版本，以及支持连接复用的系统 `ssh`：

```sh
go install .
```

也可以在仓库中直接构建：

```sh
go build -o ssh-handoff .
```

## 快速开始

在一个真实终端中运行平时使用的 SSH 命令：

```sh
ssh-handoff open 'ssh user@example.com'
```

`open` 会先显示生成的 session ID。像平时一样完成密码、MFA、扫码、主机指纹确认或
堡垒机选机。进入空闲 Shell 后，按 `Ctrl-]` 切换为托管状态：

```text
[ssh-handoff] A3B4 已托管；按 Ctrl-] 恢复交互。
```

托管期间，ssh-handoff 会阻止普通键盘输入，并每 30 分钟向原始 Shell 写入一次空操作
保持活跃。再次按 `Ctrl-]` 可以恢复人工交互。两次切换都应在空闲 Shell prompt 上进行，
不要在 `htop`、编辑器或其他交互程序中切换。

在另一个终端中使用 session ID 执行命令：

```sh
ssh-handoff run A3B4 'uname -a'
```

ID 输入不区分大小写，`a3b4` 和 `A3B4` 指向同一个 session。

## 命令

### `open`

```sh
ssh-handoff open [--note NOTE] [--mode exec|shell-pty] 'ssh ...'
```

连接命令必须是以 `ssh` 开头、用于登录的单条命令。ssh-handoff 会插入连接复用参数，
其余内容保持不变：

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

每次 `run` 都使用新的 channel，不继承原始 Shell 或上一次 `run` 的工作目录、环境变量和
历史状态。托管状态只控制原始 Shell 的输入和保活，不限制 `run`。

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

## 执行模式

`open` 使用 `--mode` 选择后续 `run` 的执行方式：

- `exec`：默认模式，使用标准 SSH exec channel，分别返回 stdout、stderr 和远端退出码；
- `shell-pty`：新建带 PTY 的 Shell channel，适用于拒绝 exec channel 的堡垒机或设备。

`shell-pty` 会向临时 Shell 写入命令和 `exit`，返回混合输出、本地 SSH 进程退出码和超时
状态。它不保证退出码等于工作命令的真实远端退出码。

## 输出

`run`、`list` 和 `close` 面向 Agent 输出 JSON。默认 `exec` 的结果示例：

```json
{"session":"A3B4","mode":"exec","stdout":"server\n","stderr":"","exit_code":0,"timed_out":false}
```

`shell-pty` 使用单一 `output` 字段：

```json
{"session":"A3B4","mode":"shell-pty","output":"server\r\n","exit_code":0,"timed_out":false}
```

命令超时返回状态码 124。参数、session 和本地执行错误以结构化 JSON 返回。

## 帮助

```sh
ssh-handoff --help
ssh-handoff help
ssh-handoff help open
ssh-handoff run --help
```

项目目标、核心语义和范围说明见 [`docs/PROJECT.md`](docs/PROJECT.md)。
