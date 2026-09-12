// oauth.go 设备授权登录（CN realm）：供 Web 管理界面「添加账号」使用。
//
// 流程与 cmd/login 完全一致（无 PKCE，state 由服务端签发）：
//
//	OAuthStart → POST /v2/plugin/auth/state?platform=CLI  拿 state + authUrl
//	OAuthPoll  → GET  /v2/plugin/auth/token?state=         取 token（未完成返回 ErrOAuthPending）
//	           → GET  /v2/plugin/login/account?state=       取 uid/nickname
//
// 与 CLI 版本的差异：这里把两步都放在**同一进程内**，但每次调用仍使用独立的临时
// http.Client（独立 cookie jar），避免多账号并发登录互相串会话。
package upstream

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"time"

	"workbuddy2api/internal/auth"
)

// ErrOAuthPending 用户尚未在浏览器完成授权（上游业务 code != 0，如 "login ing"）。
// 调用方应稍后重试 OAuthPoll，而非当作失败。
var ErrOAuthPending = errors.New("oauth pending: waiting for user login")

// 设备授权相关路径（与 cmd/login/main.go 常量保持一致）。
const (
	oauthStatePath = "/v2/plugin/auth/state?platform=CLI"
	oauthTokenPath = "/v2/plugin/auth/token?state="
	oauthAcctPath  = "/v2/plugin/login/account?state="
)

// OAuthResult 一次成功登录的凭证与账号信息。
type OAuthResult struct {
	AccessToken  string
	RefreshToken string
	ExpiresIn    int64 // 秒
	Domain       string
	UID          string
	EnterpriseID string
	Nickname     string
}

// oauthHTTP 构造本趟流程专属的临时 client（独立 cookie jar，30s 超时）。
// 复用主 client 的 Transport（连接池/超时口径一致），仅隔离 cookie。
func (c *Client) oauthHTTP() *http.Client {
	jar, _ := cookiejar.New(nil)
	cl := &http.Client{Timeout: 30 * time.Second, Jar: jar}
	if c != nil && c.HTTP != nil && c.HTTP.Transport != nil {
		cl.Transport = c.HTTP.Transport
	}
	return cl
}

// OAuthStart 发起设备授权，返回 state 与授权 URL（用户在浏览器打开完成登录）。
func (c *Client) OAuthStart() (state, authURL string, err error) {
	req, err := http.NewRequest(http.MethodPost, c.chatBase(nil)+oauthStatePath, bytes.NewReader([]byte("{}")))
	if err != nil {
		return "", "", err
	}
	c.CommonHeaders(req, &auth.Auth{})
	data, err := c.doJSONWith(c.oauthHTTP(), req)
	if err != nil {
		return "", "", err
	}
	var st struct {
		State   string `json:"state"`
		AuthURL string `json:"authUrl"`
	}
	if err := json.Unmarshal(data, &st); err != nil || st.State == "" || st.AuthURL == "" {
		return "", "", fmt.Errorf("oauth start: missing state or authUrl")
	}
	return st.State, st.AuthURL, nil
}

// OAuthPoll 轮询一次授权结果：
//   - 用户未完成 → ErrOAuthPending（可重试）；
//   - 已完成 → 返回 token + uid/nickname（uid 缺失时报错，避免落盘无名凭证）。
func (c *Client) OAuthPoll(state string) (*OAuthResult, error) {
	if state == "" {
		return nil, fmt.Errorf("oauth poll: empty state")
	}
	cl := c.oauthHTTP()
	esc := url.QueryEscape(state)

	req, err := http.NewRequest(http.MethodGet, c.chatBase(nil)+oauthTokenPath+esc, nil)
	if err != nil {
		return nil, err
	}
	c.CommonHeaders(req, &auth.Auth{})
	data, err := c.doJSONWith(cl, req)
	if err != nil {
		// 业务层等待中（HTTP 2xx + code != 0）与 4xx 一律视为「未完成」；
		// 只有网络层错误与 5xx 才算真失败（避免把暂态抖动误报成登录失败）。
		var ue *Error
		if errors.As(err, &ue) && ue.Status < 500 {
			return nil, ErrOAuthPending
		}
		return nil, err
	}
	var tok struct {
		AccessToken  string `json:"accessToken"`
		RefreshToken string `json:"refreshToken"`
		ExpiresIn    int64  `json:"expiresIn"`
		Domain       string `json:"domain"`
	}
	if err := json.Unmarshal(data, &tok); err != nil || tok.AccessToken == "" {
		return nil, ErrOAuthPending
	}

	res := &OAuthResult{
		AccessToken:  tok.AccessToken,
		RefreshToken: tok.RefreshToken,
		ExpiresIn:    tok.ExpiresIn,
		Domain:       tok.Domain,
	}
	// 账号信息（uid/nickname）：失败不阻塞登录（与 cmd/login 同口径），uid 由调用方校验。
	areq, err := http.NewRequest(http.MethodGet, c.chatBase(nil)+oauthAcctPath+esc, nil)
	if err != nil {
		return res, nil
	}
	c.CommonHeaders(areq, &auth.Auth{})
	areq.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	if adata, aerr := c.doJSONWith(cl, areq); aerr == nil {
		var acct struct {
			UID          string `json:"uid"`
			EnterpriseID string `json:"enterpriseId"`
			Nickname     string `json:"nickname"`
		}
		if json.Unmarshal(adata, &acct) == nil {
			res.UID, res.EnterpriseID, res.Nickname = acct.UID, acct.EnterpriseID, acct.Nickname
		}
	}
	return res, nil
}
