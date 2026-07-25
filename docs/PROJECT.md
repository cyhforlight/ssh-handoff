# ssh-handoff 项目概览

本文用于在设计或编写代码前建立对项目的整体认识。它记录当前阶段的目标、核心语义和范围，不是实现规格。

## 项目是什么

ssh-handoff 是一个本地 CLI 工具：用户先在真实终端中完成 SSH 认证，随后工具保持这条原始会话活跃，Agent 通过复用已授权的 SSH transport 新建 channel 执行命令。

它来自“让 AI 通过堡垒机自动运维”的真实实习需求，但当前项目面向更宽泛的场景：只要用户本来能够从本机 SSH 到远程服务器，就可以把连接交给 Agent。项目不接收、保存或代填密码、密钥口令、MFA 等认证材料。

这个项目有意保持为一个小而完整的包装器。重点不是重新实现 SSH，也不是把远程命令执行包装成新概念，而是连接人工认证和 Agent 操作，并让用户明确掌握连接的生命周期。

## 使用方式

用户通过结构化参数提供连接目标。direct 连接在所有平台上可用：

```sh
ssh-handoff open --host example.com --user operator
ssh-handoff open --host 2001:db8::10 --user operator --port 2222
ssh-handoff open --host internal.example.com --user operator --identity ~/.ssh/id_ed25519
```

`--host` 和 `--user` 必填，`--port` 默认为 22，`--identity` 可选。host 作为不透明值交给底层客户端，ssh-handoff 不区分域名、IPv4 与 IPv6。Unix 使用 OpenSSH 可读取的 identity，Windows 使用 Plink 可读取的 identity，不在两种格式之间转换。

Linux、macOS 与 WSL 还可用 `--profile myserver` 接入 OpenSSH profile，等价于用户平时执行 `ssh myserver`。HostName、User、Port、IdentityFile、ProxyJump 等配置由 OpenSSH 从 `~/.ssh/config` 原生读取。profile 与 direct 参数完全互斥。Windows Plink 后端明确拒绝 profile，也不读取 OpenSSH config、接入 PuTTY saved session 或实现配置转换。

`open` 保持在前台，用户在这个窗口中完成密码、MFA、扫码、主机指纹确认或堡垒机选机。工具会提示用户在进入目标 Shell 后准备托管。用户在空闲 prompt 上按 `Ctrl-]` 切换为托管状态，再按一次退出托管；两次切换各执行一次空指令进行同步。托管期间原始 Shell 不再接受普通用户输入，并且每 30 分钟执行一次空指令以维持会话活跃，但不承载 Agent 的工作命令。

连接建立后，工具生成一个简短的 session ID，作为 `run` 和 `close` 唯一接受的 session 引用。用户可以通过 `--note` 添加可选备注；备注只用于展示，不要求唯一，也不参与查找。`list` 同时返回 ID、规范化连接信息和备注，方便辨认不同 session。

Agent 从另一个进程调用：

```sh
ssh-handoff run <session-id> '<command>'
ssh-handoff run --stream <session-id> '<long-running-command>'
ssh-handoff run <session-id> - < script.sh
```

命令参数为 `-` 时，工具从本地标准输入读取完整命令文本，再按同样的非交互语义执行；标准输入不转发给远端程序。

v1 CLI 只包含四个子命令：

```text
open   人工认证并保持 session
run    在指定 session 中同步执行一条命令
list   列出仍然存活的 session
close  按 ID 关闭指定 session
```

原始终端在交给工具托管后不再接受普通用户输入；用户退出托管状态后才能继续操作。工具由此保证人工操作、保活写入和状态切换不会互相干扰，而不是依赖用户避免同时输入。用户只需保证在空闲 Shell prompt 上进行交接。

托管状态不限制 `run`：人工操作和 Agent 命令位于不同 channel，未托管时 Agent 仍可正常执行命令。托管只控制原始 Shell 的用户输入和定时保活，不改变 `run` 的结果，也不为未托管调用增加警告。

一个 `open` 进程对应一个 session；关闭窗口或进程即结束该 session。`list` 和 `close` 用于发现及显式关闭已有 session。`run` 默认使用单个结构化 JSON 封装命令结果；可选的 `--stream` 使用 NDJSON 在执行期间发送带来源的输出事件，并以唯一的结果或错误事件结束。流式终态不重复已经发送的输出。`list` 以表格展示 session，`close` 返回一行确认文本。

## Session 模型

一个 session 包含两个职责不同的部分：

- **保活 Shell**：用户认证时进入的原始交互 Shell。进入托管状态后，工具定期向它写入空指令，避免 JumpServer 一类服务因原始会话长时间无操作而断开；v1 的固定间隔为 30 分钟；
- **执行 channel**：Agent 每次调用 `run` 时，通过同一条已认证 transport 新建的 channel。工作命令不会写入保活 Shell，也不依赖其中的 `cwd`、环境变量或历史状态。

连接托管与命令执行是两个独立维度。无论使用哪种执行模式，原始 Shell 都只承担认证、人工接管和保活；Agent 始终使用新建的执行 channel。

## 执行模式

项目保留两种在 `open` 时选定的模式：

- `exec`：复用已认证 SSH transport，新建标准远程命令 channel，适合普通服务器，也是默认模式；
- `shell-pty`：复用同一条 transport，新建带 PTY 的 Shell channel，面向拒绝标准 `exec`、只允许交互式会话的堡垒机或设备。

两种模式都逐次新建 channel，因此 `run` 之间不保留隐式 Shell 状态，但可获得的输出语义不同。Agent 面向行式、非交互的 Linux/POSIX Shell 命令；密码询问、确认菜单、全屏程序和交互式 `sudo` 仍由用户处理。命令同步执行，同一个 session 内一次只运行一条 Agent 命令。`exec` 可以提供独立的 stdout、stderr 和可靠的远端退出状态；`shell-pty` 沿用简单的临时 Shell 语义：写入工作命令和 `exit`，等待 SSH 子进程结束，返回混合输出、本地 SSH 进程退出码和超时状态，不包装命令或使用完成标记，也不承诺该退出码等于工作命令的真实远端退出码。

## 技术方向

项目使用 Go 编写，并调用平台已有的 SSH 客户端，而不在项目内重新实现 SSH 协议与认证。Linux、macOS 与 WSL 直接调用系统 OpenSSH，因此可以沿用用户已有的 SSH 配置、跳板机、密码或 MFA、硬件密钥和主机指纹处理。原生 Windows 调用 Plink 0.84 或更新版本，沿用 Plink/PuTTY 的主机指纹、Pageant、密码、MFA 与密钥处理。

共享的结构化连接模型记录 profile 或 direct 的 host、user、port、identity。平台 adapter 从这个模型直接构造 OpenSSH 或 Plink argv；项目不保存、解析或重新拼接本地 SSH 命令字符串，也不通过本地 Shell 启动 SSH 客户端。

Go 代码负责本地 session 生命周期、原始终端托管、定时保活和 Agent 调用。每个 `open` 进程持有自己的 SSH 客户端进程和终端，不设全局后台服务或持久化运行态。在 Unix 侧，本地终端为 Unix PTY，连接复用由 OpenSSH 的 ControlMaster/ControlPath 提供；在原生 Windows 侧，本地终端为 ConPTY，连接复用由 Plink connection sharing 提供。

共享的 CLI、session registry、托管切换和输出语义不依赖具体平台，SSH 客户端与终端托管位于明确的平台 seam。每次 `run` 的命令校验、超时、输出绑定和终态归类属于共享执行流程；平台 adapter 只检查已有 transport 并准备一次 OpenSSH 或 Plink 客户端进程。Windows adapter 不是用 Plink 模拟 OpenSSH ControlMaster：它使用两个临时 PuTTY profile，把原始连接限制为 sharing upstream、把每次 `run` 限制为 sharing downstream，并在下游禁用独立认证。这样可以保持 session 的所有权和关闭语义，避免共享连接消失后悄悄建立第二条已认证 transport。

Windows 上 Plink sharing identity 由用户、逻辑 host 与端口决定，因此同一系统用户不能同时由 ssh-handoff 持有两个完全相同的 Plink target；工具会在 `open` 时明确拒绝这种冲突。Windows session 使用用户 cache 下的 runtime 目录，临时 PuTTY profile 会在 session 清理时删除。

## v1 范围

当前版本要完成的是：用户通过平台支持的 SSH 登录命令人工建立连接，工具托管并定期保活原始 Shell，Agent 随后通过新建的 `exec` 或 `shell-pty` channel 同步执行非交互命令，并能在 Linux、macOS、WSL 与原生 Windows 上列出和关闭多个显式 session。

当前范围仍不计划加入 MCP 适配、文件传输、凭据管理、异步任务、自动重连与重放、策略审计平台，以及 Agent 对多轮交互程序的自动化。Windows adapter 只正规化历史 `ssh-manager` 已验证的 Plink 路线，不额外实现 Windows OpenSSH ControlMaster 或完整的 OpenSSH-to-PuTTY 配置翻译层。

`open` 不提供地址族选择、compression、任意 SSH 选项或 extra args。端口转发与文件传输若由实际需求推动，应分别作为连接建立后的 `forward` 和 `copy` 操作设计，不扩张 `open` 的连接接口。

范围判断以旧 `ssh-manager` 为现实基线：它没有提供的能力不会因为“一个完整工具似乎应该具备”就自然进入 v1。新能力应由实际使用阻力推动。
