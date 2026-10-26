package handlers

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/hoshinonyaruko/gensokyo/mylog"
	"github.com/tencent-connect/botgo/dto"
	"github.com/tencent-connect/botgo/openapi"
)

const maxRemoteImageBytes int64 = 20 * 1024 * 1024

// mediaUploadError 标记上传阶段的确定失败，区别于消息已提交后的异步审核。
type mediaUploadError struct{ cause error }

func (e *mediaUploadError) Error() string { return e.cause.Error() }
func (e *mediaUploadError) Unwrap() error { return e.cause }

func isMediaUploadFailure(err error) bool {
	var uploadErr *mediaUploadError
	return errors.As(err, &uploadErr) || IsQQError(err, 40093007) || IsQQError(err, 850026)
}

func mediaUploadResponseStatus(err error) (int, string) {
	if isMediaUploadFailure(err) {
		return 100, "failed"
	}
	return 0, "ok"
}

func shouldRelayRemoteImage(m *dto.RichMediaMessage, err error) bool {
	return m != nil && m.FileType == 1 && m.URL != "" && m.FileData == "" && !m.SrvSendMsg &&
		(IsQQError(err, 40093007) || IsQQError(err, 850026))
}

func downloadRemoteImage(ctx context.Context, rawURL string) ([]byte, error) {
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil {
		return nil, fmt.Errorf("远程图片必须使用有效的 HTTP(S) URL")
	}
	if isPrivateOrLoopback(rawURL) {
		return nil, fmt.Errorf("远程图片地址被 SSRF 检查拒绝")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("下载远程图片失败: %w", err)
	}
	if resp.StatusCode == 567 {
		resp.Body.Close()
		mylog.Printf("源站返回 HTTP 567，尝试系统 curl 下载图片")
		curlClient := &http.Client{Timeout: httpClient.Timeout, CheckRedirect: httpClient.CheckRedirect, Transport: curlImageTransport{}}
		resp, err = curlClient.Do(req.Clone(ctx))
		if err != nil {
			return nil, fmt.Errorf("HTTP 567 后 curl 图片下载失败: %w", err)
		}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("下载远程图片返回 HTTP %d", resp.StatusCode)
	}
	if resp.ContentLength > maxRemoteImageBytes {
		return nil, fmt.Errorf("远程图片超过中转上限 20 MiB")
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxRemoteImageBytes+1))
	if err != nil {
		return nil, fmt.Errorf("读取远程图片失败: %w", err)
	}
	if int64(len(data)) > maxRemoteImageBytes {
		return nil, fmt.Errorf("远程图片超过中转上限 20 MiB")
	}
	if !strings.HasPrefix(http.DetectContentType(data), "image/") {
		return nil, fmt.Errorf("远程地址未返回图片数据")
	}
	return data, nil
}

func relayRemoteImage(ctx context.Context, id string, group bool, m *dto.RichMediaMessage, api openapi.OpenAPI, originalErr error) (string, error) {
	mylog.Printf("QQ 端拉取网络图片失败，尝试本机下载后上传")
	data, err := downloadRemoteImage(ctx, m.URL)
	if err != nil {
		return "", fmt.Errorf("%v；本机中转失败: %w", originalErr, err)
	}
	local := *m
	local.URL = ""
	local.FileData = base64.StdEncoding.EncodeToString(data)
	var fileInfo string
	if group {
		fileInfo, err = uploadMedia(ctx, id, &local, api)
	} else {
		fileInfo, err = uploadMediaPrivate(ctx, id, &local, api)
	}
	if err != nil {
		return "", fmt.Errorf("%v；本机中转上传失败: %w", originalErr, err)
	}
	return fileInfo, nil
}

func postGroupMediaWithFallback(ctx context.Context, id string, m *dto.RichMediaMessage, api openapi.OpenAPI) (*dto.GroupMessageResponse, error) {
	resp, err := api.PostGroupMessage(ctx, id, m)
	if shouldRelayRemoteImage(m, err) {
		var fileInfo string
		fileInfo, err = relayRemoteImage(ctx, id, true, m, api, err)
		if err == nil {
			resp = &dto.GroupMessageResponse{MediaResponse: &dto.MediaResponse{FileInfo: fileInfo}}
		}
	}
	if err != nil {
		return nil, &mediaUploadError{cause: err}
	}
	if resp == nil || resp.MediaResponse == nil || resp.MediaResponse.FileInfo == "" {
		return nil, &mediaUploadError{cause: fmt.Errorf("群媒体上传未返回 file_info")}
	}
	return resp, nil
}

func postPrivateMediaWithFallback(ctx context.Context, id string, m *dto.RichMediaMessage, api openapi.OpenAPI) (*dto.C2CMessageResponse, error) {
	resp, err := api.PostC2CMessage(ctx, id, m)
	if shouldRelayRemoteImage(m, err) {
		var fileInfo string
		fileInfo, err = relayRemoteImage(ctx, id, false, m, api, err)
		if err == nil {
			resp = &dto.C2CMessageResponse{MediaResponse: &dto.MediaResponse{FileInfo: fileInfo}}
		}
	}
	if err != nil {
		return nil, &mediaUploadError{cause: err}
	}
	if resp == nil || resp.MediaResponse == nil || resp.MediaResponse.FileInfo == "" {
		return nil, &mediaUploadError{cause: fmt.Errorf("私聊媒体上传未返回 file_info")}
	}
	return resp, nil
}
