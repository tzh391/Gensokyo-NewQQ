package handlers_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hoshinonyaruko/gensokyo/adapter/qq"
	"github.com/hoshinonyaruko/gensokyo/callapi"
	"github.com/hoshinonyaruko/gensokyo/config"
	"github.com/hoshinonyaruko/gensokyo/handlers"
	"github.com/hoshinonyaruko/gensokyo/internal/application/outbound"
	"github.com/hoshinonyaruko/gensokyo/template"
	"github.com/tencent-connect/botgo/dto"
	"github.com/tencent-connect/botgo/openapi"
)

type schemeAPI struct {
	openapi.OpenAPI
	uploads  []*dto.RichMediaMessage
	messages []*dto.MessageToCreate
}

func (m *schemeAPI) post(msg dto.APIMessage) (*dto.MediaResponse, *dto.Message) {
	if rich, ok := msg.(*dto.RichMediaMessage); ok {
		m.uploads = append(m.uploads, rich)
		return &dto.MediaResponse{FileInfo: "scheme-file-info"}, nil
	}
	m.messages = append(m.messages, msg.(*dto.MessageToCreate))
	return nil, &dto.Message{ID: "scheme-message"}
}
func (m *schemeAPI) PostGroupMessage(_ context.Context, _ string, msg dto.APIMessage) (*dto.GroupMessageResponse, error) {
	media, message := m.post(msg)
	return &dto.GroupMessageResponse{MediaResponse: media, Message: message}, nil
}
func (m *schemeAPI) PostC2CMessage(_ context.Context, _ string, msg dto.APIMessage) (*dto.C2CMessageResponse, error) {
	media, message := m.post(msg)
	return &dto.C2CMessageResponse{MediaResponse: media, Message: message}, nil
}

func TestFullSendPathPreservesImageSchemeAndNotificationURLs(t *testing.T) {
	for _, mode := range []string{"legacy", "new"} {
		for _, group := range []bool{true, false} {
			for _, mixed := range []bool{true, false} {
				for _, scheme := range []string{"http", "https"} {
					t.Run(fmt.Sprintf("%s/group=%t/mixed=%t/%s", mode, group, mixed, scheme), func(t *testing.T) {
						yml := strings.Replace(template.ConfigTemplate, "arch_mode : new", "arch_mode : "+mode, 1)
						yml = strings.Replace(yml, "server_dir: \"<YOUR_SERVER_DIR>\"", "server_dir: \"127.0.0.1\"", 1)
						path := filepath.Join(t.TempDir(), "config.yml")
						if err := os.WriteFile(path, []byte(yml), 0600); err != nil {
							t.Fatal(err)
						}
						if _, err := config.LoadConfig(path, false); err != nil {
							t.Fatal(err)
						}
						api := &schemeAPI{}
						handlers.SetOutboundService(outbound.NewService(qq.NewQQSender(nil, api), outbound.RetryPolicy{}))
						defer handlers.SetOutboundService(nil)
						address := scheme + "://example.com/source.png?name=a&value=b"
						segments := []interface{}{map[string]interface{}{"type": "image", "data": map[string]interface{}{"file": address}}}
						text := "更新通知\nhttps://weibo.com/123456/Example\nhttps://www.bilibili.com/opus/123456789\n"
						if mixed {
							segments = append(segments, map[string]interface{}{"type": "text", "data": map[string]interface{}{"text": text}})
						}
						msg := callapi.ActionMessage{Action: "send_private_msg", Params: callapi.ParamsContent{UserID: strings.Repeat("c", 32), GroupID: "", Message: segments}, Echo: "scheme-echo"}
						client := &adapterTestClient{}
						var err error
						if group {
							msg.Action = "send_group_msg_group"
							msg.Params.GroupID = strings.Repeat("d", 32)
							_, err = handlers.HandleSendGroupMsg(client, nil, api, msg)
						} else {
							_, err = handlers.HandleSendPrivateMsg(client, nil, api, msg)
						}
						if err != nil {
							t.Fatal(err)
						}
						if len(api.uploads) != 1 || api.uploads[0].URL != address {
							t.Fatalf("实际上传 URL 被修改: %+v, want %s", api.uploads, address)
						}
						if len(api.messages) != 1 || api.messages[0].MsgType != 7 {
							t.Fatalf("应发送一条媒体消息: %+v", api.messages)
						}
						if mixed && !strings.Contains(api.messages[0].Content, strings.TrimSpace(text)) {
							t.Fatalf("实际 API 正文丢失链接: %q", api.messages[0].Content)
						}
					})
				}
			}
		}
	}
}
