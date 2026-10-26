package handlers

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/hoshinonyaruko/gensokyo/callapi"
	"github.com/hoshinonyaruko/gensokyo/internal/domain/identity"
	"github.com/tencent-connect/botgo/dto"
	"github.com/tencent-connect/botgo/openapi"
)

var remoteTestPNG, _ = base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+a9u0AAAAASUVORK5CYII=")

type remoteImageTransport func(*http.Request) (*http.Response, error)

func (f remoteImageTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func withRemoteImageTransport(t *testing.T, f remoteImageTransport) {
	t.Helper()
	old := httpClient
	client := *old
	client.Transport = f
	httpClient = &client
	t.Cleanup(func() { httpClient = old })
}

func remoteImageHTTPResponse(r *http.Request, status int, data []byte) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(data)), ContentLength: int64(len(data)), Request: r}
}

type remoteImageAPI struct {
	openapi.OpenAPI
	uploads      []dto.RichMediaMessage
	messages     []*dto.MessageToCreate
	urlErr       error
	binaryErr    error
	emptyInfo    bool
	prepareCalls int
}

func (m *remoteImageAPI) post(msg dto.APIMessage) (*dto.MediaResponse, *dto.Message, error) {
	if rich, ok := msg.(*dto.RichMediaMessage); ok {
		m.uploads = append(m.uploads, *rich)
		if rich.URL != "" && m.urlErr != nil {
			return nil, nil, m.urlErr
		}
		if rich.FileData != "" && m.binaryErr != nil {
			return nil, nil, m.binaryErr
		}
		info := "uploaded-file-info"
		if m.emptyInfo {
			info = ""
		}
		return &dto.MediaResponse{FileInfo: info}, nil, nil
	}
	gm := msg.(*dto.MessageToCreate)
	m.messages = append(m.messages, gm)
	return nil, &dto.Message{ID: "remote-image-message"}, nil
}

func (m *remoteImageAPI) PostGroupMessage(_ context.Context, _ string, msg dto.APIMessage) (*dto.GroupMessageResponse, error) {
	media, message, err := m.post(msg)
	return &dto.GroupMessageResponse{MediaResponse: media, Message: message}, err
}

func (m *remoteImageAPI) PostC2CMessage(_ context.Context, _ string, msg dto.APIMessage) (*dto.C2CMessageResponse, error) {
	media, message, err := m.post(msg)
	return &dto.C2CMessageResponse{MediaResponse: media, Message: message}, err
}

func (m *remoteImageAPI) FileUploadPrepare(_ context.Context, _ string, _ bool, _ *dto.UploadPrepareRequest) (*dto.UploadPrepareResponse, error) {
	m.prepareCalls++
	return nil, errors.New("chunk-prepare-sentinel")
}

func TestRemoteImageRelaySendAndReceipt(t *testing.T) {
	for _, mode := range []string{"legacy", "new"} {
		for _, group := range []bool{true, false} {
			for _, mixed := range []bool{false, true} {
				for _, failUpload := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/group=%t/mixed=%t/fail=%t", mode, group, mixed, failUpload), func(t *testing.T) {
						archModeLoadConfig(t, mode)
						oldService := outboundSvc
						outboundSvc = nil
						defer func() { outboundSvc = oldService }()
						downloads := 0
						withRemoteImageTransport(t, func(r *http.Request) (*http.Response, error) {
							downloads++
							return remoteImageHTTPResponse(r, 200, remoteTestPNG), nil
						})
						api := &remoteImageAPI{urlErr: errors.New(`{"code":40093007,"message":"富媒体文件下载失败"}`)}
						if failUpload {
							api.binaryErr = errors.New("binary-upload-rejected")
						}
						client := &privateOnceClient{}
						msg := callapi.ActionMessage{Action: "send_private_msg", Echo: "remote-image-echo", Params: callapi.ParamsContent{UserID: strings.Repeat("c", 32), GroupID: ""}}
						if group {
							msg.Action = "send_group_msg_group"
							msg.Params.GroupID = strings.Repeat("d", 32)
						}
						text := ""
						segments := []interface{}{map[string]interface{}{"type": "image", "data": map[string]interface{}{"file": "http://93.184.216.34/image.png"}}}
						if mixed {
							text = "图片说明"
							segments = append(segments, textSegment(text))
						}
						msg.Params.Message = segments
						var result string
						var err error
						if mode == "legacy" {
							if group {
								result, err = HandleSendGroupMsg(client, nil, api, msg)
							} else {
								result, err = HandleSendPrivateMsg(client, nil, api, msg)
							}
						} else {
							found := map[string][]string{"url_image": {"93.184.216.34/image.png"}}
							var id string
							var target identity.ResolvedTarget
							if group {
								id, err = SendFoundItemsGroup(context.Background(), msg.Params.GroupID.(string), text, found, api)
								target = identity.ResolvedTarget{Kind: identity.TargetGroup, Group: &identity.ResolvedGroup{OpenID: identity.OpenGroupID(msg.Params.GroupID.(string))}}
							} else {
								id, err = SendFoundItemsPrivate(context.Background(), msg.Params.UserID.(string), text, found, false, api)
								target = identity.ResolvedTarget{Kind: identity.TargetPrivate, User: &identity.ResolvedUser{OpenID: identity.OpenID(msg.Params.UserID.(string))}}
							}
							if failUpload && !isMediaUploadFailure(err) {
								t.Fatalf("上传错误未沿 new 路径传回: %v", err)
							}
							result = sendOutboundReceipt(client, nil, api, msg, target, id, err)
						}
						if result == "" || len(client.responses) != 1 {
							t.Fatalf("回执 %q, count=%d, err=%v", result, len(client.responses), err)
						}
						var response ServerResponse
						if err := json.Unmarshal([]byte(result), &response); err != nil {
							t.Fatal(err)
						}
						if downloads != 1 || len(api.uploads) != 2 || api.uploads[1].URL != "" {
							t.Fatalf("中转应下载一次并上传两次: downloads=%d, uploads=%+v", downloads, api.uploads)
						}
						data, err := base64.StdEncoding.DecodeString(api.uploads[1].FileData)
						if err != nil || !bytes.Equal(data, remoteTestPNG) {
							t.Fatal("中转未上传原始图片字节")
						}
						if response.Echo != msg.Echo {
							t.Fatal("回执丢失 echo")
						}
						if failUpload {
							if response.Status != "failed" || response.RetCode == 0 || response.Data.MessageID != 0 || len(api.messages) != 0 {
								t.Fatalf("上传失败被报成成功或继续发送: %+v, sends=%d", response, len(api.messages))
							}
						} else if response.Status != "ok" || response.RetCode != 0 || len(api.messages) != 1 || api.messages[0].MsgType != 7 || api.messages[0].Media.FileInfo != "uploaded-file-info" {
							t.Fatalf("中转成功应只发送一条媒体消息: %+v, messages=%+v", response, api.messages)
						}
					})
				}
			}
		}
	}
}

func TestRemoteImageDownloadValidation(t *testing.T) {
	for _, tc := range []struct{ name, address string }{
		{"http", "http://93.184.216.34/image"},
		{"https", "https://93.184.216.34/image"},
		{"redirect", "http://93.184.216.34/redirect"},
		{"private_redirect", "http://93.184.216.34/private"},
		{"http_error", "http://93.184.216.34/missing"},
		{"html", "http://93.184.216.34/html"},
		{"too_large", "http://93.184.216.34/large"},
		{"loopback", "http://127.0.0.1/image"},
		{"unspecified", "http://0.0.0.0/image"},
		{"scheme", "file:///image.png"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withRemoteImageTransport(t, func(r *http.Request) (*http.Response, error) {
				resp := remoteImageHTTPResponse(r, 200, remoteTestPNG)
				switch r.URL.Path {
				case "/redirect", "/private":
					resp.StatusCode = 302
					location := "https://93.184.216.34/image"
					if r.URL.Path == "/private" {
						location = "http://127.0.0.1/image"
					}
					resp.Header.Set("Location", location)
				case "/missing":
					resp.StatusCode = 404
				case "/html":
					resp.Body = io.NopCloser(strings.NewReader("<html>not an image</html>"))
				case "/large":
					resp.ContentLength = maxRemoteImageBytes + 1
				}
				return resp, nil
			})
			data, err := downloadRemoteImage(context.Background(), tc.address)
			good := tc.name == "http" || tc.name == "https" || tc.name == "redirect"
			if good && (err != nil || !bytes.Equal(data, remoteTestPNG)) {
				t.Fatalf("下载失败: %v", err)
			}
			if !good && err == nil {
				t.Fatal("无效图片或目标未被拒绝")
			}
		})
	}
}

func TestRemoteImageFallbackBoundaries(t *testing.T) {
	withRemoteImageTransport(t, func(r *http.Request) (*http.Response, error) {
		return nil, errors.New("不应下载")
	})
	for _, tc := range []struct {
		name     string
		err      error
		fileType uint64
		srvSend  bool
	}{
		{"direct_success", nil, 1, false},
		{"permission_denied", errors.New(`{"code":40034105}`), 1, false},
		{"video", errors.New(`{"code":40093007}`), 2, false},
		{"server_sends", errors.New(`{"code":40093007}`), 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := &remoteImageAPI{urlErr: tc.err}
			m := &dto.RichMediaMessage{URL: "https://93.184.216.34/image", FileType: tc.fileType, SrvSendMsg: tc.srvSend}
			_, err := postGroupMediaWithFallback(context.Background(), "group", m, api)
			if len(api.uploads) != 1 || (tc.err == nil) != (err == nil) || m.FileData != "" {
				t.Fatalf("非下载错误不应中转/改写请求: %+v, err=%v", api.uploads, err)
			}
		})
	}
}

func TestRemoteImageLargerThanDirectLimitUsesChunks(t *testing.T) {
	data := append(append([]byte(nil), remoteTestPNG...), make([]byte, maxDirectFileDataBytes)...)
	withRemoteImageTransport(t, func(r *http.Request) (*http.Response, error) { return remoteImageHTTPResponse(r, 200, data), nil })
	api := &remoteImageAPI{urlErr: errors.New(`{"code":40093007}`)}
	_, err := postGroupMediaWithFallback(context.Background(), "group", &dto.RichMediaMessage{URL: "https://93.184.216.34/image", FileType: 1}, api)
	if api.prepareCalls != 1 || len(api.uploads) != 1 || err == nil || !strings.Contains(err.Error(), "chunk-prepare-sentinel") {
		t.Fatalf("大图未进入分片路径: prepare=%d uploads=%d err=%v", api.prepareCalls, len(api.uploads), err)
	}
}

func TestRemoteImageFailureStringReceiptAndEmptyUploadResult(t *testing.T) {
	client := &privateOnceClient{}
	msg := callapi.ActionMessage{Params: callapi.ParamsContent{GroupID: "group"}, Echo: "string-echo"}
	var err error = &mediaUploadError{cause: errors.New("upload-failed")}
	result, sendErr := SendResponseSB(client, err, &msg, &dto.GroupMessageResponse{Message: &dto.Message{ID: "stale-id"}}, nil, nil)
	if sendErr != nil {
		t.Fatal(sendErr)
	}
	var response ServerResponseSB
	if err := json.Unmarshal([]byte(result), &response); err != nil {
		t.Fatal(err)
	}
	if response.Status != "failed" || response.RetCode != 100 || response.Data.MessageID != "" || response.Echo != msg.Echo {
		t.Fatalf("字符串回执未报告上传失败: %+v", response)
	}
	api := &remoteImageAPI{emptyInfo: true}
	_, err = postGroupMediaWithFallback(context.Background(), "group", &dto.RichMediaMessage{URL: "https://example.com/image", FileType: 1}, api)
	if !isMediaUploadFailure(err) {
		t.Fatalf("空上传结果未被识别为失败: %v", err)
	}
}
