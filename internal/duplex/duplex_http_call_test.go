package duplex

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/opensraph/srpc/errors"
)

// mockMessagePayload 实现 spec.MessagePayload 接口
type mockMessagePayload struct {
	data   []byte
	offset int
}

func newMockPayload(data []byte) *mockMessagePayload {
	return &mockMessagePayload{
		data: data,
	}
}

func (m *mockMessagePayload) Read(p []byte) (n int, err error) {
	if m.offset >= len(m.data) {
		return 0, io.EOF
	}
	n = copy(p, m.data[m.offset:])
	m.offset += n
	return n, nil
}

func (m *mockMessagePayload) Seek(offset int64, whence int) (int64, error) {
	if whence == io.SeekStart {
		m.offset = int(offset)
	} else if whence == io.SeekCurrent {
		m.offset += int(offset)
	} else if whence == io.SeekEnd {
		m.offset = len(m.data) + int(offset)
	}

	if m.offset < 0 {
		m.offset = 0
	} else if m.offset > len(m.data) {
		m.offset = len(m.data)
	}

	return int64(m.offset), nil
}

func (m *mockMessagePayload) WriteTo(w io.Writer) (int64, error) {
	if m.offset >= len(m.data) {
		return 0, nil
	}
	n, err := w.Write(m.data[m.offset:])
	m.offset += n
	return int64(n), err
}

func (m *mockMessagePayload) Len() int {
	return len(m.data)
}

func TestDuplexHTTPCall(t *testing.T) {
	// 测试基本的单工请求和响应
	t.Run("UnaryRequest", func(t *testing.T) {
		// 设置测试服务器
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, "Failed to read body", http.StatusInternalServerError)
				return
			}
			defer r.Body.Close()

			if string(body) != "request payload" {
				http.Error(w, "Unexpected request body", http.StatusBadRequest)
				return
			}

			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte("response payload"))
		}))
		defer server.Close()

		// 创建测试 URL
		testURL, _ := url.Parse(server.URL)

		// 创建 DuplexHTTPCall
		ctx := context.Background()
		call := NewDuplexHTTPCall(
			ctx,
			server.Client(),
			testURL,
			true, // 单工流
			http.Header{
				"Content-Type": []string{"application/json"},
			},
		)

		// 发送请求
		payload := newMockPayload([]byte("request payload"))
		bytesWritten, err := call.Send(payload)
		if err != nil {
			t.Fatalf("Send failed: %v", err)
		}
		if bytesWritten != int64(len("request payload")) {
			t.Errorf("Expected to write %d bytes, got %d", len("request payload"), bytesWritten)
		}

		// 关闭写入
		if err := call.CloseWrite(); err != nil {
			t.Fatalf("CloseWrite failed: %v", err)
		}

		// 获取状态码
		statusCode, err := call.ResponseStatusCode()
		if err != nil {
			t.Fatalf("Failed to get status code: %v", err)
		}
		if statusCode != http.StatusOK {
			t.Errorf("Expected status code %d, got %d", http.StatusOK, statusCode)
		}

		// 读取响应
		respData := make([]byte, 1024)
		n, err := call.Read(respData)
		if err != nil && err != io.EOF {
			t.Fatalf("Read failed: %v", err)
		}

		respBody := string(respData[:n])
		if respBody != "response payload" {
			t.Errorf("Expected response body %q, got %q", "response payload", respBody)
		}

		// 关闭读取
		if err := call.CloseRead(); err != nil {
			t.Fatalf("CloseRead failed: %v", err)
		}
	})

	// 测试响应验证
	t.Run("ResponseValidation", func(t *testing.T) {
		// 设置测试服务器
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"status":"ok"}`))
		}))
		defer server.Close()

		testURL, _ := url.Parse(server.URL)
		ctx := context.Background()

		call := NewDuplexHTTPCall(
			ctx,
			server.Client(),
			testURL,
			true,
			http.Header{},
		)

		// 设置验证函数来验证响应
		validationCalled := false
		call.SetValidateResponse(func(resp *http.Response) error {
			validationCalled = true
			if resp.Header.Get("Content-Type") != "application/json" {
				return errors.New("invalid content type")
			}
			return nil
		})

		// 发送请求
		payload := newMockPayload([]byte(""))
		_, err := call.Send(payload)
		if err != nil {
			t.Fatalf("Send failed: %v", err)
		}

		// 等待响应就绪
		err = call.BlockUntilResponseReady()
		if err != nil {
			t.Fatalf("BlockUntilResponseReady failed: %v", err)
		}

		// 验证函数应该被调用
		if !validationCalled {
			t.Error("Validation function was not called")
		}

		// 读取响应
		respData := make([]byte, 1024)
		n, err := call.Read(respData)
		if err != nil && err != io.EOF {
			t.Fatalf("Read failed: %v", err)
		}

		// 验证 JSON 响应
		var respObj map[string]string
		if err := json.Unmarshal(respData[:n], &respObj); err != nil {
			t.Fatalf("Failed to parse JSON response: %v", err)
		}

		if respObj["status"] != "ok" {
			t.Errorf("Expected status 'ok', got %q", respObj["status"])
		}
	})

	// 测试上下文取消
	t.Run("ContextCancellation", func(t *testing.T) {
		// 创建一个可取消的上下文
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		// 创建一个会延迟响应的服务器
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// 延迟响应
			time.Sleep(500 * time.Millisecond)
			w.Write([]byte("delayed response"))
		}))
		defer server.Close()

		testURL, _ := url.Parse(server.URL)

		call := NewDuplexHTTPCall(
			ctx,
			server.Client(),
			testURL,
			true,
			http.Header{},
		)

		// 发送空请求
		go func() {
			payload := newMockPayload([]byte(""))
			call.Send(payload)
		}()

		// 立即取消上下文
		cancel()

		// 尝试读取应该返回上下文取消错误
		time.Sleep(50 * time.Millisecond) // 给后台请求一点时间

		respData := make([]byte, 1024)
		_, err := call.Read(respData)
		if err == nil || !strings.Contains(err.Error(), "context canceled") {
			t.Fatalf("Expected context canceled error, got: %v", err)
		}
	})

	// 测试 payloadCloser
	t.Run("PayloadCloser", func(t *testing.T) {
		testData := []byte("test payload data")
		payload := newMockPayload(testData)

		// 创建 payloadCloser
		pc := newPayloadCloser(payload)

		// 测试读取
		buf := make([]byte, len(testData))
		n, err := pc.Read(buf)
		if err != nil {
			t.Fatalf("Read failed: %v", err)
		}
		if n != len(testData) {
			t.Errorf("Expected to read %d bytes, got %d", len(testData), n)
		}
		if !bytes.Equal(buf[:n], testData) {
			t.Errorf("Read data doesn't match expected: %s vs %s", buf[:n], testData)
		}

		// 测试 Rewind
		if !pc.Rewind() {
			t.Fatal("Rewind failed")
		}

		// 重新读取
		n, err = pc.Read(buf)
		if err != nil {
			t.Fatalf("Read after Rewind failed: %v", err)
		}
		if n != len(testData) {
			t.Errorf("Expected to read %d bytes after Rewind, got %d", len(testData), n)
		}

		// 测试 Release
		pc.Release()

		// Release 后 Rewind 应该失败
		if pc.Rewind() {
			t.Error("Rewind should fail after Release")
		}

		// Release 后读取应该返回 EOF
		n, err = pc.Read(buf)
		if err != io.EOF {
			t.Errorf("Expected EOF after Release, got: %v", err)
		}
		if n != 0 {
			t.Errorf("Expected to read 0 bytes after Release, got %d", n)
		}
	})
}
