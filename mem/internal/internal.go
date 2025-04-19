package internal

var (
	// SetDefaultBufferPoolForTesting updates the default buffer pool, for
	// testing purposes.
	SetDefaultBufferPoolForTesting any // func(mem.BufferPool)

	// SetBufferPoolingThresholdForTesting updates the buffer pooling threshold, for
	// testing purposes.
	SetBufferPoolingThresholdForTesting any // func(int)
)
