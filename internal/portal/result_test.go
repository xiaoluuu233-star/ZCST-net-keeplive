package portal

import "testing"

// 这些独立响应来自协议形态，不靠实现自身计算预期结果。
func TestClassifyRejectsFalseAndPartialSuccess(t *testing.T) {
	cases := []struct {
		body   string
		status int
		want   State
	}{
		{`{"success":false}`, 200, Failed},
		{`{"code":"7","message":"账号或密码不正确"}`, 200, BadCredential},
		{`{"code":7,"message":"账号或密码不正确"}`, 200, BadCredential},
		{`{"message":"PPPoE 认证失败，内网认证成功"}`, 200, PartialFailure},
		{`{"code":0,"message":"认证成功"}`, 500, Failed},
		{`<html>success login page</html>`, 200, Failed},
		{`{"message":"unsuccessful"}`, 200, Failed},
		{`{"code":"0","message":"认证成功"}`, 200, Success},
		{`{"code":0}`, 200, Success},
		{`{"message":"already online"}`, 200, AlreadyOnline},
		{`{"success":true}`, 200, Success},
		{`{"success":false,"message":"认证成功"}`, 200, Failed},
		{`{"code":7,"message":"认证成功"}`, 200, BadCredential},
		{`{"code":0,"message":"用户已在线，但认证失败"}`, 200, Failed},
		{`{"message":"用户已在线，error"}`, 200, Failed},
	}
	for _, tc := range cases {
		t.Run(tc.body, func(t *testing.T) {
			if got := Classify(tc.status, []byte(tc.body)); got.State != tc.want {
				t.Fatalf("state=%s want=%s", got.State, tc.want)
			}
		})
	}
}
