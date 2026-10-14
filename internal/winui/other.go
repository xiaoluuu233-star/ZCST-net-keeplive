//go:build !windows

package winui

import (
	"context"
	"errors"
	"fmt"
	"os"
)

var errWindowsOnly = errors.New("此功能仅支持 Windows；请使用 run 命令运行保活")

func Run(context.Context, Options) error  { return errWindowsOnly }
func Acquire(string) (func(), error)      { return func() {}, nil }
func StartupEnabled(string) (bool, error) { return false, errWindowsOnly }
func SetStartup(bool, string) error       { return errWindowsOnly }
func ErrorDialog(message string)          { fmt.Fprintln(os.Stderr, message) }
func IsDoubleClick() bool                 { return false }
func openPath(string) error               { return errWindowsOnly }
