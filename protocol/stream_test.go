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

	assert.Equal(t, StreamTypeUnary&StreamTypeServer, StreamTypeUnary)
	assert.Equal(t, StreamTypeServer&StreamTypeServer, StreamTypeServer)
	assert.Equal(t, StreamTypeClient&StreamTypeServer, StreamTypeUnary)
	assert.Equal(t, StreamTypeBidi&StreamTypeServer, StreamTypeServer)
}
