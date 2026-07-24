# ssh-handoff 项目概览

本文用于在设计或编写代码前建立对项目的整体认识。它记录当前阶段的目标、核心语义和
范围，不是实现规格。

## 项目是什么

ssh-handoff 是一个本地 CLI 工具：用户先在真实终端中完成 SSH 认证，随后工具保持这条
原始会话活跃，Agent 通过复用已授权的 SSH transport 新建 channel 执行命令。

它来自“让 AI 通过堡垒机自动运维”的真实实习需求，但当前项目面向更宽泛的场景：
只要用户本来能够从本机 SSH 到远程服务器，就可以把连接交给 Agent。项目不接收、保存
或代填密码、密钥口令、MFA 等认证材料。

这个项目有意保持为一个小而完整的包装器。重点不是重新实现 SSH，也不是把远程命令
执行包装成新概念，而是连接人工认证和 Agent 操作，并让用户明确掌握连接的生命周期。

## 使用方式

用户直接提供自己平时使用的 SSH 命令，不要求目标预先存在于 `~/.ssh/config`：

```sh
ssh-handoff open 'ssh -p 2222 user@example.com'
ssh-handoff open --mode shell-pty 'ssh jump-alias'
ssh-handoff open --note '生产环境' 'ssh -J bastion user@internal.example.com'
```

`open` 接受一条以 `ssh` 开头的完整命令字符串。工具只在 `ssh` 后插入连接复用所需的
参数，其余内容不解析、不改写，再交给本地 Shell 执行。输入应是用于登录的单条 SSH
命令，不包含预设的远程命令或其他 Shell 组合操作。

`open` 保持在前台，用户在这个窗口中完成密码、MFA、扫码、主机指纹确认或堡垒机选机。
工具会提示用户在进入目标 Shell 后准备托管。用户在空闲 prompt 上按 `Ctrl-]` 切换为
托管状态，再按一次退出托管；两次切换各执行一次空指令进行同步。托管期间原始 Shell
不再接受普通用户输入，并且每 30 分钟执行一次空指令以维持会话活跃，但不承载 Agent
的工作命令。

连接建立后，工具生成一个简短的 session ID，作为 `run` 和 `close` 唯一接受的 session
引用。用户可以通过 `--note` 添加可选备注；备注只用于展示，不要求唯一，也不参与查找。
`list` 同时返回 ID、原始连接命令和备注，方便辨认不同 session。

Agent 从另一个进程调用：

```sh
ssh-handoff run <session-id> '<command>'
ssh-handoff run <session-id> - < script.sh
```

命令参数为 `-` 时，工具从本地标准输入读取完整命令文本，再按同样的非交互语义执行；
标准输入不转发给远端程序。

v1 CLI 只包含四个子命令：

```text
open   人工认证并保持 session
run    在指定 session 中同步执行一条命令
list   列出仍然存活的 session
close  按 ID 关闭指定 session
```

原始终端在交给工具托管后不再接受普通用户输入；用户退出托管状态后才能继续操作。
工具由此保证人工操作、保活写入和状态切换不会互相干扰，而不是依赖用户避免同时输入。
用户只需保证在空闲 Shell prompt 上进行交接。

托管状态不限制 `run`：人工操作和 Agent 命令位于不同 channel，未托管时 Agent 仍可
正常执行命令。托管只控制原始 Shell 的用户输入和定时保活，不改变 `run` 的结果，也不
为未托管调用增加警告。

一个 `open` 进程对应一个 session；关闭窗口或进程即结束该 session。`list` 和 `close`
用于发现及显式关闭已有 session。`run` 使用结构化 JSON 封装命令结果，`list` 以表格
展示 session，`close` 返回一行确认文本。

## Session 模型

一个 session 包含两个职责不同的部分：

- **保活 Shell**：用户认证时进入的原始交互 Shell。进入托管状态后，工具定期向它写入
  空指令，避免 JumpServer 一类服务因原始会话长时间无操作而断开；v1 的固定间隔为
  30 分钟；
- **执行 channel**：Agent 每次调用 `run` 时，通过同一条已认证 transport 新建的
  channel。工作命令不会写入保活 Shell，也不依赖其中的 `cwd`、环境变量或历史状态。

连接托管与命令执行是两个独立维度。无论使用哪种执行模式，原始 Shell 都只承担认证、
人工接管和保活；Agent 始终使用新建的执行 channel。

## 执行模式

项目保留两种在 `open` 时选定的模式：

- `exec`：复用 OpenSSH transport，新建标准远程命令 channel，适合普通服务器，也是默认
  模式；
- `shell-pty`：复用同一条 transport，新建带 PTY 的 Shell channel，面向拒绝标准
  `exec`、只允许交互式会话的堡垒机或设备。

两种模式都逐次新建 channel，因此 `run` 之间不保留隐式 Shell 状态，但可获得的输出
语义不同。Agent 面向行式、非交互的 Linux/POSIX Shell 命令；密码询问、确认菜单、
全屏程序和交互式 `sudo` 仍由用户处理。命令同步执行，同一个 session 内一次只运行一条
Agent 命令。`exec` 可以提供独立的 stdout、stderr 和可靠的远端退出状态；`shell-pty`
沿用简单的临时 Shell 语义：写入工作命令和 `exit`，等待 SSH 子进程结束，返回混合输出、
本地 SSH 进程退出码和超时状态，不包装命令或使用完成标记，也不承诺该退出码等于工作
命令的真实远端退出码。

## 技术方向

项目使用 Go 编写，并直接调用系统 OpenSSH。这样可以沿用用户已有的 SSH 配置、跳板机、
密码或 MFA、硬件密钥和主机指纹处理，而不在项目内重新实现 SSH 协议与认证。

Go 代码负责本地 session 生命周期、原始终端托管、定时保活和 Agent 调用。v1 面向
Linux、macOS 和 WSL。每个 `open` 进程持有自己的 SSH 进程、PTY 和 ControlPath
socket，不设全局后台服务或持久化运行态。连接复用由 OpenSSH 的
ControlMaster/ControlPath 提供。

共享的 CLI 与 session 语义不依赖具体平台，SSH 客户端和终端托管位于明确的平台 seam。
v1 只实现系统 OpenSSH 与 Unix PTY；未来原生 Windows adapter 固定使用 Plink connection
sharing 与 ConPTY，而不是 Windows OpenSSH。当前不为未实现的 Windows adapter 增加
空脚手架。

具体的 socket 路径、参数解析和结果字段属于实现阶段的设计问题，不在本文中预先规定。

## v1 范围

v1 要完成的是：用户通过任意常规 SSH 命令人工建立连接，工具托管并定期保活原始
Shell，Agent 随后通过新建的 `exec` 或 `shell-pty` channel 同步执行非交互命令，并能
列出和关闭多个显式 session。

v1 不实现原生 Windows，也不计划加入 MCP 适配、文件传输、第二套 SSH 配置、凭据管理、
异步任务、自动重连与重放、策略审计平台，以及 Agent 对多轮交互程序的自动化。

范围判断以旧 `ssh-manager` 为现实基线：它没有提供的能力不会因为“一个完整工具似乎
应该具备”就自然进入 v1。新能力应由实际使用阻力推动。
