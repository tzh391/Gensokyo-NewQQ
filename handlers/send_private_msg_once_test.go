package handlers

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/hoshinonyaruko/gensokyo/callapi"
	"github.com/hoshinonyaruko/gensokyo/config"
	"github.com/hoshinonyaruko/gensokyo/echo"
	"github.com/hoshinonyaruko/gensokyo/idmap"
)

type privateOnceClient struct {
	responses []map[string]interface{}
}

func (c *privateOnceClient) SendMessage(response map[string]interface{}) error {
	c.responses = append(c.responses, response)
	return nil
}

func TestSendPrivateMsgVirtualIDOnce(t *testing.T) {
	for i, msgType := range []string{"", "group_private", "group"} {
		t.Run(fmt.Sprintf("cached_type_%q", msgType), func(t *testing.T) {
			rawID := strings.Repeat(fmt.Sprintf("%x", i+10), 32)
			virtualID, err := idmap.StoreIDv2(rawID)
			if err != nil {
				t.Fatal(err)
			}
			echo.AddMsgType(config.GetAppIDStr(), virtualID, msgType)
			// 旧递归计数残留也不应导致同一请求重发。
			echo.AddMapping(virtualID, 2)
			client := &privateOnceClient{}
			api := &mockMDSendOpenAPI{}
			msg := callapi.ActionMessage{
				Action: "send_private_msg",
				Params: callapi.ParamsContent{
					UserID: fmt.Sprint(virtualID), GroupID: "",
					Message: []interface{}{textSegment("123")},
				},
				Echo: fmt.Sprintf("send_private_msg:once-%d", i),
			}
			result, err := HandleSendPrivateMsg(client, nil, api, msg)
			if err != nil {
				t.Fatal(err)
			}
			if api.c2cCalls != 1 || api.groupCalls != 0 {
				t.Fatalf("C2C 调用 %d 次，群调用 %d 次，期望 1/0", api.c2cCalls, api.groupCalls)
			}
			if api.lastC2CID != rawID {
				t.Fatalf("目标 OpenID = %q, want %q", api.lastC2CID, rawID)
			}
			if result == "" || len(client.responses) != 1 {
				t.Fatalf("返回值 %q，回执数 %d，期望一个有效回执", result, len(client.responses))
			}
			var response ServerResponse
			if err := json.Unmarshal([]byte(result), &response); err != nil {
				t.Fatal(err)
			}
			if response.UserID != virtualID || response.Echo != msg.Echo {
				t.Fatalf("回执 user_id/echo = %d/%v, want %d/%v", response.UserID, response.Echo, virtualID, msg.Echo)
			}
		})
	}
}
