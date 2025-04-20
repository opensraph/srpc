package connect

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/opensraph/srpc/compress"
	"github.com/opensraph/srpc/internal/duplex"
	"github.com/opensraph/srpc/internal/envelope"
	"github.com/opensraph/srpc/internal/headers"
	"github.com/opensraph/srpc/protocol"
)

type connectClient struct {
	protocol.ProtocolClientParams

	peer protocol.Peer
}

func (c *connectClient) Peer() protocol.Peer {
	return c.peer
}

func (c *connectClient) WriteRequestHeader(streamType protocol.StreamType, header http.Header) {
	// We know these header keys are in canonical form, so we can bypass all the
	// checks in Header.Set.
	if headers.GetHeaderCanonical(header, headers.HeaderUserAgent) == "" {
		header[headers.HeaderUserAgent] = []string{defaultConnectUserAgent}
	}
	header[connectHeaderProtocolVersion] = []string{connectProtocolVersion}
	header[headers.HeaderContentType] = []string{
		connectContentTypeFromCodecName(streamType, c.Codec.Name()),
	}
	acceptCompressionHeader := connectUnaryHeaderAcceptCompression
	if streamType != protocol.StreamTypeUnary {
		// If we don't set Accept-Encoding, by default http.Client will ask the
		// server to compress the whole stream. Since we're already compressing
		// each message, this is a waste.
		header[connectUnaryHeaderAcceptCompression] = []string{compress.CompressionIdentity}
		acceptCompressionHeader = connectStreamingHeaderAcceptCompression
		// We only write the request encoding header here for streaming calls,
		// since the streaming envelope lets us choose whether to compress each
		// message individually. For unary, we won't know whether we're compressing
		// the request until we see how large the payload is.
		if c.CompressionName != "" && c.CompressionName != compress.CompressionIdentity {
			header[connectStreamingHeaderCompression] = []string{c.CompressionName}
		}
	}
	if acceptCompression := c.CompressionPools.CommaSeparatedNames(); acceptCompression != "" {
		header[acceptCompressionHeader] = []string{acceptCompression}
	}
}

func (c *connectClient) NewConn(
	ctx context.Context,
	spec protocol.Spec,
	header http.Header,
) protocol.StreamingClientConn {
	if deadline, ok := ctx.Deadline(); ok {
		millis := int64(time.Until(deadline) / time.Millisecond)
		if millis > 0 {
			encoded := strconv.FormatInt(millis, 10 /* base */)
			if len(encoded) <= 10 {
				header[connectHeaderTimeout] = []string{encoded}
			} // else effectively unbounded
		}
	}
	duplexCall := duplex.NewDuplexHTTPCall(ctx, c.HTTPClient, c.URL, protocol.IsSendUnary(spec.StreamType), header)
	var conn protocol.StreamingClientConn
	if spec.StreamType == protocol.StreamTypeUnary {
		unaryConn := &connectUnaryClientConn{
			spec:             spec,
			peer:             c.Peer(),
			duplexCall:       duplexCall,
			compressionPools: c.CompressionPools,
			bufferPool:       c.BufferPool,
			marshaler: connectUnaryRequestMarshaler{
				connectUnaryMarshaler: connectUnaryMarshaler{
					ctx:              ctx,
					sender:           duplexCall,
					codec:            c.Codec,
					compressMinBytes: c.CompressMinBytes,
					compressionName:  c.CompressionName,
					compressionPool:  c.CompressionPools.Get(c.CompressionName),
					bufferPool:       c.BufferPool,
					header:           duplexCall.Header(),
					sendMaxBytes:     c.SendMaxBytes,
				},
			},
			unmarshaler: connectUnaryUnmarshaler{
				ctx:          ctx,
				reader:       duplexCall,
				codec:        c.Codec,
				bufferPool:   c.BufferPool,
				readMaxBytes: c.ReadMaxBytes,
			},
			responseHeader:  make(http.Header),
			responseTrailer: make(http.Header),
		}
		if spec.IdempotencyLevel == protocol.IdempotencyNoSideEffects {
			unaryConn.marshaler.enableGet = c.EnableGet
			unaryConn.marshaler.getURLMaxBytes = c.GetURLMaxBytes
			unaryConn.marshaler.getUseFallback = c.GetUseFallback
			unaryConn.marshaler.duplexCall = duplexCall
			if stableCodec, ok := c.Codec.(stableCodec); ok {
				unaryConn.marshaler.stableCodec = stableCodec
			}
		}
		conn = unaryConn
		duplexCall.SetValidateResponse(unaryConn.validateResponse)
	} else {
		streamingConn := &connectStreamingClientConn{
			spec:             spec,
			peer:             c.Peer(),
			duplexCall:       duplexCall,
			compressionPools: c.CompressionPools,
			bufferPool:       c.BufferPool,
			codec:            c.Codec,
			marshaler: connectStreamingMarshaler{
				EnvelopeWriter: envelope.EnvelopeWriter{
					Ctx:              ctx,
					Sender:           duplexCall,
					Codec:            c.Codec,
					CompressMinBytes: c.CompressMinBytes,
					CompressionName:  c.CompressionName,
					CompressionPool:  c.CompressionPools.Get(c.CompressionName),
					BufferPool:       c.BufferPool,
					SendMaxBytes:     c.SendMaxBytes,
				},
			},
			unmarshaler: connectStreamingUnmarshaler{
				EnvelopeReader: envelope.EnvelopeReader{
					Ctx:             ctx,
					Reader:          duplexCall,
					Codec:           c.Codec,
					BufferPool:      c.BufferPool,
					ReadMaxBytes:    c.ReadMaxBytes,
					CompressionName: c.CompressionName,
					CompressionPool: c.CompressionPools.Get(c.CompressionName),
				},
			},
			responseHeader:  make(http.Header),
			responseTrailer: make(http.Header),
		}
		conn = streamingConn
		duplexCall.SetValidateResponse(streamingConn.validateResponse)
	}
	return protocol.WrapClientConnWithCodedErrors(conn)
}
