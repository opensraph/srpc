package stats

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDefineNewEvent(t *testing.T) {
	num0 := MaxEventNum()

	event1, err1 := DefineNewEvent("myevent", LevelDetailed)
	num1 := MaxEventNum()

	assert.True(t, err1 == nil)
	assert.True(t, event1 != nil)
	assert.True(t, num1 == num0+1)
	assert.True(t, event1.Level() == LevelDetailed)

	event2, err2 := DefineNewEvent("myevent", LevelBase)
	num2 := MaxEventNum()
	assert.True(t, err2 == ErrDuplicated)
	assert.True(t, event2 == event1)
	assert.True(t, num2 == num1)
	assert.True(t, event1.Level() == LevelDetailed)

	FinishInitialization()

	event3, err3 := DefineNewEvent("another", LevelDetailed)
	num3 := MaxEventNum()
	assert.True(t, err3 == ErrNotAllowed)
	assert.True(t, event3 == nil)
	assert.True(t, num3 == num1)
}
