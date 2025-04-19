package envelope

import (
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/opensraph/srpc/mem"
	"github.com/stretchr/testify/assert"
)

func TestEnvelope(t *testing.T) {
	t.Parallel()
	payload := []byte(`{"number": 42}`)
	head, err := makeEnvelopePrefix(0, len(payload))
	assert.Nil(t, err)
	buf := &bytes.Buffer{}
	buf.Write(head[:])
	buf.Write(payload)
	t.Run("read", func(t *testing.T) {
		t.Parallel()
		t.Run("full", func(t *testing.T) {
			t.Parallel()
			env := &Envelope{Data: mem.BufferSlice{mem.SliceBuffer{}}}
			rdr := EnvelopeReader{
				Reader: bytes.NewReader(buf.Bytes()),
			}
			assert.Nil(t, rdr.Read(env))
			assert.Equal(t, payload, env.Data.Materialize())
		})
		t.Run("byteByByte", func(t *testing.T) {
			t.Parallel()
			env := &Envelope{Data: mem.BufferSlice{mem.SliceBuffer{}}}
			rdr := EnvelopeReader{
				Ctx: context.Background(),
				Reader: byteByByteReader{
					reader: bytes.NewReader(buf.Bytes()),
				},
			}
			assert.Nil(t, rdr.Read(env))
			assert.Equal(t, payload, env.Data.Materialize())
		})
	})
	t.Run("write", func(t *testing.T) {
		t.Parallel()
		t.Run("full", func(t *testing.T) {
			t.Parallel()
			dst := &bytes.Buffer{}
			wtr := EnvelopeWriter{
				Sender: WriteSender{Writer: dst},
			}
			env := &Envelope{Data: mem.BufferSlice{mem.SliceBuffer(payload)}}
			err := wtr.Write(env)
			assert.Nil(t, err)
			assert.Equal(t, buf.Bytes(), dst.Bytes())
		})
		t.Run("partial", func(t *testing.T) {
			t.Parallel()
			dst := &bytes.Buffer{}
			env := &Envelope{Data: mem.BufferSlice{mem.SliceBuffer(payload)}}
			_, err := io.CopyN(dst, env, 2)
			assert.Nil(t, err)
			_, err = env.WriteTo(dst)
			assert.Nil(t, err)
			assert.Equal(t, buf.Bytes(), dst.Bytes())
		})
	})
	t.Run("seek", func(t *testing.T) {
		t.Parallel()
		t.Run("start", func(t *testing.T) {
			t.Parallel()
			dst1 := &bytes.Buffer{}
			dst2 := &bytes.Buffer{}
			env := &Envelope{Data: mem.BufferSlice{mem.SliceBuffer(payload)}}
			_, err := io.CopyN(dst1, env, 2)
			assert.Nil(t, err)
			assert.Equal(t, env.Len(), len(payload)+3)
			_, err = env.Seek(0, io.SeekStart)
			assert.Nil(t, err)
			assert.Equal(t, env.Len(), len(payload)+5)
			_, err = io.CopyN(dst2, env, 2)
			assert.Nil(t, err)
			assert.Equal(t, dst1.Bytes(), dst2.Bytes())
			_, err = env.WriteTo(dst2)
			assert.Nil(t, err)
			assert.Equal(t, dst2.Bytes(), buf.Bytes())
			assert.Equal(t, env.Len(), 0)
		})
	})
}

// byteByByteReader is test reader that reads a single byte at a time.
type byteByByteReader struct {
	reader io.ByteReader
}

func (b byteByByteReader) Read(data []byte) (int, error) {
	if len(data) == 0 {
		return 0, nil
	}
	next, err := b.reader.ReadByte()
	if err != nil {
		return 0, err
	}
	data[0] = next
	return 1, nil
}
