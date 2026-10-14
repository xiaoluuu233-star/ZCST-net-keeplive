package main

import (
	"campus-net/internal/app"
	"campus-net/internal/config"
	"campus-net/internal/network"
	"campus-net/internal/winui"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
)

var version = "1.1.0"

const help = `校园网 Go 自动登录与保活
用法：campus-network [run|tray|check|config-check|version] [--config 路径]
run          命令行保活，Ctrl+C 退出
tray         Windows 托盘（默认），开机启动需手动开启
check        仅检测网络，不进行认证
config-check 仅校验配置，不联网、不显示账号密码
version      显示版本
--config/-c  指定配置；默认读取 EXE 同目录 config.toml
`

func execute(ctx context.Context, args []string, out, errOut io.Writer) int {
	command := ""
	options := []string{}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--help" || arg == "-h" || arg == "help" {
			fmt.Fprint(out, help)
			return 0
		}
		if arg == "--config" || arg == "-c" {
			options = append(options, arg)
			i++
			if i >= len(args) {
				fmt.Fprintln(errOut, "缺少配置路径")
				return 2
			}
			options = append(options, args[i])
			continue
		}
		if strings.HasPrefix(arg, "-") {
			options = append(options, arg)
			continue
		}
		if command != "" {
			fmt.Fprintln(errOut, "不支持多个命令")
			return 2
		}
		command = arg
	}
	if command == "" {
		command = "run"
		if runtime.GOOS == "windows" {
			command = "tray"
		}
	}
	if command == "version" {
		fmt.Fprintln(out, "campus-network", version)
		return 0
	}
	if command != "run" && command != "tray" && command != "check" && command != "config-check" {
		fmt.Fprintln(errOut, "未知命令；使用 --help 查看用法")
		return 2
	}
	fs := flag.NewFlagSet("campus-network", flag.ContinueOnError)
	fs.SetOutput(errOut)
	var path string
	fs.StringVar(&path, "config", "", "配置路径")
	fs.StringVar(&path, "c", "", "配置路径")
	if err := fs.Parse(options); err != nil {
		return 2
	}
	cfg, err := config.Load(path)
	fail := func(err error) int {
		message := "程序未能启动：" + err.Error()
		fmt.Fprintln(errOut, message)
		if command == "tray" {
			winui.ErrorDialog(message)
		}
		return 1
	}
	if err != nil {
		return fail(err)
	}
	if command == "config-check" {
		fmt.Fprintln(out, "配置有效；未联网，未显示凭据。")
		return 0
	}
	if command == "check" {
		h, e := network.New(cfg)
		if e != nil {
			return fail(e)
		}
		defer h.CloseIdleConnections()
		o := network.Check(ctx, h, cfg)
		fmt.Fprintln(out, o.Detail)
		if o.Online {
			return 0
		}
		return 1
	}
	release, err := winui.Acquire(cfg.ConfigPath)
	if err != nil {
		return fail(err)
	}
	defer release()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	c := app.New(cfg.ConfigPath, nil)
	if err = c.Start(ctx); err != nil {
		return fail(err)
	}
	defer c.Stop()
	if command == "run" {
		fmt.Fprintln(out, "保活已启动；Ctrl+C 退出。")
		<-ctx.Done()
		return 0
	}
	err = winui.Run(ctx, winui.Options{ConfigPath: cfg.ConfigPath, LogPath: cfg.LogPath, CurrentLogPath: func() string { return c.Config().LogPath }, Snapshot: func() winui.Status {
		s := c.Snapshot()
		return winui.Status{State: s.State, Detail: s.Detail, Paused: s.Paused}
	}, Pause: c.Pause, Retry: c.Retry, Reload: c.Reload, Quit: cancel})
	if err != nil {
		return fail(err)
	}
	return 0
}
func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := execute(ctx, os.Args[1:], os.Stdout, os.Stderr)
	cancel()
	os.Exit(code)
}
