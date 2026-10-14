// Package winui 将平台界面与保活核心隔离；回调只传递状态或命令，不持有凭据。
package winui

import (
	"context"
	"errors"
)

type Status struct {
	State, Detail string
	Paused        bool
}
type Options struct {
	ConfigPath, LogPath string
	CurrentLogPath      func() string
	Snapshot            func() Status
	Pause               func(bool)
	Retry               func()
	Reload              func() error
	Quit                func()
}

func currentLogPath(opts Options) string {
	if opts.CurrentLogPath != nil {
		return opts.CurrentLogPath()
	}
	return opts.LogPath
}

const (
	cmdPause = 1001 + iota
	cmdRetry
	cmdReload
	cmdConfig
	cmdLog
	cmdStartup
	cmdQuit
)

type command struct {
	id     int
	status Status
}
type dispatcher struct {
	ctx      context.Context
	commands chan command
}

// 一个后台队列串行执行所有菜单动作。GUI 不等待重载、Shell 或注册表写入。
func newDispatcher(ctx context.Context, opts Options, complete func(error)) *dispatcher {
	d := &dispatcher{ctx: ctx, commands: make(chan command, 32)}
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case c := <-d.commands:
				if ctx.Err() != nil {
					return
				}
				var err error
				switch c.id {
				case cmdPause:
					if opts.Pause != nil {
						opts.Pause(!c.status.Paused)
					}
				case cmdRetry:
					if opts.Retry != nil {
						opts.Retry()
					}
				case cmdReload:
					if opts.Reload != nil {
						err = opts.Reload()
					}
				case cmdConfig:
					err = openPath(opts.ConfigPath)
				case cmdLog:
					err = openPath(currentLogPath(opts))
				case cmdStartup:
					var enabled bool
					enabled, err = StartupEnabled(opts.ConfigPath)
					if err == nil {
						err = SetStartup(!enabled, opts.ConfigPath)
					}
				case cmdQuit:
					if opts.Quit != nil {
						opts.Quit()
					}
				default:
					err = errors.New("未知托盘命令")
				}
				if complete != nil {
					complete(err)
				}
			}
		}
	}()
	return d
}

func (d *dispatcher) enqueue(id int, status Status) bool {
	if d.ctx.Err() != nil {
		return false
	}
	select {
	case <-d.ctx.Done():
		return false
	case d.commands <- command{id, status}:
		return true
	default:
		return false
	}
}
