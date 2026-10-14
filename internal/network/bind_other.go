//go:build !windows

package network

import "syscall"

// 非 Windows 使用源 IPv4 约束出口；第一版不安装系统服务。
func bindInterface(index int, name string) func(string, string, syscall.RawConn) error { return nil }
