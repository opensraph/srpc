package protojson

import (
	"fmt"

	"github.com/opensraph/srpc/encoding"
	"github.com/opensraph/srpc/mem"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

const CodecNameJSON = encoding.CodecNameJSON
const CodecNameJSONCharsetUTF8 = encoding.CodecNameJSON + "; charset=utf-8"

func init() {
	encoding.RegisterCodec(&protoJson{CodecNameJSON})
	encoding.RegisterCodec(&protoJson{CodecNameJSONCharsetUTF8})
}

var _ encoding.Codec = (*protoJson)(nil)

type protoJson struct {
	name string
}

// Marshal implements codec.Codec.
func (c *protoJson) Marshal(v any) (data mem.BufferSlice, err error) {
	vv := encoding.MessageV2Of(v)
	if vv == nil {
		return nil, fmt.Errorf("proto: failed to marshal, message is %T, want proto.Message", v)
	}

	size := proto.Size(vv)
	if mem.IsBelowBufferPoolingThreshold(size) {
		buf, err := protojson.Marshal(vv)
		if err != nil {
			return nil, err
		}
		data = append(data, mem.SliceBuffer(buf))
	} else {
		pool := mem.DefaultBufferPool()
		buf := pool.Get(size)
		if _, err := (protojson.MarshalOptions{}).MarshalAppend((*buf)[:0], vv); err != nil {
			pool.Put(buf)
			return nil, err
		}
		data = append(data, mem.NewBuffer(buf, pool))
	}

	return data, nil
}

// Unmarshal implements codec.Codec.
func (c *protoJson) Unmarshal(data mem.BufferSlice, v any) (err error) {
	vv := encoding.MessageV2Of(v)
	if vv == nil {
		return fmt.Errorf("failed to unmarshal, message is %T, want proto.Message", v)
	}

	buf := data.MaterializeToBuffer(mem.DefaultBufferPool())
	defer buf.Free()
	return protojson.Unmarshal(buf.ReadOnlyData(), vv)
}

func (c *protoJson) Name() string {
	return c.name
}
