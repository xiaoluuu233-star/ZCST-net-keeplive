# 第三方来源与许可

本工程独立实现认证、配置、检测和恢复调度。Windows 的 Win32 ABI 布局与网卡 IP_UNICAST_IF 处理参考或选择性复用自：

- [stmtc233/zcst-net](https://github.com/stmtc233/zcst-net)，固定提交 `eb928170bb23d08d2331de9517743190c63b2e12`，MIT。
- Copyright (c) 2026 stmtc233。许可全文见 LICENSE。

Go 模块依赖（版本固定在 go.mod/go.sum）：

- github.com/pelletier/go-toml/v2 v2.4.3，MIT，许可证随源码模块提供。
- golang.org/x/sys v0.30.0，BSD-3-Clause，许可证随源码模块提供。

发行包保留 LICENSE、THIRD_PARTY_NOTICES.md 和 LICENSES/ 中的依赖许可，不包含真实配置或凭据。
