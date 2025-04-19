package compress_test

import (
	"compress/gzip"
	"io"
	"strings"
	"testing"

	"github.com/opensraph/srpc/compress"
	"github.com/opensraph/srpc/errors"
	"github.com/opensraph/srpc/mem"
	"github.com/stretchr/testify/assert"
)

var gzipCompressor = gzip.NewWriter(io.Discard)
var gzipDecompressor = &gzip.Reader{}

// 验证gzip实现是否符合Compressor和Decompressor接口
var _ compress.Compressor = (*gzip.Writer)(nil)
var _ compress.Decompressor = (*gzip.Reader)(nil)

// 测试CompressionPool的创建
func TestNewCompressionPool(t *testing.T) {
	tests := []struct {
		name            string
		newDecompressor compress.Decompressor
		newCompressor   compress.Compressor
		expectNil       bool
	}{
		{
			name:            "创建完整的gzip压缩池",
			newDecompressor: gzipDecompressor,
			newCompressor:   gzipCompressor,
			expectNil:       false,
		},
		{
			name:            "仅解压缩器",
			newDecompressor: gzipDecompressor,
			newCompressor:   nil,
			expectNil:       true,
		},
		{
			name:            "仅压缩器",
			newDecompressor: nil,
			newCompressor:   gzipCompressor,
			expectNil:       true,
		},
		{
			name:            "无压缩器和解压缩器",
			newDecompressor: nil,
			newCompressor:   nil,
			expectNil:       true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pool := compress.NewCompressionPool(tc.newCompressor, tc.newDecompressor, mem.DefaultBufferPool())
			if tc.expectNil {
				assert.Nil(t, pool)
			} else {
				assert.NotNil(t, pool)
			}
		})
	}
}

// 测试使用gzip进行压缩
func TestCompressionPool_Compress_Gzip(t *testing.T) {
	testData := []struct {
		name     string
		input    string
		maxBytes int64
	}{
		{
			name:     "空数据",
			input:    "",
			maxBytes: 0,
		},
		{
			name:     "短文本",
			input:    "Hello, World!",
			maxBytes: 0,
		},
		{
			name:     "重复文本(易压缩)",
			input:    strings.Repeat("ABCDEFG", 100),
			maxBytes: 0,
		},
		{
			name:     "大数据",
			input:    strings.Repeat("The quick brown fox jumps over the lazy dog. ", 1000),
			maxBytes: 0,
		},
	}

	for _, tc := range testData {
		t.Run(tc.name, func(t *testing.T) {
			// 创建压缩池
			pool := compress.NewCompressionPool(gzipCompressor, gzipDecompressor, mem.DefaultBufferPool())
			assert.NotNil(t, pool)

			// 准备源数据
			var src mem.BufferSlice
			w := mem.NewWriter(&src, mem.DefaultBufferPool())
			_, err := w.Write([]byte(tc.input))
			assert.NoError(t, err)

			// 压缩数据
			dst, err := pool.Compress(src)
			defer dst.Free()
			assert.NoError(t, err)

			// 验证压缩后的数据不为空
			assert.Greater(t, dst.Len(), 0)

			// 如果数据有重复（易压缩），验证压缩后的大小小于原始大小
			if len(tc.input) > 100 && strings.Contains(string(tc.input), strings.Repeat("A", 10)) {
				assert.Less(t, dst.Len(), src.Len())
			}

			// 解压缩数据并验证
			result, err := pool.Decompress(dst, tc.maxBytes)
			defer result.Free()
			assert.NoError(t, err)

			// 验证解压缩后内容与原始内容相同
			resultBytes := result.Materialize()
			assert.Equal(t, tc.input, string(resultBytes))
		})
	}
}

// 测试解压缩大小限制功能
func TestCompressionPool_Decompress_SizeLimit(t *testing.T) {
	// 创建压缩池
	pool := compress.NewCompressionPool(gzipCompressor, gzipDecompressor, mem.DefaultBufferPool())
	assert.NotNil(t, pool)

	// 准备一些大数据
	largeInput := strings.Repeat("Large amount of data that will be compressed. ", 1000)
	var src mem.BufferSlice
	w := mem.NewWriter(&src, mem.DefaultBufferPool())
	_, err := w.Write([]byte(largeInput))
	assert.NoError(t, err)

	// 压缩数据
	compressed, err := pool.Compress(src)
	assert.NoError(t, err)

	tests := []struct {
		name        string
		maxBytes    int64
		expectError bool
		errorCode   errors.Code
	}{
		{
			name:        "无限制",
			maxBytes:    0,
			expectError: false,
		},
		{
			name:        "足够大的限制",
			maxBytes:    int64(len(largeInput) + 1000),
			expectError: false,
		},
		{
			name:        "过小的限制",
			maxBytes:    100,
			expectError: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var result mem.BufferSlice
			result, err := pool.Decompress(compressed, tc.maxBytes)
			defer result.Free()

			if err != nil {
				t.Logf("解压缩失败: %v", err)
			}

			resultBytes := result.Materialize()
			if tc.expectError {
				assert.Error(t, err, "解压缩应该失败")
			} else {
				assert.Equal(t, largeInput, string(resultBytes), "解压缩后的数据应该与原始数据匹配")
			}
		})
	}
}

// 测试压缩协商功能
func TestNegotiateCompression_Gzip(t *testing.T) {
	// 创建测试用的压缩池
	pools := compress.NewReadOnlyCompressionPools(
		map[string]*compress.CompressionPool{
			"gzip": compress.NewCompressionPool(gzipCompressor, gzipDecompressor, mem.DefaultBufferPool()),
			"br":   nil, // 假设br不可用
		},
		[]string{"gzip", "br"},
	)

	tests := []struct {
		name              string
		sent              string
		accept            string
		expectedRequest   string
		expectedResponse  string
		expectError       bool
		expectedErrorCode errors.Code
	}{
		{
			name:             "默认使用identity",
			sent:             "",
			accept:           "",
			expectedRequest:  compress.CompressionIdentity,
			expectedResponse: compress.CompressionIdentity,
			expectError:      false,
		},
		{
			name:             "客户端请求gzip",
			sent:             "gzip",
			accept:           "",
			expectedRequest:  "gzip",
			expectedResponse: "gzip",
			expectError:      false,
		},
		{
			name:              "客户端请求不支持的压缩",
			sent:              "deflate",
			accept:            "",
			expectError:       true,
			expectedErrorCode: errors.Unimplemented,
		},
		{
			name:             "客户端接受gzip",
			sent:             "",
			accept:           "gzip",
			expectedRequest:  compress.CompressionIdentity,
			expectedResponse: "gzip",
			expectError:      false,
		},
		{
			name:             "客户端接受多种压缩",
			sent:             "",
			accept:           "deflate, gzip, br",
			expectedRequest:  compress.CompressionIdentity,
			expectedResponse: "gzip", // 选择第一个支持的
			expectError:      false,
		},
		{
			name:             "客户端发送和接受不同压缩",
			sent:             "gzip",
			accept:           "br",
			expectedRequest:  "gzip",
			expectedResponse: "gzip", // 响应压缩应匹配请求压缩
			expectError:      false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reqComp, respComp, err := compress.NegotiateCompression(pools, tc.sent, tc.accept)

			if tc.expectError {
				assert.Error(t, err)
				if tc.expectedErrorCode != 0 {
					var srpcErr *errors.Error
					ok := errors.As(err, &srpcErr)
					assert.True(t, ok, "错误应该是Connect错误")
					assert.Equal(t, tc.expectedErrorCode, srpcErr.Code(), "错误代码应该匹配")
				}
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tc.expectedRequest, reqComp, "请求压缩应该匹配预期")
				assert.Equal(t, tc.expectedResponse, respComp, "响应压缩应该匹配预期")
			}
		})
	}
}

// 测试ReadOnlyCompressionPools接口
func TestReadOnlyCompressionPools_Gzip(t *testing.T) {
	gzipPool := compress.NewCompressionPool(gzipCompressor, gzipDecompressor, mem.DefaultBufferPool())

	pools := compress.NewReadOnlyCompressionPools(
		map[string]*compress.CompressionPool{
			"gzip": gzipPool,
		},
		[]string{"gzip"},
	)

	// 测试Get方法
	assert.Nil(t, pools.Get(""), "空名称应返回nil")
	assert.Nil(t, pools.Get(compress.CompressionIdentity), "identity压缩应返回nil")
	assert.Equal(t, gzipPool, pools.Get("gzip"), "应返回正确的gzip池")
	assert.Nil(t, pools.Get("br"), "不支持的压缩应返回nil")

	// 测试Contains方法
	assert.True(t, pools.Contains("gzip"), "应包含gzip")
	assert.False(t, pools.Contains("br"), "不应包含br")
	assert.False(t, pools.Contains(""), "不应包含空字符串")

	// 测试CommaSeparatedNames方法
	names := pools.CommaSeparatedNames()
	assert.Equal(t, "gzip", names, "名称应该逗号分隔并按优先顺序")
}

// 测试真实gzip压缩与解压缩的完整流程
func TestGzipCompressionFullWorkflow(t *testing.T) {
	testData := "这是一段需要压缩的测试数据，包含一些重复内容。重复内容重复内容重复内容。"

	// 1. 创建压缩池
	pool := compress.NewCompressionPool(gzipCompressor, gzipDecompressor, mem.DefaultBufferPool())
	assert.NotNil(t, pool)

	// 2. 准备源数据
	var src mem.BufferSlice
	w := mem.NewWriter(&src, mem.DefaultBufferPool())
	_, err := w.Write([]byte(testData))
	assert.NoError(t, err)
	originalLen := src.Len()

	// 3. 压缩数据
	var compressed mem.BufferSlice
	compressed, err = pool.Compress(src)
	defer compressed.Free()
	assert.NoError(t, err)
	compressedLen := compressed.Len()

	// 4. 验证压缩效果
	t.Logf("原始大小: %d, 压缩后大小: %d, 压缩率: %.2f%%",
		originalLen, compressedLen, float64(compressedLen)/float64(originalLen)*100)

	// 对于有重复内容的数据，压缩率应该有效果
	assert.Less(t, compressedLen, originalLen)

	// 5. 解压缩数据
	decompressed, err := pool.Decompress(compressed, 0)
	defer decompressed.Free()
	assert.NoError(t, err)

	// 6. 验证解压缩后的数据与原始数据一致
	assert.NoError(t, err)
	assert.Equal(t, testData, string(decompressed.Materialize()))
}
