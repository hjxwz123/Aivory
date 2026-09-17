package oauth

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func useWeComTestClient(t *testing.T, fn oauth2RoundTripFunc) {
	t.Helper()
	previous := httpClient
	httpClient = &http.Client{Transport: fn}
	weComTokenCache.Lock()
	weComTokenCache.entries = make(map[[32]byte]weComCachedToken)
	weComTokenCache.Unlock()
	t.Cleanup(func() {
		httpClient = previous
		weComTokenCache.Lock()
		weComTokenCache.entries = make(map[[32]byte]weComCachedToken)
		weComTokenCache.Unlock()
	})
}

func resolvedWeComConfig() Config {
	return Resolve(Config{
		Kind: "wecom", ClientID: "ww-corp-id", AgentID: "1000002", ClientSecret: "app-secret",
	})
}

func TestWeComAuthorizationURLUsesOfficialCorpAppParameters(t *testing.T) {
	cfg := resolvedWeComConfig()
	raw := cfg.AuthCodeURL("https://chat.example.test/api/auth/oauth/oa_wecom/callback", "state-value", "must-not-appear", "must-not-appear")
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if u.Scheme+"://"+u.Host+u.Path != "https://login.work.weixin.qq.com/wwlogin/sso/login" {
		t.Fatalf("authorization endpoint = %q", u.String())
	}
	for key, want := range map[string]string{
		"login_type":   "CorpApp",
		"appid":        "ww-corp-id",
		"agentid":      "1000002",
		"redirect_uri": "https://chat.example.test/api/auth/oauth/oa_wecom/callback",
		"state":        "state-value",
	} {
		if got := u.Query().Get(key); got != want {
			t.Fatalf("authorization query %s = %q, want %q", key, got, want)
		}
	}
	if u.Query().Has("client_id") || u.Query().Has("scope") || u.Query().Has("nonce") || u.Query().Has("code_challenge") {
		t.Fatalf("authorization URL contains non-WeCom parameters: %s", u.RawQuery)
	}
	if UsesPKCE("wecom") || UsesIDToken("wecom") || UsesFormPost(cfg) {
		t.Fatalf("unexpected WeCom protocol flags: PKCE=%v IDToken=%v FormPost=%v", UsesPKCE("wecom"), UsesIDToken("wecom"), UsesFormPost(cfg))
	}
}

func TestWeComExchangeAndMemberProfile(t *testing.T) {
	tokenRequests := 0
	useWeComTestClient(t, func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/cgi-bin/gettoken":
			tokenRequests++
			if req.URL.Query().Get("corpid") != "ww-corp-id" || req.URL.Query().Get("corpsecret") != "app-secret" {
				t.Fatalf("token query = %s", req.URL.RawQuery)
			}
			return oauth2JSONResponse(`{"errcode":0,"access_token":"app-token","expires_in":7200}`), nil
		case "/cgi-bin/auth/getuserinfo":
			if req.URL.Query().Get("access_token") != "app-token" || req.URL.Query().Get("code") != "login-code" {
				t.Fatalf("identity query = %s", req.URL.RawQuery)
			}
			return oauth2JSONResponse(`{"errcode":0,"userid":"zhangsan"}`), nil
		case "/cgi-bin/user/get":
			if req.URL.Query().Get("access_token") != "app-token" || req.URL.Query().Get("userid") != "zhangsan" {
				t.Fatalf("member query = %s", req.URL.RawQuery)
			}
			return oauth2JSONResponse(`{"errcode":0,"name":"Zhang San","biz_mail":"zhangsan@example.com","email":"other@example.com","avatar":"https://example.com/avatar.png"}`), nil
		default:
			t.Fatalf("unexpected WeCom request: %s", req.URL)
			return nil, errors.New("unexpected request")
		}
	})

	cfg := resolvedWeComConfig()
	tokens, err := cfg.Exchange(context.Background(), "https://ignored.example/callback", "login-code", "")
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	info, err := cfg.FetchUserInfo(context.Background(), tokens, "")
	if err != nil {
		t.Fatalf("FetchUserInfo: %v", err)
	}
	if info.Subject != "zhangsan" || info.Name != "Zhang San" || info.Email != "zhangsan@example.com" ||
		!info.EmailVerified || info.AvatarURL != "https://example.com/avatar.png" {
		t.Fatalf("member info = %+v", info)
	}
	if _, err := cfg.Exchange(context.Background(), "", "second-code", ""); err != nil {
		t.Fatalf("cached Exchange: %v", err)
	}
	if tokenRequests != 1 {
		t.Fatalf("token requests = %d, want cached single request", tokenRequests)
	}
}

func TestWeComFallsBackToUserIDWhenProfileIsNotVisible(t *testing.T) {
	useWeComTestClient(t, func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/cgi-bin/gettoken":
			return oauth2JSONResponse(`{"errcode":0,"access_token":"app-token","expires_in":7200}`), nil
		case "/cgi-bin/auth/getuserinfo":
			return oauth2JSONResponse(`{"errcode":0,"userid":"member-outside-visible-range"}`), nil
		case "/cgi-bin/user/get":
			return oauth2JSONResponse(`{"errcode":60111,"errmsg":"user not found"}`), nil
		default:
			t.Fatalf("unexpected path %s", req.URL.Path)
			return nil, errors.New("unexpected request")
		}
	})
	cfg := resolvedWeComConfig()
	tokens, err := cfg.Exchange(context.Background(), "", "login-code", "")
	if err != nil {
		t.Fatal(err)
	}
	info, err := cfg.FetchUserInfo(context.Background(), tokens, "")
	if err != nil {
		t.Fatalf("FetchUserInfo: %v", err)
	}
	if info.Subject != "member-outside-visible-range" || info.Name != info.Subject || info.Email != "" || info.EmailVerified {
		t.Fatalf("fallback member info = %+v", info)
	}
}

func TestWeComRejectsNonEnterpriseIdentity(t *testing.T) {
	useWeComTestClient(t, func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/cgi-bin/gettoken":
			return oauth2JSONResponse(`{"errcode":0,"access_token":"app-token","expires_in":7200}`), nil
		case "/cgi-bin/auth/getuserinfo":
			return oauth2JSONResponse(`{"errcode":0,"openid":"external-open-id"}`), nil
		default:
			t.Fatalf("unexpected path %s", req.URL.Path)
			return nil, errors.New("unexpected request")
		}
	})
	cfg := resolvedWeComConfig()
	tokens, err := cfg.Exchange(context.Background(), "", "login-code", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cfg.FetchUserInfo(context.Background(), tokens, ""); err == nil || !strings.Contains(err.Error(), "not an enterprise member") {
		t.Fatalf("non-enterprise identity error = %v", err)
	}
}

func TestWeComRefreshesExpiredAccessTokenOnce(t *testing.T) {
	tokenRequests := 0
	identityRequests := 0
	useWeComTestClient(t, func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/cgi-bin/gettoken":
			tokenRequests++
			return oauth2JSONResponse(`{"errcode":0,"access_token":"token-` + string(rune('0'+tokenRequests)) + `","expires_in":7200}`), nil
		case "/cgi-bin/auth/getuserinfo":
			identityRequests++
			if identityRequests == 1 {
				return oauth2JSONResponse(`{"errcode":42001,"errmsg":"access token expired"}`), nil
			}
			if req.URL.Query().Get("access_token") != "token-2" {
				t.Fatalf("retry used access token %q", req.URL.Query().Get("access_token"))
			}
			return oauth2JSONResponse(`{"errcode":0,"userid":"member-1"}`), nil
		case "/cgi-bin/user/get":
			return oauth2JSONResponse(`{"errcode":0,"email":"member@example.com"}`), nil
		default:
			t.Fatalf("unexpected path %s", req.URL.Path)
			return nil, errors.New("unexpected request")
		}
	})
	cfg := resolvedWeComConfig()
	tokens, err := cfg.Exchange(context.Background(), "", "login-code", "")
	if err != nil {
		t.Fatal(err)
	}
	info, err := cfg.FetchUserInfo(context.Background(), tokens, "")
	if err != nil {
		t.Fatal(err)
	}
	if info.Subject != "member-1" || info.Email != "member@example.com" || !info.EmailVerified || tokenRequests != 2 || identityRequests != 2 {
		t.Fatalf("info=%+v tokenRequests=%d identityRequests=%d", info, tokenRequests, identityRequests)
	}
}

func TestWeComAPIErrorsDoNotLeakCredentials(t *testing.T) {
	const secret = "secret-must-not-leak"
	useWeComTestClient(t, func(req *http.Request) (*http.Response, error) {
		return oauth2JSONResponse(`{"errcode":40013,"errmsg":"invalid secret ` + secret + `"}`), nil
	})
	cfg := Resolve(Config{Kind: "wecom", ClientID: "corp", AgentID: "agent", ClientSecret: secret})
	_, err := cfg.Exchange(context.Background(), "", "code-must-not-leak", "")
	if err == nil {
		t.Fatal("Exchange succeeded unexpectedly")
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "code-must-not-leak") {
		t.Fatalf("WeCom error leaked credentials: %v", err)
	}
}
