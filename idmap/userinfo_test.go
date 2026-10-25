package idmap

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hoshinonyaruko/gensokyo/structs"
)

func TestMain(m *testing.M) {
	tmp, err := os.MkdirTemp("", "gensokyo-idmap-test-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := os.Chdir(tmp); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	code := m.Run()
	CloseDB()
	_ = os.Chdir(os.TempDir())
	_ = os.RemoveAll(tmp)
	os.Exit(code)
}

func TestObservedNicknamePersistsAndMerges(t *testing.T) {
	InitializeDB()
	rawID := strings.Repeat("d", 32)
	virtualID, err := StoreIDv2(rawID)
	if err != nil {
		t.Fatal(err)
	}
	userID := strconv.FormatInt(virtualID, 10)
	if err := StoreUserInfo(rawID, structs.FriendData{UserID: userID, Remark: "已知联系人"}); err != nil {
		t.Fatal(err)
	}
	StoreUserName(userID, "测试昵称")
	usernameCache.Store(userID, usernameCacheItem{Username: "测试昵称", Timestamp: time.Now().Add(-11 * time.Minute)})
	if got := GetUserName(userID); got != "" {
		t.Fatalf("过期内存缓存 = %q, want 空", got)
	}
	if err := StoreUserInfo(rawID, structs.FriendData{UserID: userID}); err != nil {
		t.Fatal(err)
	}
	assertProfile := func(want string) {
		t.Helper()
		users, err := ListAllUsers()
		if err != nil {
			t.Fatal(err)
		}
		for _, user := range users {
			if user.UserID == userID {
				if user.Nickname != want || user.Remark != "已知联系人" {
					t.Fatalf("用户资料 = %+v, want nickname=%q 且保留备注", user, want)
				}
				return
			}
		}
		t.Fatal("好友资料中缺少已观测用户")
	}
	assertProfile("测试昵称")
	StoreUserName(userID, "更新昵称")
	StoreUserName(userID, "")
	assertProfile("更新昵称")
}
