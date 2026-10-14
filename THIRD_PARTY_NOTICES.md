# 第三方来源与许可

## 参考项目

- 项目：[stmtc233/zcst-net](https://github.com/stmtc233/zcst-net)
- 参考提交：[eb928170bb23d08d2331de9517743190c63b2e12](https://github.com/stmtc233/zcst-net/tree/eb928170bb23d08d2331de9517743190c63b2e12)
- 许可：MIT，Copyright (c) 2026 stmtc233。

主动恢复设计参考该项目的客户端 HTTP 心跳与探测/认证分离思路。Windows Win32 ABI 布局及 `IP_UNICAST_IF` 网卡绑定处理参考或选择性改编自该项目。相关版权与 MIT 许可保留在 [LICENSE](LICENSE) 中。

本工程单独实现配置、Portal 认证、网络检测和恢复调度；主动检测使用 Go 标准 HTTP 客户端，并实现新连接复核、备用目标判断、认证后确认、暂停与冷却保护。两项目均通过客户端发起的请求判断联网状态，不应将其描述为服务器推送注销事件。

## Go 模块依赖

| 模块 | 固定版本 | 许可 | 许可文件 |
| --- | --- | --- | --- |
| [github.com/pelletier/go-toml/v2](https://github.com/pelletier/go-toml) | v2.4.3 | MIT | [LICENSES/go-toml.txt](LICENSES/go-toml.txt) |
| [golang.org/x/sys](https://pkg.go.dev/golang.org/x/sys) | v0.30.0 | BSD-3-Clause | [LICENSES/x-sys.txt](LICENSES/x-sys.txt) |

实际依赖与校验值以 `go.mod`、`go.sum` 为准。发行包保留主许可证、本说明与依赖许可文件。
