package protocol

import (
	"net/http"

	"github.com/opensraph/srpc/errors"
)

func FlushResponseWriter(w http.ResponseWriter) {
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

// checkServerStreamsCanFlush ensures that bidi and server streaming handlers
// have received an http.ResponseWriter that implements http.Flusher, since
// they must flush data after sending each message.
func CheckServerStreamsCanFlush(streamType StreamType, responseWriter http.ResponseWriter) error {
	requiresFlusher := (streamType & StreamTypeServer) == StreamTypeServer
	if _, flushable := responseWriter.(http.Flusher); requiresFlusher && !flushable {
		return errors.Newf("%T does not implement http.Flusher", responseWriter).WithCode(errors.InvalidArgument)
	}
	return nil
}
