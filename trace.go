package srpc

type traceEventLog interface {
	Printf(format string, a ...any)
	Errorf(format string, a ...any)
}
