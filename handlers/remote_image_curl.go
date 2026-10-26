package handlers

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"runtime"
)

// curlImageRequest 仅在源站拒绝 Go HTTP 客户端（567）时使用系统 curl。
// 不启用 -L，重定向交回 http.Client，复用每跳的地址检查。
var curlImageRequest = runCurlImageRequest

type curlImageTransport struct{}

func (curlImageTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return curlImageRequest(req)
}

func runCurlImageRequest(req *http.Request) (*http.Response, error) {
	name := "curl"
	if runtime.GOOS == "windows" {
		name = "curl.exe"
	}
	path, err := exec.LookPath(name)
	if err != nil {
		return nil, fmt.Errorf("源站返回 HTTP 567，系统未找到 %s: %w", name, err)
	}
	headers, err := os.CreateTemp("", "gensokyo-image-headers-*")
	if err != nil {
		return nil, err
	}
	defer os.Remove(headers.Name())
	headers.Close()
	cmd := exec.CommandContext(req.Context(), path,
		"--disable", "--silent", "--show-error", "--http1.1",
		"--proto", "=http,https", "--connect-timeout", "10", "--max-time", "30",
		"--max-filesize", fmt.Sprint(maxRemoteImageBytes),
		"--dump-header", headers.Name(), "--output", "-", "--url", req.URL.String())
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	data, readErr := io.ReadAll(io.LimitReader(stdout, maxRemoteImageBytes+1))
	if readErr != nil || int64(len(data)) > maxRemoteImageBytes {
		cmd.Process.Kill()
		cmd.Wait()
		if readErr != nil {
			return nil, readErr
		}
		return nil, fmt.Errorf("远程图片超过中转上限 20 MiB")
	}
	if err := cmd.Wait(); err != nil {
		return nil, fmt.Errorf("curl 图片下载失败: %w: %s", err, stderr.String())
	}
	f, err := os.Open(headers.Name())
	if err != nil {
		return nil, err
	}
	defer f.Close()
	headerData, err := io.ReadAll(io.LimitReader(f, 1024*1024+1))
	if err != nil || len(headerData) > 1024*1024 {
		return nil, fmt.Errorf("curl 响应头无效或过大")
	}
	reader := bufio.NewReader(bytes.NewReader(headerData))
	var response *http.Response
	for {
		next, err := http.ReadResponse(reader, req)
		if err != nil {
			return nil, fmt.Errorf("解析 curl 响应头失败: %w", err)
		}
		response = next
		if _, err := reader.Peek(1); err == io.EOF {
			break
		} else if err != nil {
			return nil, err
		}
	}
	response.Body = io.NopCloser(bytes.NewReader(data))
	response.ContentLength = int64(len(data))
	return response, nil
}
