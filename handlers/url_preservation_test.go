package handlers

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/hoshinonyaruko/gensokyo/callapi"
	"github.com/hoshinonyaruko/gensokyo/config"
	"github.com/hoshinonyaruko/gensokyo/internal/application/outbound"
	"github.com/hoshinonyaruko/gensokyo/internal/domain/identity"
	"github.com/hoshinonyaruko/gensokyo/internal/domain/message"
	"github.com/hoshinonyaruko/gensokyo/template"
)

func TestRemoteMediaSchemeRoundTrip(t *testing.T) {
	for _, kind := range []string{"image", "record", "video", "file"} {
		for _, scheme := range []string{"http", "https"} {
			t.Run(kind+"_"+scheme, func(t *testing.T) {
				key := "url_" + kind
				if scheme == "https" {
					key += "s"
				}
				value := "example.com/media?name=a&value=b"
				found := map[string][]string{key: {value}}
				cmd := BuildOutboundCommand("", found, identity.ResolvedTarget{}, outbound.DeliveryPolicy{})
				var source message.MediaSource
				switch part := cmd.Message.Parts[0].(type) {
				case message.ImagePart:
					source = part.Source
				case message.AudioPart:
					source = part.Source
				case message.VideoPart:
					source = part.Source
				case message.FilePart:
					source = part.Source
				}
				if source.URL != scheme+"://"+value {
					t.Fatalf("协议丢失: %+v", source)
				}
				_, actual := PartsToFoundItems(cmd.Message.Parts)
				if !reflect.DeepEqual(actual, found) {
					t.Fatalf("往返后改变: got=%v want=%v", actual, found)
				}
			})
		}
	}
}

func TestNotificationURLsRetainedWithHiddenServerIP(t *testing.T) {
	for _, mode := range []string{"legacy", "new"} {
		yml := strings.Replace(template.ConfigTemplate, "cq_parse_mode : legacy", "cq_parse_mode : "+mode, 1)
		yml = strings.Replace(yml, "server_dir: \"<YOUR_SERVER_DIR>\"", "server_dir: \"127.0.0.1\"", 1)
		path := filepath.Join(t.TempDir(), "config.yml")
		if err := os.WriteFile(path, []byte(yml), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := config.LoadConfig(path, false); err != nil {
			t.Fatal(err)
		}
		if !config.GetTransferUrl() || config.GetVisibleIP() {
			t.Fatal("测试必须覆盖启用短链且隐藏服务器 IP 的默认配置")
		}
		for _, url := range []string{"https://weibo.com/123456/Example", "https://www.bilibili.com/opus/123456789"} {
			for _, group := range []bool{true, false} {
				t.Run(fmt.Sprintf("%s/%t/%s", mode, group, url), func(t *testing.T) {
					params := callapi.ParamsContent{UserID: strings.Repeat("c", 32), Message: []interface{}{textSegment("更新通知\n" + url + "\n")}}
					if group {
						params.GroupID = strings.Repeat("d", 32)
					}
					text, _, _ := parseMessageContent(params, callapi.ActionMessage{Params: params}, nil, nil, nil)
					if !strings.Contains(text, url) {
						t.Fatalf("正文链接被删除: %q", text)
					}
				})
			}
		}
	}
}

func TestRemoteImage567CurlFallback(t *testing.T) {
	withRemoteImageTransport(t, func(r *http.Request) (*http.Response, error) {
		return remoteImageHTTPResponse(r, 567, []byte("blocked")), nil
	})
	old := curlImageRequest
	defer func() { curlImageRequest = old }()
	for _, privateRedirect := range []bool{false, true} {
		calls := 0
		curlImageRequest = func(r *http.Request) (*http.Response, error) {
			calls++
			if r.URL.Path == "/start" {
				res := remoteImageHTTPResponse(r, 302, nil)
				location := "https://93.184.216.34/image"
				if privateRedirect {
					location = "http://127.0.0.1/image"
				}
				res.Header.Set("Location", location)
				return res, nil
			}
			return remoteImageHTTPResponse(r, 200, remoteTestPNG), nil
		}
		data, err := downloadRemoteImage(context.Background(), "https://93.184.216.34/start")
		if privateRedirect {
			if err == nil || calls != 1 {
				t.Fatalf("curl 重定向绕过检查: err=%v calls=%d", err, calls)
			}
		} else if err != nil || len(data) != len(remoteTestPNG) || calls != 2 {
			t.Fatalf("567 未切换 curl: err=%v calls=%d", err, calls)
		}
	}
	curlImageRequest = func(r *http.Request) (*http.Response, error) {
		res := remoteImageHTTPResponse(r, 200, []byte("<html>not an image</html>"))
		res.Body = io.NopCloser(strings.NewReader("<html>not an image</html>"))
		return res, nil
	}
	if _, err := downloadRemoteImage(context.Background(), "https://93.184.216.34/image"); err == nil {
		t.Fatal("curl 的 HTML 响应不应被当作图片")
	}
}
