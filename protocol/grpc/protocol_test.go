package grpc

import (
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/quick"
	"time"
	"unicode/utf8"

	"github.com/google/go-cmp/cmp"
	"github.com/opensraph/srpc/encoding"
	"github.com/opensraph/srpc/errors"
	"github.com/opensraph/srpc/internal/envelope"
	"github.com/opensraph/srpc/mem"
	"github.com/opensraph/srpc/protocol"
	"github.com/stretchr/testify/assert"
)

// TestGRPCHandlerSender 测试 gRPC 处理器发送器
func TestGRPCHandlerSender(t *testing.T) {
	t.Parallel()

	// 创建一个新的 gRPC 处理连接的辅助函数
	newConn := func(web bool) *grpcHandlerConn {
		responseWriter := httptest.NewRecorder()
		protobufCodec := encoding.GetCodec(encoding.CodecNameProto)
		bufferPool := mem.DefaultBufferPool()
		request, err := http.NewRequest(
			http.MethodPost,
			"https://demo.example.com",
			strings.NewReader(""),
		)
		assert.Nil(t, err)
		return &grpcHandlerConn{
			spec:       protocol.Spec{},
			web:        web,
			bufferPool: bufferPool,
			protobuf:   protobufCodec,
			marshaler: grpcMarshaler{
				EnvelopeWriter: envelope.EnvelopeWriter{
					Sender:     envelope.WriteSender{Writer: responseWriter},
					Codec:      protobufCodec,
					BufferPool: bufferPool,
				},
			},
			responseWriter:  responseWriter,
			responseHeader:  make(http.Header),
			responseTrailer: make(http.Header),
			request:         request,
			unmarshaler: grpcUnmarshaler{
				EnvelopeReader: envelope.EnvelopeReader{
					Reader:     request.Body,
					Codec:      protobufCodec,
					BufferPool: bufferPool,
				},
			},
		}
	}

	tests := []struct {
		name   string
		web    bool
		testFn func(t *testing.T, conn protocol.HandlerConnCloser)
	}{
		{
			name: "web connection",
			web:  true,
			testFn: func(t *testing.T, conn protocol.HandlerConnCloser) {
				testGRPCHandlerConnMetadata(t, conn)
			},
		},
		{
			name: "http2 connection",
			web:  false,
			testFn: func(t *testing.T, conn protocol.HandlerConnCloser) {
				testGRPCHandlerConnMetadata(t, conn)
			},
		},
	}

	for _, tt := range tests {
		tt := tt // 捕获循环变量
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			conn := newConn(tt.web)
			tt.testFn(t, conn)
		})
	}
}

func testGRPCHandlerConnMetadata(t *testing.T, conn protocol.HandlerConnCloser) {
	// Closing the sender shouldn't unpredictably mutate user-visible headers or
	// trailers.
	t.Helper()
	expectHeaders := conn.ResponseHeader().Clone()
	expectTrailers := conn.ResponseTrailer().Clone()
	conn.Close(errors.New("oh no").WithCode(errors.Unavailable))
	if diff := cmp.Diff(expectHeaders, conn.ResponseHeader()); diff != "" {
		t.Errorf("headers changed:\n%s", diff)
	}
	gotTrailers := conn.ResponseTrailer()
	if diff := cmp.Diff(expectTrailers, gotTrailers); diff != "" {
		t.Errorf("trailers changed:\n%s", diff)
	}
}

func TestGRPCParseTimeout(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		input         string
		wantDuration  time.Duration
		wantErr       bool
		wantNoTimeout bool
	}{
		{
			name:          "empty string",
			input:         "",
			wantErr:       true,
			wantNoTimeout: true,
		},
		{
			name:    "invalid format",
			input:   "foo",
			wantErr: true,
		},
		{
			name:    "invalid unit",
			input:   "12xS",
			wantErr: true,
		},
		{
			name:          "too many digits",
			input:         "999999999n", // 9 digits
			wantErr:       true,
			wantNoTimeout: false,
		},
		{
			name:          "overflow",
			input:         "99999999H", // 8 digits but overflows time.Duration
			wantErr:       true,
			wantNoTimeout: true,
		},
		{
			name:         "valid seconds",
			input:        "45S",
			wantDuration: 45 * time.Second,
		},
		{
			name:         "max valid",
			input:        "99999999S", // 8 digits, shouldn't overflow
			wantDuration: 99999999 * time.Second,
		},
	}

	for _, tt := range tests {
		tt := tt // 捕获循环变量
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			duration, err := grpcParseTimeout(tt.input)

			if tt.wantErr {
				assert.NotNil(t, err, "expected error for input %q", tt.input)
				if tt.wantNoTimeout {
					assert.True(t, errors.Is(err, errNoTimeout), "expected errNoTimeout for input %q", tt.input)
				} else if tt.input != "" {
					assert.False(t, errors.Is(err, errNoTimeout), "unexpected errNoTimeout for input %q", tt.input)
				}
				return
			}

			assert.Nil(t, err, "unexpected error for input %q: %v", tt.input, err)
			assert.Equal(t, tt.wantDuration, duration, "incorrect duration for input %q", tt.input)
		})
	}
}

func TestGRPCEncodeTimeout(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    time.Duration
		expected string
	}{
		{
			name:     "hour and second",
			input:    time.Hour + time.Second,
			expected: "3601000m", // NB, m is milliseconds
		},
		{
			name:     "overflow max int64",
			input:    time.Duration(math.MaxInt64),
			expected: "2562047H",
		},
		{
			name:     "negative value",
			input:    -1,
			expected: "0n",
		},
		{
			name:     "negative hour",
			input:    -1 * time.Hour,
			expected: "0n",
		},
		{
			name:     "eight digits nanoseconds",
			input:    99999999 * time.Nanosecond, // shouldn't need unit conversion
			expected: "99999999n",
		},
		{
			name:     "nine digits nanoseconds with conversion",
			input:    99999999*time.Nanosecond + 1, // 9 digits, convert to micros
			expected: "100000u",
		},
		{
			name:     "milliseconds with nanosecond",
			input:    10*time.Millisecond + 1, // shouldn't round
			expected: "10000001n",
		},
		{
			name:     "seconds with nanosecond rounding",
			input:    10*time.Second + 1, // should round down
			expected: "10000000u",
		},
	}

	for _, tt := range tests {
		tt := tt // 捕获循环变量
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := grpcEncodeTimeout(tt.input)
			assert.Equal(t, tt.expected, got, "input: %v", tt.input)
		})
	}
}

func TestGRPCPercentEncodingQuick(t *testing.T) {
	t.Parallel()

	// 使用 quick 包进行随机测试，确保编码和解码是互逆操作
	roundtrip := func(input string) bool {
		if !utf8.ValidString(input) {
			return true // 跳过非UTF-8字符串
		}
		encoded := grpcPercentEncode(input)
		decoded, err := grpcPercentDecode(encoded)
		return err == nil && decoded == input
	}

	if err := quick.Check(roundtrip, nil /* config */); err != nil {
		t.Error(err)
	}
}

func TestGRPCPercentEncoding(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
	}{
		{
			name:  "simple string",
			input: "foo",
		},
		{
			name:  "string with spaces",
			input: "foo bar",
		},
		{
			name:  "string with percent",
			input: `foo%bar`,
		},
		{
			name:  "string with non-ascii chars",
			input: "fiancée",
		},
		{
			name:  "string with unicode",
			input: "你好，世界",
		},
		{
			name:  "string with special chars",
			input: "!@#$^&*()_+{}:<>?[]|\\;',./",
		},
	}

	for _, tt := range tests {
		tt := tt // 捕获循环变量
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// 确保输入是有效的UTF-8字符串
			assert.True(t, utf8.ValidString(tt.input), "input invalid UTF-8: %q", tt.input)

			// 编码测试
			encoded := grpcPercentEncode(tt.input)
			t.Logf("%q encoded as %q", tt.input, encoded)

			// 解码测试
			decoded, err := grpcPercentDecode(encoded)
			assert.Nil(t, err, "unexpected error decoding %q: %v", encoded, err)
			assert.Equal(t, tt.input, decoded, "roundtrip failed for %q", tt.input)
		})
	}
}

// TestGRPCWebTrailerMarshalling 测试 gRPC Web trailer 编组
func TestGRPCWebTrailerMarshalling(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		trailers         map[string]string
		expectedContains []string
	}{
		{
			name: "simple status",
			trailers: map[string]string{
				"grpc-status": "0",
			},
			expectedContains: []string{
				"grpc-status: 0",
			},
		},
		{
			name: "status with message",
			trailers: map[string]string{
				"grpc-status":  "0",
				"Grpc-Message": "Foo",
			},
			expectedContains: []string{
				"grpc-status: 0",
				"grpc-message: Foo",
			},
		},
		{
			name: "status with custom headers",
			trailers: map[string]string{
				"grpc-status":    "0",
				"Grpc-Message":   "Foo",
				"User-Provided":  "bar",
				"Another-Header": "value",
			},
			expectedContains: []string{
				"grpc-status: 0",
				"grpc-message: Foo",
				"user-provided: bar",
				"another-header: value",
			},
		},
	}

	for _, tt := range tests {
		tt := tt // 捕获循环变量
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			responseWriter := httptest.NewRecorder()
			marshaler := grpcMarshaler{
				EnvelopeWriter: envelope.EnvelopeWriter{
					Sender:     envelope.WriteSender{Writer: responseWriter},
					BufferPool: mem.DefaultBufferPool(),
				},
			}

			trailer := http.Header{}
			for k, v := range tt.trailers {
				trailer.Add(k, v)
			}

			err := marshaler.MarshalWebTrailers(trailer)
			assert.Nil(t, err, "unexpected error: %v", err)

			responseWriter.Body.Next(5) // 跳过 flags 和消息长度
			marshalled := responseWriter.Body.String()

			for _, expected := range tt.expectedContains {
				assert.True(t, strings.Contains(marshalled, expected),
					"marshalled data %q does not contain %q", marshalled, expected)
			}
		})
	}
}

func TestGRPCValidateResponseContentType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name               string
		web                bool
		codecName          string
		contentType        string
		expectError        bool
		expectErrorCode    errors.Code
		expectErrorMsgPart string
	}{
		// 有效的内容类型
		{
			name:        "HTTP2 Proto",
			codecName:   encoding.CodecNameProto,
			contentType: "application/grpc",
		},
		{
			name:        "HTTP2 Proto with suffix",
			codecName:   encoding.CodecNameProto,
			contentType: "application/grpc+proto",
		},
		{
			name:        "HTTP2 JSON",
			codecName:   encoding.CodecNameJSON,
			contentType: "application/grpc+json",
		},
		{
			name:        "Web Proto",
			web:         true,
			codecName:   encoding.CodecNameProto,
			contentType: "application/grpc-web",
		},
		{
			name:        "Web Proto with suffix",
			web:         true,
			codecName:   encoding.CodecNameProto,
			contentType: "application/grpc-web+proto",
		},
		{
			name:        "Web JSON",
			web:         true,
			codecName:   encoding.CodecNameJSON,
			contentType: "application/grpc-web+json",
		},

		// 编解码器不匹配的内容类型
		{
			name:               "HTTP2 Proto with JSON content type",
			codecName:          encoding.CodecNameProto,
			contentType:        "application/grpc+json",
			expectError:        true,
			expectErrorCode:    errors.Internal,
			expectErrorMsgPart: "invalid content-type",
		},
		{
			name:               "HTTP2 JSON with generic content type",
			codecName:          encoding.CodecNameJSON,
			contentType:        "application/grpc",
			expectError:        true,
			expectErrorCode:    errors.Internal,
			expectErrorMsgPart: "invalid content-type",
		},
		{
			name:               "HTTP2 JSON with Proto content type",
			codecName:          encoding.CodecNameJSON,
			contentType:        "application/grpc+proto",
			expectError:        true,
			expectErrorCode:    errors.Internal,
			expectErrorMsgPart: "invalid content-type",
		},
		{
			name:               "Web Proto with JSON content type",
			web:                true,
			codecName:          encoding.CodecNameProto,
			contentType:        "application/grpc-web+json",
			expectError:        true,
			expectErrorCode:    errors.Internal,
			expectErrorMsgPart: "invalid content-type",
		},
		{
			name:               "Web JSON with generic content type",
			web:                true,
			codecName:          encoding.CodecNameJSON,
			contentType:        "application/grpc-web",
			expectError:        true,
			expectErrorCode:    errors.Internal,
			expectErrorMsgPart: "invalid content-type",
		},
		{
			name:               "Web JSON with Proto content type",
			web:                true,
			codecName:          encoding.CodecNameJSON,
			contentType:        "application/grpc-web+proto",
			expectError:        true,
			expectErrorCode:    errors.Internal,
			expectErrorMsgPart: "invalid content-type",
		},

		// 非法的内容类型
		{
			name:               "HTTP2 with plain Proto content type",
			codecName:          encoding.CodecNameProto,
			contentType:        "application/proto",
			expectError:        true,
			expectErrorCode:    errors.Unknown,
			expectErrorMsgPart: "invalid content-type",
		},
		{
			name:               "HTTP2 with Web content type",
			codecName:          encoding.CodecNameProto,
			contentType:        "application/grpc-web",
			expectError:        true,
			expectErrorCode:    errors.Unknown,
			expectErrorMsgPart: "invalid content-type",
		},
		{
			name:               "HTTP2 with Web Proto content type",
			codecName:          encoding.CodecNameProto,
			contentType:        "application/grpc-web+proto",
			expectError:        true,
			expectErrorCode:    errors.Unknown,
			expectErrorMsgPart: "invalid content-type",
		},
		{
			name:               "HTTP2 with plain JSON content type",
			codecName:          encoding.CodecNameJSON,
			contentType:        "application/json",
			expectError:        true,
			expectErrorCode:    errors.Unknown,
			expectErrorMsgPart: "invalid content-type",
		},
		{
			name:               "HTTP2 with Web JSON content type",
			codecName:          encoding.CodecNameJSON,
			contentType:        "application/grpc-web+json",
			expectError:        true,
			expectErrorCode:    errors.Unknown,
			expectErrorMsgPart: "invalid content-type",
		},
		{
			name:               "Web with plain Proto content type",
			web:                true,
			codecName:          encoding.CodecNameProto,
			contentType:        "application/proto",
			expectError:        true,
			expectErrorCode:    errors.Unknown,
			expectErrorMsgPart: "invalid content-type",
		},
		{
			name:               "Web with HTTP2 content type",
			web:                true,
			codecName:          encoding.CodecNameProto,
			contentType:        "application/grpc",
			expectError:        true,
			expectErrorCode:    errors.Unknown,
			expectErrorMsgPart: "invalid content-type",
		},
		{
			name:               "Web with HTTP2 Proto content type",
			web:                true,
			codecName:          encoding.CodecNameProto,
			contentType:        "application/grpc+proto",
			expectError:        true,
			expectErrorCode:    errors.Unknown,
			expectErrorMsgPart: "invalid content-type",
		},
		{
			name:               "Web with plain JSON content type",
			web:                true,
			codecName:          encoding.CodecNameJSON,
			contentType:        "application/json",
			expectError:        true,
			expectErrorCode:    errors.Unknown,
			expectErrorMsgPart: "invalid content-type",
		},
		{
			name:               "Web with HTTP2 JSON content type",
			web:                true,
			codecName:          encoding.CodecNameJSON,
			contentType:        "application/grpc+json",
			expectError:        true,
			expectErrorCode:    errors.Unknown,
			expectErrorMsgPart: "invalid content-type",
		},
		{
			name:               "HTTP2 with invalid content type",
			codecName:          encoding.CodecNameProto,
			contentType:        "some/garbage",
			expectError:        true,
			expectErrorCode:    errors.Unknown,
			expectErrorMsgPart: "invalid content-type",
		},
	}

	for _, tt := range tests {
		tt := tt // 捕获循环变量
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := grpcValidateResponseContentType(
				tt.web,
				tt.codecName,
				tt.contentType,
			)

			if !tt.expectError {
				assert.Nil(t, err, "unexpected error: %v", err)
				return
			}

			assert.NotNil(t, err, "expected error but got nil")
			assert.Equal(t, tt.expectErrorCode, errors.CodeOf(err), "wrong error code")
			assert.True(t, strings.Contains(err.Error(), tt.expectErrorMsgPart),
				"error message %q does not contain %q", err.Error(), tt.expectErrorMsgPart)
		})
	}
}
