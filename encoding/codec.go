package encoding

import (
	"strings"

	"github.com/opensraph/srpc/mem"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/protoadapt"
)

const (
	CodecNameProto           = "proto"
	CodecNameJSON            = "json"
	CodecNameJSONCharsetUTF8 = CodecNameJSON + "; charset=utf-8"
)

// Codec defines the interface gRPC uses to encode and decode messages.  Note
// that implementations of this interface must be thread safe; a Codec's
// methods can be called from concurrent goroutines.
type Codec interface {
	// Marshal returns the wire format of v. The buffers in the returned
	// [mem.BufferSlice] must have at least one reference each, which will be freed
	// by gRPC when they are no longer needed.
	Marshal(v any) (out mem.BufferSlice, err error)
	// Unmarshal parses the wire format into v. Note that data will be freed as soon
	// as this function returns. If the codec wishes to guarantee access to the data
	// after this function, it must take its own reference that it frees when it is
	// no longer needed.
	Unmarshal(data mem.BufferSlice, v any) error
	// Name returns the name of the Codec implementation. The returned string
	// will be used as part of content type in transmission.  The result must be
	// static; the result cannot change between calls.
	Name() string
}

var registeredCodecs = make(ReadOnlyCodecs)

// RegisterCodec registers the provided Codec for use with all gRPC clients and
// servers.
//
// The Codec will be stored and looked up by result of its Name() method, which
// should match the content-subtype of the encoding handled by the Codec.  This
// is case-insensitive, and is stored and looked up as lowercase.  If the
// result of calling Name() is an empty string, RegisterCodec will panic. See
// Content-Type on
// https://github.com/grpc/grpc/blob/master/doc/PROTOCOL-HTTP2.md#requests for
// more details.
//
// NOTE: this function must only be called during initialization time (i.e. in
// an init() function), and is not thread-safe.  If multiple Codecs are
// registered with the same name, the one registered last will take effect.
func RegisterCodec(codec Codec) {
	if codec == nil {
		panic("cannot register a nil Codec")
	}
	if codec.Name() == "" {
		panic("cannot register Codec with empty string result for Name()")
	}
	contentSubtype := strings.ToLower(codec.Name())
	registeredCodecs[contentSubtype] = codec
}

type ReadOnlyCodecs map[string]Codec

func (m ReadOnlyCodecs) Get(name string) Codec {
	return registeredCodecs[name]
}

func (m ReadOnlyCodecs) Protobuf() Codec {
	if pb, ok := m[CodecNameProto]; ok {
		return pb
	}

	panic("protobuf codec not registered")
}

func (m ReadOnlyCodecs) Names() []string {
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	return names
}

// GetCodec gets a registered Codec by content-subtype, or nil if no Codec is
// registered for the content-subtype.
//
// The content-subtype is expected to be lowercase.
func GetCodec(contentSubtype string) Codec {
	c, _ := registeredCodecs[contentSubtype]
	return c
}

func MessageV2Of(v any) proto.Message {
	switch v := v.(type) {
	case protoadapt.MessageV1:
		return protoadapt.MessageV2Of(v)
	case protoadapt.MessageV2:
		return v
	}

	return nil
}
