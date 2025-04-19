package protocol

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestStreamTypeString(t *testing.T) {
	assert.True(t, StreamTypeUnary&StreamTypeClient == 0)
	assert.True(t, StreamTypeServer&StreamTypeClient == 0)
	assert.True(t, StreamTypeClient&StreamTypeClient != 0)
	assert.True(t, StreamTypeBidi&StreamTypeClient != 0)

	assert.Equal(t, StreamTypeUnary&StreamTypeServer, StreamTypeServer)
	assert.Equal(t, StreamTypeServer&StreamTypeServer, StreamTypeServer)
	assert.NotEqual(t, StreamTypeClient&StreamTypeServer, StreamTypeServer)
	assert.NotEqual(t, StreamTypeBidi&StreamTypeServer, StreamTypeServer)
}
