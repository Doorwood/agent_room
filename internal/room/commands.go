package room

import "context"

type result[T any] struct {
	value T
	err   error
}

type recoverCommand struct {
	ctx context.Context
	res chan error
}

type resolveCommand struct {
	ctx   context.Context
	actor Actor
	input RecoverInput
	res   chan error
}

type submitCommand struct {
	ctx   context.Context
	actor Actor
	input SubmitInput
	note  bool
	res   chan result[Acceptance]
}

type controlCommand struct {
	ctx   context.Context
	actor Actor
	input ControlInput
	res   chan result[Acceptance]
}

type snapshotCommand struct {
	ctx context.Context
	res chan result[Snapshot]
}
