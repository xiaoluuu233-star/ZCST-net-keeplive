//go:build windows

package network

import (
	"math/bits"
	"syscall"
)

// Windows IP_UNICAST_IF 使用网络字节序的网卡索引。
// 参考 stmtc233/zcst-net 的 MIT 实现，见 THIRD_PARTY_NOTICES.md。
func bindInterface(index int, name string) func(string, string, syscall.RawConn) error {
	return func(network, address string, c syscall.RawConn) error {
		var bindErr error
		if err := c.Control(func(fd uintptr) {
			bindErr = syscall.SetsockoptInt(syscall.Handle(fd), syscall.IPPROTO_IP, 31, int(bits.ReverseBytes32(uint32(index))))
		}); err != nil {
			return err
		}
		return bindErr
	}
}
