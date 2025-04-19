package connect

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/opensraph/srpc/encoding"
	"github.com/opensraph/srpc/errors"
	"github.com/opensraph/srpc/internal/headers"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
)

const (
	defaultAnyResolverPrefix = "type.googleapis.com/"
)

type connectWireDetail struct {
	pbAny    *anypb.Any
	pbInner  proto.Message // if nil, must be extracted from pbAny
	wireJSON string        // preserve human-readable JSON
}

// NewErrorDetail constructs a new error detail. If msg is an *[anypb.Any] then
// it is used as is. Otherwise, it is first marshalled into an *[anypb.Any]
// value. This returns an error if msg cannot be marshalled.
func NewErrorDetail(msg proto.Message) (*connectWireDetail, error) {
	// If it's already an Any, don't wrap it inside another.
	if pb, ok := msg.(*anypb.Any); ok {
		return &connectWireDetail{pbAny: pb}, nil
	}
	pb, err := anypb.New(msg)
	if err != nil {
		return nil, err
	}
	return &connectWireDetail{pbAny: pb, pbInner: msg}, nil
}

func newConnectWireError(err error) *connectWireError {
	wire := &connectWireError{
		Code:    errors.Unknown,
		Message: err.Error(),
	}
	if connectErr, ok := errors.AsError(err); ok {
		wire.Code = connectErr.Code()
		wire.Message = connectErr.Error()
		if len(connectErr.Details()) > 0 {
			wire.Details = make([]*connectWireDetail, len(connectErr.Details()))
			for _, detail := range connectErr.Details() {
				wire.Details = append(wire.Details, detail.(*connectWireDetail))
			}
		}
	}
	return wire
}

func (d *connectWireDetail) MarshalJSON() ([]byte, error) {
	if d.wireJSON != "" {
		// If we unmarshaled this detail from JSON, return the original data. This
		// lets proxies w/o protobuf descriptors preserve human-readable details.
		return []byte(d.wireJSON), nil
	}
	wire := struct {
		Type  string          `json:"type"`
		Value string          `json:"value"`
		Debug json.RawMessage `json:"debug,omitempty"`
	}{
		Type:  typeNameFromURL(d.pbAny.GetTypeUrl()),
		Value: base64.RawStdEncoding.EncodeToString(d.pbAny.GetValue()),
	}
	// Try to produce debug info, but expect failure when we don't have
	// descriptors.
	msg, err := d.GetInner()
	if err == nil {
		codec := encoding.GetCodec(encoding.CodecNameJSON)
		debug, err := codec.Marshal(msg)
		if err == nil {
			wire.Debug = debug.Materialize()
		}
	}
	return json.Marshal(wire)
}

func (d *connectWireDetail) UnmarshalJSON(data []byte) error {
	var wire struct {
		Type  string `json:"type"`
		Value string `json:"value"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	if !strings.Contains(wire.Type, "/") {
		wire.Type = defaultAnyResolverPrefix + wire.Type
	}
	decoded, err := headers.DecodeBinaryHeader(wire.Value)
	if err != nil {
		return fmt.Errorf("decode base64: %w", err)
	}
	*d = connectWireDetail{
		pbAny: &anypb.Any{
			TypeUrl: wire.Type,
			Value:   decoded,
		},
		wireJSON: string(data),
	}
	return nil
}

func (d *connectWireDetail) GetInner() (proto.Message, error) {
	if d.pbInner != nil {
		return d.pbInner, nil
	}
	return d.pbAny.UnmarshalNew()
}

type connectWireError struct {
	Code    errors.Code          `json:"code"`
	Message string               `json:"message,omitempty"`
	Details []*connectWireDetail `json:"details,omitempty"`
}

func (e *connectWireError) asError() *errors.Error {
	if e == nil {
		return nil
	}
	if e.Code < errors.MinCode || e.Code > errors.MaxCode {
		e.Code = errors.Unknown
	}
	err := errors.NewWireError(e.Code, errors.New(e.Message))
	if len(e.Details) > 0 {
		for _, detail := range e.Details {
			err.WithDetails(detail.pbAny)
		}
	}
	return err
}

func (e *connectWireError) UnmarshalJSON(data []byte) error {
	// We want to be lenient if the JSON has an unrecognized or invalid code.
	// So if that occurs, we leave the code unset but can still de-serialize
	// the other fields from the input JSON.
	var wireError struct {
		Code    string               `json:"code"`
		Message string               `json:"message"`
		Details []*connectWireDetail `json:"details"`
	}
	err := json.Unmarshal(data, &wireError)
	if err != nil {
		return err
	}
	e.Message = wireError.Message
	e.Details = wireError.Details
	// This will leave e.Code unset if we can't unmarshal the given string.
	e.Code.UnmarshalJSON([]byte(wireError.Code))

	_ = e.Code.UnmarshalText([]byte(wireError.Code))
	return nil
}

func typeNameFromURL(url string) string {
	return url[strings.LastIndexByte(url, '/')+1:]
}
