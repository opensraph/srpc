package protobinary

import (
	"fmt"

	"github.com/opensraph/srpc/encoding"
	"github.com/opensraph/srpc/mem"

	"google.golang.org/protobuf/proto"
)

const Name = encoding.CodecNameProto

func init() {
	encoding.RegisterCodec(&protoBinary{})
}

var _ encoding.Codec = (*protoBinary)(nil)

type protoBinary struct{}

// Marshal implements codec.Codec.
func (c *protoBinary) Marshal(v any) (data mem.BufferSlice, err error) {
	vv := encoding.MessageV2Of(v)
	if vv == nil {
		return nil, fmt.Errorf("proto: failed to marshal, message is %T, want proto.Message", v)
	}

	size := proto.Size(vv)
	if mem.IsBelowBufferPoolingThreshold(size) {
		buf, err := proto.Marshal(vv)
		if err != nil {
			return nil, err
		}
		data = append(data, mem.SliceBuffer(buf))
	} else {
		pool := mem.DefaultBufferPool()
		buf := pool.Get(size)
		if _, err := (proto.MarshalOptions{}).MarshalAppend((*buf)[:0], vv); err != nil {
			pool.Put(buf)
			return nil, err
		}
		data = append(data, mem.NewBuffer(buf, pool))
	}

	return data, nil
}

// Unmarshal implements codec.Codec.
func (c *protoBinary) Unmarshal(data mem.BufferSlice, v any) error {
	vv := encoding.MessageV2Of(v)
	if vv == nil {
		return fmt.Errorf("failed to unmarshal, message is %T, want proto.Message", v)
	}

	buf := data.MaterializeToBuffer(mem.DefaultBufferPool())
	defer buf.Free()
	return proto.Unmarshal(buf.ReadOnlyData(), vv)
}

// Name implements codec.Codec.
func (c *protoBinary) Name() string {
	return Name
}
