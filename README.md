# 校园网 Go 自动登录与保活

Windows Go 1.1.0：命令行、纯 Go 托盘、用户可选开机启动。没有 Python 运行依赖，不需要管理员权限安装系统服务。

## 使用

编译好的 `campus-network.exe` 与本地 `config.toml` 放在一起；源码目录的 `config.toml` 已兼容迁移原 Python 配置，属于本地私密文件，不进入压缩包和 Git。发行 ZIP 中只有配置模板，解压后需要自己准备真实配置。

双击 EXE 进入托盘，右键菜单显示真实网络状态，并提供暂停/恢复、立即重试、重新载入配置、打开配置/日志、开机启动开关、退出。开机启动默认不自动开启，只有点击开关才写入当前用户的 Run 项；移除或移动 EXE 前先关闭这个选项。

命令行示例（在程序目录执行）：

```powershell
.\campus-network.exe config-check --config .\config.toml
.\campus-network.exe check --config .\config.toml
.\campus-network.exe run --config .\config.toml
.\campus-network.exe tray --config .\config.toml
.\campus-network.exe version
```

`config-check` 只校验配置，不联网、不显示账号密码；`check` 只检测网络，不认证。`run` 会检测并按需认证，Ctrl+C 退出。默认配置固定查找 EXE 同目录；从源码运行时必须显式指定配置：

```powershell
go run ./cmd/campus-net run --config ./config.toml
```

同一个配置路径只允许一个实例，避免命令行和托盘重复认证。旧 Python 程序不会参与这个互斥机制，切换到 Go 时需先自行退出原 Python 保活程序；回退时退出 Go，再运行原 Python。

## 配置兼容与重载

可以使用新版分组模板，也可以直接读取原 Python 扁平 TOML，例如 userid/password/portal_base_url/check_interval/timeout 等。账号原样发送，密码中的前后空格保留；BRAS、VLAN、MAC 与网内 IP 的回退值不擅自替换。检测重定向中同一 Portal 的新参数优先覆盖配置，不保留旧网络缓存。

旧 `groupId` 和 `heartbeat_interval` 兼容读取但不生效：登录默认不发送 groupId；在线检测与心跳共用一次 HTTP 请求，周期由 check_interval 或 probe.interval 控制，1.1.0 默认 1 秒。已有显式周期仍按配置执行，本次本机配置已迁移到 1 秒。日志相对路径以配置文件所在目录解析。

探测超时独立使用扁平 `probe_timeout` 或分组 `probe.probe_timeout`，默认 2 秒；原 `timeout` / `probe.timeout` 继续作为共享请求/登录超时兼容读取，默认 8 秒，不会因加快探测而自动缩短登录时间。单轮外网诊断最多包含一个主探测预算和一个并行复核预算；整个认证后确认还受 confirm_timeout 限制。

编辑文件后使用“重新载入配置”。新文件先校验，校验失败继续使用旧任务；成功后取消并等待旧任务结束，再启动新任务，不并发登录，也会更新用于日志脱敏的凭据。重载保留用户的暂停选择。

## 联网判断与重试

Portal 响应中的 success=false、失败文案、部分 PPPoE 认证失败、非成功 HTTP 状态或未知 HTML 都不能直接算成功；JSON code 支持字符串和数字。认证成功/已在线后还需确认外网检测正常，才显示在线。

独立探测任务按固定节拍运行，前次未完成时合并节拍，不在超时后额外再睡一个完整周期。可信同源 Portal 重定向会快速唤醒唯一认证任务，不再等第二个完整检测周期。普通连接关闭、EOF、超时或异常响应先通过新连接复核，并行检查配置的备用检测站与 Portal；备用站点正常时不认证。明确的认证跳转与复核失败分开处理，Portal 可达不能单独证明认证已失效。

正常心跳复用连接；每 5 秒将一次普通心跳换为新连接检查，不增加请求次数，防止网关继续放行旧 TCP 流而掩盖注销。网页下线通常不会改变本机网卡状态，因此第一版优化以 HTTP 心跳为主，不依赖 Windows 网卡变化通知。每秒一个正常主探测请求；想降低频率可改成 2 秒。

认证前取消并等待旧探测结束，认证后重建连接并立即复查外网，旧结果不能把新会话误报在线。自动探测事件不能解除用户暂停、错误凭据保护或认证冷却。

max_retries 表示一轮最大认证次数，达到上限进入冷却但仍检查外网；外网恢复时计数清零。明确账号密码错误会暂停自动认证，修正配置并重载或点“立即重试”恢复。第一版不自动登出释放 PPPoE 会话。

## Clash、TUN 和多网卡

默认不读取 HTTP_PROXY/HTTPS_PROXY 等环境代理，allow_proxy=true 才启用；这一设置不能覆盖 TUN/VPN 的系统路由。将 clash-campus-direct.yaml 指向 DIRECT，并放在通用代理和 MATCH 之前。指定 network.interface 或 source_ip 可以约束出口；错误或未启用的网卡会报错，未指定则沿用系统路由，不缓存某个默认网卡。

## 日志与隐私

控制台和文件使用同一脱敏处理，过滤密码、账号、IP/MAC、URL 查询、异常与嵌套响应属性。默认每份日志最多 5 MB，保留 3 份历史文件；正常在线只记录状态变化，避免持续刷心跳日志。不要将真实 config.toml、旧协议抓包或历史日志提交到 GitHub。

## 编译与测试

需要 Go 1.26 或更新版本，依赖固定在 go.mod/go.sum。

```powershell
go test ./...
go vet ./...
powershell -ExecutionPolicy Bypass -File ./scripts/build.ps1
```

