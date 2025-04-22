package connect

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/opensraph/srpc/encoding"
	"github.com/opensraph/srpc/errors"
	"github.com/opensraph/srpc/internal/envelope"
	"github.com/opensraph/srpc/mem"
	"github.com/opensraph/srpc/protocol"
	"github.com/stretchr/testify/assert"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/known/durationpb"
)

func TestConnectErrorDetailMarshaling(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name        string
		errorDetail proto.Message
		expectDebug any
	}{
		{
			name: "normal",
			errorDetail: &descriptorpb.FieldOptions{
				Deprecated: proto.Bool(true),
				Jstype:     descriptorpb.FieldOptions_JS_STRING.Enum(),
			},
			expectDebug: map[string]any{
				"deprecated": true,
				"jstype":     "JS_STRING",
			},
		},
		{
			name:        "well-known type with custom JSON",
			errorDetail: durationpb.New(time.Second),
			expectDebug: "1s", // special JS representation as duration string
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			detail, err := NewErrorDetail(testCase.errorDetail)
			assert.Nil(t, err)
			data, err := json.Marshal(detail)
			assert.Nil(t, err)
			t.Logf("marshaled error detail: %s", string(data))

			var unmarshaled connectWireDetail
			assert.Nil(t, json.Unmarshal(data, &unmarshaled))
			assert.Equal(t, unmarshaled.wireJSON, string(data))
			assert.Equal(t, unmarshaled.pbAny, detail.pbAny)

			var extractDetails struct {
				Debug any `json:"debug"`
			}
			assert.Nil(t, json.Unmarshal(data, &extractDetails))
			assert.Equal(t, extractDetails.Debug, testCase.expectDebug)
		})
	}
}

func TestConnectErrorDetailMarshalingNoDescriptor(t *testing.T) {
	t.Parallel()
	raw := `{"type":"acme.user.v1.User","value":"DEADBF",` +
		`"debug":{"email":"someone@connectrpc.com"}}`
	var detail connectWireDetail
	assert.Nil(t, json.Unmarshal([]byte(raw), &detail))
	assert.Equal(t, detail.pbAny.GetTypeUrl(), defaultAnyResolverPrefix+"acme.user.v1.User")

	_, err := detail.pbAny.UnmarshalNew()
	assert.NotNil(t, err)
	assert.True(t, strings.HasSuffix(err.Error(), "not found"))

	encoded, err := json.Marshal(&detail)
	assert.Nil(t, err)
	assert.Equal(t, string(encoded), raw)
}

func TestConnectEndOfResponseCanonicalTrailers(t *testing.T) {
	t.Parallel()

	buffer := bytes.Buffer{}
	bufferPool := mem.DefaultBufferPool()

	endStreamMessage := connectEndStreamMessage{Trailer: make(http.Header)}
	endStreamMessage.Trailer["not-canonical-header"] = []string{"a"}
	endStreamMessage.Trailer["mixed-Canonical"] = []string{"b"}
	endStreamMessage.Trailer["Mixed-Canonical"] = []string{"b"}
	endStreamMessage.Trailer["Canonical-Header"] = []string{"c"}
	endStreamData, err := json.Marshal(endStreamMessage)
	assert.Nil(t, err)

	writer := envelope.EnvelopeWriter{
		Sender:     envelope.WriteSender{Writer: &buffer},
		BufferPool: bufferPool,
	}
	err = writer.Write(&envelope.Envelope{
		Flags: connectFlagEnvelopeEndStream,
		Data:  mem.BufferSlice{mem.SliceBuffer(endStreamData)},
	})
	assert.Nil(t, err)

	unmarshaler := connectStreamingUnmarshaler{
		EnvelopeReader: envelope.EnvelopeReader{
			Ctx:        context.Background(),
			Reader:     &buffer,
			BufferPool: bufferPool,
		},
	}
	err = unmarshaler.Unmarshal(nil) // parameter won't be used
	assert.ErrorIs(t, err, envelope.ErrSpecialEnvelope)
	assert.Equal(t, unmarshaler.Trailer().Values("Not-Canonical-Header"), []string{"a"})
	assert.Equal(t, unmarshaler.Trailer().Values("Mixed-Canonical"), []string{"b", "b"})
	assert.Equal(t, unmarshaler.Trailer().Values("Canonical-Header"), []string{"c"})
}

func TestConnectValidateUnaryResponseContentType(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		codecName            string
		get                  bool
		statusCode           int
		responseContentType  string
		expectCode           errors.Code
		expectBadContentType bool
		expectNotModified    bool
	}{
		// Allowed content-types for OK responses.
		{
			codecName:           encoding.CodecNameProto,
			statusCode:          http.StatusOK,
			responseContentType: "application/proto",
		},
		{
			codecName:           encoding.CodecNameJSON,
			statusCode:          http.StatusOK,
			responseContentType: "application/json",
		},
		{
			codecName:           encoding.CodecNameJSON,
			statusCode:          http.StatusOK,
			responseContentType: "application/json; charset=utf-8",
		},
		{
			codecName:           encoding.CodecNameJSONCharsetUTF8,
			statusCode:          http.StatusOK,
			responseContentType: "application/json",
		},
		{
			codecName:           encoding.CodecNameJSONCharsetUTF8,
			statusCode:          http.StatusOK,
			responseContentType: "application/json; charset=utf-8",
		},
		// Allowed content-types for error responses.
		{
			codecName:           encoding.CodecNameProto,
			statusCode:          http.StatusNotFound,
			responseContentType: "application/json",
		},
		{
			codecName:           encoding.CodecNameProto,
			statusCode:          http.StatusBadRequest,
			responseContentType: "application/json; charset=utf-8",
		},
		{
			codecName:           encoding.CodecNameJSON,
			statusCode:          http.StatusInternalServerError,
			responseContentType: "application/json",
		},
		{
			codecName:           encoding.CodecNameJSON,
			statusCode:          http.StatusPreconditionFailed,
			responseContentType: "application/json; charset=utf-8",
		},
		// 304 Not Modified for GET request gets a special error, regardless of content-type
		{
			codecName:           encoding.CodecNameProto,
			get:                 true,
			statusCode:          http.StatusNotModified,
			responseContentType: "application/json",
			expectCode:          errors.Unknown,
			expectNotModified:   true,
		},
		{
			codecName:           encoding.CodecNameJSON,
			get:                 true,
			statusCode:          http.StatusNotModified,
			responseContentType: "application/json",
			expectCode:          errors.Unknown,
			expectNotModified:   true,
		},
		// OK status, invalid content-type
		{
			codecName:            encoding.CodecNameProto,
			statusCode:           http.StatusOK,
			responseContentType:  "application/proto; charset=utf-8",
			expectCode:           errors.Internal,
			expectBadContentType: true,
		},
		{
			codecName:            encoding.CodecNameProto,
			statusCode:           http.StatusOK,
			responseContentType:  "application/json",
			expectCode:           errors.Internal,
			expectBadContentType: true,
		},
		{
			codecName:            encoding.CodecNameJSON,
			statusCode:           http.StatusOK,
			responseContentType:  "application/proto",
			expectCode:           errors.Internal,
			expectBadContentType: true,
		},
		{
			codecName:            encoding.CodecNameJSON,
			statusCode:           http.StatusOK,
			responseContentType:  "some/garbage",
			expectCode:           errors.Unknown, // doesn't even look like it could be connect protocol
			expectBadContentType: true,
		},
		// Error status, invalid content-type, returns code based on HTTP status code
		{
			codecName:           encoding.CodecNameProto,
			statusCode:          http.StatusNotFound,
			responseContentType: "application/proto",
			expectCode:          errors.HttpToCode(http.StatusNotFound),
		},
		{
			codecName:           encoding.CodecNameJSON,
			statusCode:          http.StatusBadRequest,
			responseContentType: "some/garbage",
			expectCode:          errors.HttpToCode(http.StatusBadRequest),
		},
		{
			codecName:           encoding.CodecNameJSON,
			statusCode:          http.StatusTooManyRequests,
			responseContentType: "some/garbage",
			expectCode:          errors.HttpToCode(http.StatusTooManyRequests),
		},
	}
	for _, testCase := range testCases {
		httpMethod := http.MethodPost
		if testCase.get {
			httpMethod = http.MethodGet
		}
		testCaseName := fmt.Sprintf("%s_%s->%d_%s", httpMethod, testCase.codecName, testCase.statusCode, testCase.responseContentType)
		t.Run(testCaseName, func(t *testing.T) {
			t.Parallel()
			err := connectValidateUnaryResponseContentType(
				testCase.codecName,
				httpMethod,
				testCase.statusCode,
				http.StatusText(testCase.statusCode),
				testCase.responseContentType,
			)
			if testCase.expectCode == 0 {
				assert.Nil(t, err)
			} else if assert.NotNil(t, err) {
				assert.Equal(t, errors.CodeOf(err), testCase.expectCode)
				switch {
				case testCase.expectNotModified:
					assert.ErrorIs(t, err, errors.ErrNotModified)
				case testCase.expectBadContentType:
					assert.True(t, strings.Contains(err.Error(), fmt.Sprintf("invalid content-type: %q; expecting", testCase.responseContentType)))
				default:
					assert.Equal(t, err.Error(), http.StatusText(testCase.statusCode))
				}
			}
		})
	}
}

func TestConnectValidateStreamResponseContentType(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		codecName           string
		responseContentType string
		expectCode          errors.Code
	}{
		// Allowed content-types
		{
			codecName:           encoding.CodecNameProto,
			responseContentType: "application/connect+proto",
		},
		{
			codecName:           encoding.CodecNameJSON,
			responseContentType: "application/connect+json",
		},
		// Mismatched response codec
		{
			codecName:           encoding.CodecNameProto,
			responseContentType: "application/connect+json",
			expectCode:          errors.Internal,
		},
		{
			codecName:           encoding.CodecNameJSON,
			responseContentType: "application/connect+proto",
			expectCode:          errors.Internal,
		},
		// Disallowed content-types
		{
			codecName:           encoding.CodecNameJSON,
			responseContentType: "application/connect+json; charset=utf-8",
			expectCode:          errors.Internal, // *almost* looks right
		},
		{
			codecName:           encoding.CodecNameProto,
			responseContentType: "application/proto",
			expectCode:          errors.Unknown,
		},
		{
			codecName:           encoding.CodecNameJSON,
			responseContentType: "application/json",
			expectCode:          errors.Unknown,
		},
		{
			codecName:           encoding.CodecNameJSON,
			responseContentType: "application/json; charset=utf-8",
			expectCode:          errors.Unknown,
		},
		{
			codecName:           encoding.CodecNameProto,
			responseContentType: "some/garbage",
			expectCode:          errors.Unknown,
		},
	}
	for _, testCase := range testCases {
		testCaseName := fmt.Sprintf("%s->%s", testCase.codecName, testCase.responseContentType)
		t.Run(testCaseName, func(t *testing.T) {
			t.Parallel()
			err := connectValidateStreamResponseContentType(
				testCase.codecName,
				protocol.StreamTypeServer,
				testCase.responseContentType,
			)
			if testCase.expectCode == 0 {
				assert.Nil(t, err)
			} else if assert.NotNil(t, err) {
				assert.Equal(t, errors.CodeOf(err), testCase.expectCode)
				assert.True(t, strings.Contains(err.Error(), fmt.Sprintf("invalid content-type: %q; expecting", testCase.responseContentType)))
			}
		})
	}
}
