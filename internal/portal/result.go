package portal

import (
	"encoding/json"
	"strings"
)

type State string

const (
	Failed         State = "failed"
	Success        State = "success"
	AlreadyOnline  State = "already-online"
	BadCredential  State = "bad-credential"
	PartialFailure State = "partial-failure"
	NetworkError   State = "network-error"
)

type Result struct {
	State         State
	Code, Message string
	HTTPStatus    int
}

func (r Result) OK() bool { return r.State == Success || r.State == AlreadyOnline }

// Classify 只接受结构化响应；出现矛盾信息时失败优先。
// 外网是否真正恢复仍由 keeper 单独确认。
func Classify(status int, body []byte) Result {
	r := Result{State: Failed, HTTPStatus: status}
	if status < 200 || status >= 300 {
		return r
	}
	var payload struct {
		Code    json.RawMessage `json:"code"`
		Message string          `json:"message"`
		Success *bool           `json:"success"`
	}
	if json.Unmarshal(body, &payload) != nil {
		return r
	}
	r.Message = strings.TrimSpace(payload.Message)
	if len(payload.Code) > 0 && string(payload.Code) != "null" {
		if json.Unmarshal(payload.Code, &r.Code) != nil {
			var number json.Number
			if json.Unmarshal(payload.Code, &number) != nil {
				return r
			}
			r.Code = number.String()
		}
	}
	msg := strings.ToLower(r.Message)
	if strings.Contains(msg, "pppoe") && (strings.Contains(msg, "失败") || strings.Contains(msg, "fail")) {
		r.State = PartialFailure
		return r
	}
	if r.Code == "7" || strings.Contains(msg, "密码不正确") || strings.Contains(msg, "密码错误") || strings.Contains(msg, "账号不存在") || strings.Contains(msg, "用户不存在") || strings.Contains(msg, "invalid password") || strings.Contains(msg, "invalid user") {
		r.State = BadCredential
		return r
	}
	if payload.Success != nil && !*payload.Success {
		return r
	}
	if strings.Contains(msg, "失败") || strings.Contains(msg, "不成功") || strings.Contains(msg, "fail") || strings.Contains(msg, "error") {
		return r
	}
	if strings.Contains(msg, "已在线") || strings.Contains(msg, "已经在线") || msg == "already online" || msg == "already-online" {
		r.State = AlreadyOnline
		return r
	}
	if r.Code != "" && r.Code != "0" {
		return r
	}
	if r.Code == "0" || (payload.Success != nil && *payload.Success) || strings.Contains(msg, "认证成功") || msg == "success" {
		r.State = Success
	}
	return r
}
